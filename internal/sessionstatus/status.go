// Package sessionstatus publishes observational metadata, never routing authority.
package sessionstatus

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/applock"
)

var ErrStatus = errors.New("session_status_unavailable")

type Session struct {
	Conversation string    `json:"conversation"`
	Project      string    `json:"project"`
	Worktree     string    `json:"worktree"`
	Branch       string    `json:"branch"`
	Slot         string    `json:"slot"`
	State        string    `json:"state"`
	UpdatedAt    time.Time `json:"updated_at"`
	Heartbeat    time.Time `json:"heartbeat"`
}

func hexID(s string, bytes int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == bytes && hex.EncodeToString(b) == s
}
func valid(s Session) bool {
	return hexID(s.Conversation, 16) && hexID(s.Project, 32) && hexID(s.Worktree, 32) && hexID(s.Branch, 32) &&
		(accountslot.Valid(s.Slot)) && (s.State == "active" || s.State == "waiting" || s.State == "failed" || s.State == "closed") &&
		!s.UpdatedAt.IsZero() && !s.Heartbeat.IsZero()
}

type Publisher struct {
	mu      sync.Mutex
	root    *os.Root
	s       Session
	done    chan struct{}
	stopped chan struct{}
}

// Start follows trusted CLI registration; account/worktree labels are local hashes.
func Start(dir string, s Session) (*Publisher, error) {
	s.State = "waiting"
	s.UpdatedAt, s.Heartbeat = time.Now().UTC(), time.Now().UTC()
	if !valid(s) {
		return nil, ErrStatus
	}
	lock, err := applock.Acquire(dir)
	if err != nil {
		return nil, ErrStatus
	}
	root, err := os.OpenRoot(dir)
	lock.Close()
	if err != nil {
		return nil, ErrStatus
	}
	p := &Publisher{root: root, s: s, done: make(chan struct{}), stopped: make(chan struct{})}
	if p.save() != nil {
		root.Close()
		return nil, ErrStatus
	}
	go func() {
		defer close(p.stopped)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-tick.C:
				p.mu.Lock()
				p.s.Heartbeat = time.Now().UTC()
				_ = p.save()
				p.mu.Unlock()
			}
		}
	}()
	return p, nil
}

func (p *Publisher) Set(state string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch state {
	case "active", "waiting", "failed", "closed":
	default:
		return
	}
	p.s.State, p.s.UpdatedAt, p.s.Heartbeat = state, time.Now().UTC(), time.Now().UTC()
	_ = p.save() // Observation failure never replays or blocks a model request.
}

// Close must be called once, after request handlers have drained.
func (p *Publisher) Close(state string) {
	if p == nil {
		return
	}
	close(p.done)
	<-p.stopped
	p.Set(state)
	p.root.Close()
}

func (p *Publisher) save() error {
	lock, err := mutationLock(p.root)
	if err != nil {
		return err
	}
	defer lock.Close()
	b, err := json.Marshal(p.s)
	if err != nil {
		return ErrStatus
	}
	name := "pending-" + rand.Text()
	f, err := p.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrStatus
	}
	defer p.root.Remove(name)
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return ErrStatus
	}
	return os.Rename(filepath.Join(p.root.Name(), name), filepath.Join(p.root.Name(), p.s.Conversation+".json"))
}

func Read(dir string, now time.Time) ([]Session, error) {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return []Session{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrStatus
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) {
		return nil, ErrStatus
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrStatus
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return nil, ErrStatus
	}
	defer f.Close()
	entries, err := f.ReadDir(4097)
	if err != nil && err != io.EOF || len(entries) > 4096 {
		return nil, ErrStatus
	}
	result := []Session{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || !hexID(strings.TrimSuffix(name, ".json"), 16) {
			continue
		}
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if os.IsNotExist(err) {
			continue // Periodic cleanup can remove an entry after enumeration.
		}
		if err != nil {
			return nil, ErrStatus
		}
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
			f.Close()
			return nil, ErrStatus
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		// Atomic replacement can unlink the inode after OpenFile; that pinned
		// descriptor is still a valid complete sample. Only extra links are unsafe.
		if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink > 1 {
			f.Close()
			return nil, ErrStatus
		}
		b, e := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		var s Session
		if e != nil || len(b) > 4096 || json.Unmarshal(b, &s) != nil || !valid(s) || name != s.Conversation+".json" {
			return nil, ErrStatus
		}
		age := now.Sub(s.Heartbeat)
		if age > 24*time.Hour {
			continue
		}
		if age < -time.Second || ((s.State == "active" || s.State == "waiting") && age >= 5*time.Second) {
			s.State = "disconnected"
		}
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool {
		live := func(s Session) bool { return s.State == "active" || s.State == "waiting" }
		if live(result[i]) != live(result[j]) {
			return live(result[i])
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	if len(result) > 64 {
		result = result[:64]
	}
	return result, nil
}

func Handler(dir, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(secret) < 32 || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Origin") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path != "/control/sessions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		now := time.Now().UTC()
		sessions, err := Read(dir, now)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Event       string    `json:"event"`
			GeneratedAt time.Time `json:"generated_at"`
			Sessions    []Session `json:"sessions"`
		}{"session_snapshot", now, sessions})
	})
}

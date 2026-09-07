// Package clirecord stores private CLI homes behind opaque local handles.
package clirecord

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ChoBazzi/codex-switcher/internal/applock"
	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
)

var ErrRecord = errors.New("cli_record_unavailable")

var ErrVerification = errors.New("cli_record_write_verification_failed")

type metadata struct {
	Origin checkpoint.Origin `json:"origin"`
	Model  string            `json:"model"`
	Ready  bool              `json:"ready"`
}

type Record struct {
	Handle, Home string
	data         metadata
	root         *os.Root
	lock         *applock.Lock
}

func ValidHandle(handle string) bool {
	b, err := hex.DecodeString(handle)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == handle
}

func Create(parent string, origin checkpoint.Origin, model string) (*Record, error) {
	if origin.Project == "" || origin.Worktree == "" || origin.Branch == "" || origin.Session != "" {
		return nil, ErrRecord
	}
	// Validate the shared private parent before creating a child.
	lock, err := applock.Acquire(parent)
	if err != nil {
		return nil, ErrRecord
	}
	defer lock.Close()
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, ErrRecord
	}
	handle := hex.EncodeToString(entropy[:])
	dir := filepath.Join(parent, handle)
	if os.Mkdir(dir, 0700) != nil {
		return nil, ErrRecord
	}
	r, err := open(dir, handle)
	if err != nil {
		return nil, err
	}
	r.data = metadata{Origin: origin, Model: model}
	if err := r.save(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// Load never creates a missing handle and never falls back to a new session.
func Load(parent, handle string, origin checkpoint.Origin) (*Record, error) {
	return load(parent, handle, origin, true)
}

// LoadForCheckpoint only grants a locked local record for Wiki inspection.
// It does not authorize model execution or alter the conversation ready flag.
func LoadForCheckpoint(parent, handle string, origin checkpoint.Origin) (*Record, error) {
	return load(parent, handle, origin, false)
}

func load(parent, handle string, origin checkpoint.Origin, requireReady bool) (*Record, error) {
	if !ValidHandle(handle) || origin.Session != "" {
		return nil, ErrRecord
	}
	dir := filepath.Join(parent, handle)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, ErrRecord
	}
	r, err := open(dir, handle)
	if err != nil {
		return nil, err
	}
	f, err := r.root.Open("record.json")
	if err != nil {
		r.Close()
		return nil, ErrRecord
	}
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	f.Close()
	if err != nil || len(data) > 8192 || json.Unmarshal(data, &r.data) != nil {
		r.Close()
		return nil, ErrRecord
	}
	base := r.data.Origin
	base.Session = ""
	if base != origin || (requireReady && !r.data.Ready) || !validThread(r.data.Origin.Session) {
		r.Close()
		return nil, ErrRecord
	}
	return r, nil
}

func open(dir, handle string) (*Record, error) {
	lock, err := applock.Acquire(dir)
	if err != nil {
		return nil, ErrRecord
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		lock.Close()
		return nil, ErrRecord
	}
	return &Record{Handle: handle, Home: dir, root: root, lock: lock}, nil
}

func (r *Record) Close()                    { r.root.Close(); r.lock.Close() }
func (r *Record) Origin() checkpoint.Origin { return r.data.Origin }
func (r *Record) Model() string             { return r.data.Model }

// Begin durably disables resume before starting the CLI. Crashes stay disabled.
func (r *Record) Begin() error { r.data.Ready = false; return r.save() }
func (r *Record) Started(thread string) error {
	if !validThread(thread) || r.data.Origin.Session != "" && r.data.Origin.Session != thread {
		return ErrRecord
	}
	r.data.Origin.Session = thread
	return r.save()
}
func (r *Record) Complete() error {
	if !validThread(r.data.Origin.Session) {
		return ErrRecord
	}
	r.data.Ready = true
	return r.save()
}

func validThread(id string) bool {
	h := http.Header{}
	h.Set("Thread-Id", id)
	h.Set("Session-Id", id)
	_, err := cliidentity.ThreadID(h)
	return err == nil
}

func (r *Record) save() error {
	b, err := json.Marshal(r.data)
	if err != nil {
		return ErrRecord
	}
	name := "record-" + rand.Text() + ".tmp"
	f, err := r.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrRecord
	}
	defer r.root.Remove(name)
	_, writeErr := f.Write(b)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || os.Rename(filepath.Join(r.Home, name), filepath.Join(r.Home, "record.json")) != nil {
		return ErrRecord
	}
	dir, err := r.root.Open(".")
	if err != nil {
		return ErrRecord
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrRecord
	}
	return r.verify(b)
}

// Reopen by path, as the next process does. A successful write alone must not
// authorize reporting a resumable record when the visible file differs.
func (r *Record) verify(expected []byte) error {
	f, err := os.Open(filepath.Join(r.Home, "record.json"))
	if err != nil {
		return ErrVerification
	}
	defer f.Close()
	actual, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || !bytes.Equal(actual, expected) {
		return ErrVerification
	}
	return nil
}

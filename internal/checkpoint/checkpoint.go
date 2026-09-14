// Package checkpoint validates locally published Wiki files without model calls.
package checkpoint

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const Timeout = 3 * time.Minute
const MaxBytes = 32 * 1024

type State string

const (
	Pending   State = "pending"
	Writing   State = "writing"
	Validated State = "validated"
	Failed    State = "failed"
	TimedOut  State = "timed_out"
	Cancelled State = "cancelled"
	Consumed  State = "consumed"
)

var (
	ErrState    = errors.New("checkpoint_state_conflict")
	ErrTimeout  = errors.New("checkpoint_timeout")
	ErrFile     = errors.New("checkpoint_file_unavailable")
	ErrFormat   = errors.New("checkpoint_invalid_format")
	ErrIdentity = errors.New("checkpoint_identity_mismatch")
	ErrSize     = errors.New("checkpoint_size_exceeded")
)

// Origin contains only app-local identifiers, never upstream credentials.
type Origin struct {
	Project  string `json:"project"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	Session  string `json:"session"`
}

type Metadata struct {
	ID     string `json:"checkpoint_id"`
	Origin Origin `json:"origin"`
}

// Snapshot is a copy of the exact bytes validated, independent of later edits.
type Snapshot struct {
	Metadata    Metadata
	Markdown    string
	SHA256      string
	ValidatedAt time.Time
}

type Status struct {
	ID       string
	State    State
	Deadline time.Time
	Error    string
}

// Request owns one registered session directory. Call Close when it is retired.
// Requests are in-memory in Phase 0; restarting never reissues a model request.
type Request struct {
	mu        sync.Mutex
	root      *os.Root
	meta      Metadata
	state     State
	deadline  time.Time
	snapshot  Snapshot
	errorCode string
	now       func() time.Time
}

func New(directory string, origin Origin) (*Request, error) {
	if origin.Project == "" || origin.Worktree == "" || origin.Branch == "" || origin.Session == "" {
		return nil, ErrIdentity
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrFile
	}
	return &Request{root: root, meta: Metadata{ID: rand.Text(), Origin: origin}, state: Pending, now: time.Now}, nil
}

func (r *Request) Close() error       { r.mu.Lock(); defer r.mu.Unlock(); return r.root.Close() }
func (r *Request) Metadata() Metadata { r.mu.Lock(); defer r.mu.Unlock(); return r.meta }

// MarkSent starts the deadline only when the Wiki instruction was dispatched.
func (r *Request) MarkSent() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != Pending {
		return ErrState
	}
	r.state, r.deadline = Writing, r.now().Add(Timeout)
	return nil
}

func (r *Request) expire() {
	if r.state == Writing && !r.now().Before(r.deadline) {
		r.state, r.errorCode = TimedOut, ErrTimeout.Error()
	}
}

func (r *Request) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire()
	return Status{r.meta.ID, r.state, r.deadline, r.errorCode}
}

func (r *Request) Cancel() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == Consumed || r.state == Cancelled {
		return ErrState
	}
	r.state = Cancelled
	return nil
}

// Validate is triggered by observing publication, not by polling the model.
func (r *Request) Validate() (Snapshot, error) { return r.check(false) }

// Recheck is a single user-requested local read. It never resets the deadline.
func (r *Request) Recheck() (Snapshot, error) { return r.check(true) }

func (r *Request) check(manual bool) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire()
	if r.state == Validated {
		return r.snapshot, nil
	}
	if r.state != Writing && !(manual && (r.state == Failed || r.state == TimedOut)) {
		if r.state == TimedOut {
			return Snapshot{}, ErrTimeout
		}
		return Snapshot{}, ErrState
	}
	// The name is fixed; O_NOFOLLOW prevents a final-component symlink race.
	f, err := r.root.OpenFile("checkpoint.md", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return r.fail(ErrFile)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return r.fail(ErrFile)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return r.fail(ErrFile)
	}
	if len(b) > MaxBytes {
		return r.fail(ErrSize)
	}
	if err := validate(b, r.meta); err != nil {
		return r.fail(err)
	}
	// Validation itself is included in the automatic deadline.
	if !manual && !r.now().Before(r.deadline) {
		r.state, r.errorCode = TimedOut, ErrTimeout.Error()
		return Snapshot{}, ErrTimeout
	}
	hash := sha256.Sum256(b)
	r.snapshot = Snapshot{r.meta, string(b), hex.EncodeToString(hash[:]), r.now()}
	r.state, r.errorCode = Validated, ""
	return r.snapshot, nil
}

func (r *Request) fail(err error) (Snapshot, error) {
	r.state, r.errorCode = Failed, err.Error()
	return Snapshot{}, err
}

func (r *Request) Consume() (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != Validated {
		return Snapshot{}, ErrState
	}
	r.state = Consumed
	return r.snapshot, nil
}

func validate(b []byte, want Metadata) error {
	if !utf8.Valid(b) {
		return ErrFormat
	}
	line, body, ok := strings.Cut(string(b), "\n")
	const prefix = "<!-- codex-switcher:"
	if !ok || !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, " -->") {
		return ErrFormat
	}
	var got Metadata
	d := json.NewDecoder(strings.NewReader(strings.TrimSuffix(strings.TrimPrefix(line, prefix), " -->")))
	d.DisallowUnknownFields()
	if d.Decode(&got) != nil {
		return ErrFormat
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrFormat
	}
	if got != want {
		return ErrIdentity
	}
	end := "<!-- checkpoint-complete:" + want.ID + " -->"
	body = strings.TrimSpace(body)
	if !strings.HasSuffix(body, end) {
		return ErrFormat
	}
	body = strings.TrimSpace(strings.TrimSuffix(body, end))
	headings := []string{"## Goal", "## Changes", "## Decisions", "## TODO"}
	for i, heading := range headings {
		if !strings.HasPrefix(body, heading+"\n") {
			return ErrFormat
		}
		body = strings.TrimPrefix(body, heading+"\n")
		if i == len(headings)-1 {
			if strings.TrimSpace(body) == "" {
				return ErrFormat
			}
			break
		}
		content, rest, found := strings.Cut(body, "\n"+headings[i+1]+"\n")
		if !found || strings.TrimSpace(content) == "" {
			return ErrFormat
		}
		body = headings[i+1] + "\n" + rest
	}
	return nil
}

// CheckBytes validates a manually selected candidate without starting a timer.
// The caller must verify the persisted request state and source before calling.
func CheckBytes(b []byte, want Metadata) (Snapshot, error) {
	if len(b) > MaxBytes {
		return Snapshot{}, ErrSize
	}
	if err := validate(b, want); err != nil {
		return Snapshot{}, err
	}
	hash := sha256.Sum256(b)
	return Snapshot{want, string(b), hex.EncodeToString(hash[:]), time.Now()}, nil
}

// Format produces a file template for the Codex instruction and test fixtures.
// Callers must publish a completed file atomically, not stream into checkpoint.md.
func Format(meta Metadata, goal, changes, decisions, todo string) []byte {
	var b bytes.Buffer
	j, _ := json.Marshal(meta)
	b.WriteString("<!-- codex-switcher:" + string(j) + " -->\n")
	for i, content := range []string{goal, changes, decisions, todo} {
		b.WriteString([]string{"## Goal", "## Changes", "## Decisions", "## TODO"}[i] + "\n" + content + "\n")
	}
	b.WriteString("<!-- checkpoint-complete:" + meta.ID + " -->\n")
	return b.Bytes()
}

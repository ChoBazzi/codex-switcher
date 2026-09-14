package handoff

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

func setup(t *testing.T, project string) (*Store, checkpoint.Origin, string) {
	t.Helper()
	s := New()
	s.SetAccount("a", true)
	s.SetAccount("b", true)
	o := checkpoint.Origin{Project: project, Worktree: "w", Branch: "branch", Session: "old"}
	if _, err := s.Register(o, "a"); err != nil {
		t.Fatal(err)
	}
	id, err := s.Prepare("old", "b", checkpoint.Snapshot{Metadata: checkpoint.Metadata{ID: "cp", Origin: o}, Markdown: "pinned"}, true)
	if err != nil {
		t.Fatal(err)
	}
	return s, o, id
}

func TestExplicitBinding(t *testing.T) {
	s, o, id := setup(t, "p")
	o.Session = "new"
	if _, err := s.Bind(id, o, false); err != ErrInputRequired {
		t.Fatal(err)
	}
	s.SetAccount("b", false)
	if _, err := s.Bind(id, o, true); err != ErrUnavailable {
		t.Fatal(err)
	}
	s.SetAccount("b", true)
	x, err := s.Bind(id, o, true)
	if err != nil || x.Account != "b" || x.Wiki != "pinned" {
		t.Fatalf("bad bind: %v %v", x, err)
	}
	old, _ := s.Session("old")
	if old.Account != "a" {
		t.Fatal("source moved")
	}
	o.Session = "another"
	if _, err := s.Bind(id, o, true); err != ErrConflict {
		t.Fatal("reservation reused")
	}
}

func TestOrdinarySessionDoesNotSteal(t *testing.T) {
	s, o, id := setup(t, "p")
	o.Session = "ordinary"
	x, err := s.Register(o, "a")
	if err != nil || x.Wiki != "" {
		t.Fatal("ordinary session consumed Wiki")
	}
	o.Session = "handoff"
	if _, err := s.Bind(id, o, true); err != nil {
		t.Fatal(err)
	}
}

func TestProjectIsolation(t *testing.T) {
	s, o, id := setup(t, "p")
	other := o
	other.Project = "q"
	other.Session = "q-old"
	s.Register(other, "a")
	qid, err := s.Prepare(other.Session, "b", checkpoint.Snapshot{Metadata: checkpoint.Metadata{ID: "qcp", Origin: other}, Markdown: "q-wiki"}, true)
	if err != nil {
		t.Fatal(err)
	}
	other.Session = "q-new"
	if _, err := s.Bind(id, other, true); err != ErrIdentity {
		t.Fatal("cross-project accepted")
	}
	x, err := s.Bind(qid, other, true)
	if err != nil || x.Wiki != "q-wiki" {
		t.Fatal("wrong Wiki")
	}
	o.Session = "p-new"
	x, err = s.Bind(id, o, true)
	if err != nil || x.Wiki != "pinned" {
		t.Fatal("wrong Wiki")
	}
}

func TestConcurrentClaimsOneWinner(t *testing.T) {
	s, o, id := setup(t, "p")
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next := o
			next.Session = fmt.Sprint(i)
			if _, err := s.Bind(id, next, true); err == nil {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
}

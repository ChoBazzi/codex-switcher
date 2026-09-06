//go:build darwin && cgo

package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
)

const functionResponse = `{"id":"resp-tool","status":"completed","output":[{"type":"function_call","id":"fc-tool","call_id":"call-tool","name":"synthetic","arguments":"{}","status":"completed"},{"type":"message","id":"msg-tool","role":"assistant","content":[]}]}`
const functionFollowup = `{"previous_response_id":"resp-tool","input":[{"type":"item_reference","id":"msg-tool"},{"type":"function_call","id":"fc-tool","call_id":"call-tool","name":"synthetic","arguments":"{}"},{"type":"function_call_output","call_id":"call-tool","output":"synthetic result"}]}`

func TestToolOwnershipAfterRestart(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "private")
			db, err := affinity.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			for id, slot := range map[string]string{"one": "a", "same-slot": "a", "other-slot": "b"} {
				if _, err = db.Register(persistentOrigin(id), slot, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				response := functionResponse
				if n > 1 {
					response = `{"id":"resp-final","status":"completed","output":[]}`
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
				} else {
					fmt.Fprint(w, response)
				}
			}))
			defer up.Close()
			h := persistentHandler(t, db, up.URL, "one", "a")
			if _, abort := invoke(h, `{"input":"synthetic"}`); abort {
				t.Fatal("tool response rejected")
			}
			h.Close()
			db.Close()
			db, err = affinity.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			h = persistentHandler(t, db, up.URL, "one", "a")
			if status, abort := invoke(h, functionFollowup); status != 200 || abort {
				t.Fatal("owned tool continuation rejected")
			}
			for id, slot := range map[string]string{"same-slot": "a", "other-slot": "b"} {
				x := persistentHandler(t, db, up.URL, id, slot)
				if status, _ := invoke(x, `{"input":[{"type":"function_call_output","call_id":"call-tool","output":"foreign"}]}`); status != 409 {
					t.Fatal("foreign call accepted")
				}
			}
			if calls.Load() != 2 {
				t.Fatal("foreign tool output reached upstream")
			}
		})
	}
}

func TestToolReferenceKindsCannotBeConfused(t *testing.T) {
	db, err := affinity.Open(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Register(persistentOrigin("one"), "a", time.Now())
	lease, _ := db.Begin(persistentOrigin("one"), nil, time.Now())
	ids, err := completedRefs([]byte(functionResponse))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(lease, ids, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"input":[{"type":"function_call_output","call_id":"msg-tool","output":"x"}]}`,
		`{"input":[{"type":"item_reference","id":"call-tool"}]}`,
		`{"input":[{"type":"item_reference","id":"unknown"}]}`,
	} {
		refs, err := continuationRefs([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Begin(persistentOrigin("one"), refs, time.Now()); err == nil {
			t.Fatal("unverified reference accepted")
		}
	}
	for _, body := range []string{
		`{"input":"x","previous_response_id":"item:msg-tool"}`,
		`{"input":[{"type":"function_call_output","call_id":"call-tool","output":null}]}`,
		`{"input":[{"type":"function_call_output","call_id":"call-tool","output":[],"id":"arbitrary"}]}`,
		`{"input":[{"type":"function_call","call_id":"call-tool","name":"x","arguments":"{}","status":"incomplete"}]}`,
	} {
		if _, err := continuationRefs([]byte(body)); err == nil {
			t.Fatal("ambiguous shape accepted")
		}
	}
}

func TestPartialItemsDoNotGrantOwnership(t *testing.T) {
	db, err := affinity.Open(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Register(persistentOrigin("one"), "a", time.Now())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc-partial\",\"type\":\"function_call\",\"call_id\":\"call-partial\"}}\n\n")
	}))
	defer up.Close()
	h := persistentHandler(t, db, up.URL, "one", "a")
	if _, abort := invoke(h, `{"input":"x"}`); !abort {
		t.Fatal("partial stream succeeded")
	}
	if err := db.Ready(persistentOrigin("one")); err == nil {
		t.Fatal("partial session remained ready")
	}
}

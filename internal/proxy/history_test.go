//go:build darwin && cgo

package proxy

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/affinity"
)

func TestHistoryContentAndSessionOwnership(t *testing.T) {
	db, err := affinity.Open(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	for id, slot := range map[string]string{"one": "a", "two": "b"} {
		if _, err := db.Register(persistentOrigin(id), slot, now); err != nil {
			t.Fatal(err)
		}
	}
	response := []byte(`{"id":"resp-history","status":"completed","output":[{"type":"message","id":"msg-history","role":"assistant","status":"completed","content":[{"type":"output_text","text":"synthetic","annotations":[]}]},{"type":"reasoning","id":"rs-history","summary":[],"encrypted_content":"synthetic-encrypted"}]}`)
	ids, err := completedRefs(response)
	if err != nil {
		t.Fatal(err)
	}
	l, err := db.Begin(persistentOrigin("one"), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(l, ids, true, now); err != nil {
		t.Fatal(err)
	}
	body := `{"input":[{"content":[{"annotations":[],"text":"synthetic","type":"output_text"}],"role":"assistant","type":"message"},{"encrypted_content":"synthetic-encrypted","summary":[],"type":"reasoning"}]}`
	refs, err := continuationRefs([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(inlineMessageIDs([]byte(body))) != 0 {
		t.Fatal("server history became new client input")
	}
	if _, err = db.Begin(persistentOrigin("two"), refs, now); err == nil {
		t.Fatal("foreign history accepted")
	}
	changed, err := continuationRefs([]byte(strings.Replace(body, "synthetic-encrypted", "changed", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Begin(persistentOrigin("one"), changed, now); err == nil {
		t.Fatal("modified history accepted")
	}
	l, err = db.Begin(persistentOrigin("one"), refs, now)
	if err != nil {
		t.Fatal("canonical matching history rejected")
	}
	if err = db.Finish(l, nil, true, now); err != nil {
		t.Fatal(err)
	}
	// Equal content is not a globally unique resource: another session may
	// independently receive it and must acquire its own digest ownership.
	l, err = db.Begin(persistentOrigin("two"), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	other := strings.ReplaceAll(string(response), "resp-history", "resp-other")
	other = strings.ReplaceAll(other, "msg-history", "msg-other")
	other = strings.ReplaceAll(other, "rs-history", "rs-other")
	otherIDs, err := completedRefs([]byte(other))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(l, otherIDs, true, now); err != nil {
		t.Fatal("independent identical content conflicted")
	}
	if _, err = db.Begin(persistentOrigin("two"), refs, now); err != nil {
		t.Fatal("independently observed history denied")
	}
}

func TestAdditionalToolsRequireDefinitions(t *testing.T) {
	valid := `{"input":[{"type":"additional_tools","role":"developer","id":"tools-client","tools":[{"type":"function","name":"synthetic"}]}]}`
	if _, err := continuationRefs([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	if ids := inlineMessageIDs([]byte(valid)); len(ids) != 1 || ids[0] != "item:tools-client" {
		t.Fatal("definition ID not claimed")
	}
	for _, input := range []string{
		`{"type":"additional_tools","role":"developer","id":"only-id"}`,
		`{"type":"additional_tools","role":"assistant","tools":[{"name":"synthetic"}]}`,
		`{"type":"additional_tools","role":"developer","tools":[{}]}`,
		`{"type":"additional_tools","role":"developer","tools":[],"previous_response_id":"foreign"}`,
	} {
		if _, err := continuationRefs([]byte(`{"input":[` + input + `]}`)); err == nil {
			t.Fatal("invalid definitions accepted")
		}
	}
}

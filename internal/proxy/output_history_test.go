package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamOutputHistoryWithCompactCompletion(t *testing.T) {
	item := `{"id":"synthetic-item","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"synthetic","annotations":[]}]}`
	done := "data: {\"type\":\"response.output_item.done\",\"item\":" + item + "}\n\n"
	complete := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"status\":\"completed\",\"output\":[]}}\n\n"
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(item), &decoded); err != nil {
		t.Fatal(err)
	}
	key, err := historyKey(decoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []int{1, 7, 4096} {
		o := eventObserver{strict: true}
		body := done + complete
		for len(body) > 0 {
			n := min(chunk, len(body))
			if !o.feed([]byte(body[:n])) {
				t.Fatalf("stream rejected: %s", o.failureCode)
			}
			body = body[n:]
		}
		if !o.completed || len(o.responseIDs) != 3 {
			t.Fatalf("missing ownership: %d", len(o.responseIDs))
		}
		found := false
		for _, ref := range o.responseIDs {
			if ref == key {
				found = true
			}
		}
		if !found {
			t.Fatal("history lost when final output was empty")
		}
	}
	for _, suffix := range []string{"data: {\"type\":\"response.failed\"}\n\n", complete + done} {
		o := eventObserver{strict: true}
		if o.feed([]byte(done + suffix)) {
			t.Fatal("invalid terminal sequence accepted")
		}
	}
	o := eventObserver{strict: true}
	if !o.feed([]byte(done)) || o.completed || len(o.responseIDs) != 0 {
		t.Fatal("item alone authorized completion")
	}
	o = eventObserver{strict: true}
	if o.feed([]byte("data: {\"type\":\"response.output_item.done\",\"item\":\"" + strings.Repeat("x", 1<<20) + "\"}\n\n")) {
		t.Fatal("unbounded output accepted")
	}
}

package proxy

import (
	"encoding/json"
	"testing"
)

func TestHistoryEmptyAnnotations(t *testing.T) {
	key := func(raw string) string {
		t.Helper()
		var item map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &item) != nil {
			t.Fatal("invalid fixture")
		}
		key, err := historyKey(item)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	empty := key(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic","annotations":[]}]}`)
	omitted := key(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic"}]}`)
	nonempty := key(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic","annotations":[{"type":"synthetic"}]}]}`)
	changed := key(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"changed"}]}`)
	emptyLogprobs := key(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic","annotations":[],"logprobs":[]}]}`)
	nonemptyLogprobs := key(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic","logprobs":[{"token":"synthetic","logprob":-1}]}]}`)
	nullLogprobs := key(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic","logprobs":null}]}`)
	if emptyLogprobs != omitted || nonemptyLogprobs == omitted || nullLogprobs == omitted {
		t.Fatal("logprobs normalization broadened")
	}
	if empty != omitted || nonempty == omitted || changed == omitted {
		t.Fatal("history normalization broadened")
	}
}

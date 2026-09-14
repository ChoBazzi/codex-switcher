package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

const probeToolHistory = `{"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],"input":[{"role":"user","content":"read"},{"type":"reasoning","id":"rs_old","summary":[],"encrypted_content":"old-opaque"},{"type":"function_call","id":"fc_old","call_id":"call_old","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_old","output":"blue apple"},{"role":"assistant","phase":"final_answer","content":"done"},{"role":"user","content":"continue"}]}`

func TestProbeToolsPortableHistory(t *testing.T) {
	out, err := probeToolBody([]byte(probeToolHistory), "b", "a", "synthetic-salt")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"old-opaque", "rs_old", "fc_old", "call_old"} {
		if strings.Contains(string(out), secret) {
			t.Fatal("server reference escaped")
		}
	}
	if !strings.Contains(string(out), "blue apple") || !strings.Contains(string(out), `"tools":[{`) {
		t.Fatal("history or declarations lost")
	}
	var p struct{ Input []map[string]json.RawMessage }
	_ = json.Unmarshal(out, &p)
	if probeString(p.Input[1], "call_id") == "" || probeString(p.Input[1], "call_id") != probeString(p.Input[2], "call_id") {
		t.Fatal("pair disconnected")
	}
	// Stable per slot/run, but never reuse A's portable ids in B.
	if string(probePortableCall("salt", "a", "x")) == string(probePortableCall("salt", "b", "x")) {
		t.Fatal("slots share ids")
	}
}

func TestProbeToolsCurrentTurn(t *testing.T) {
	body := `{"input":[{"role":"user","content":"run"},{"type":"reasoning","id":"rs_current","summary":[],"encrypted_content":"opaque-current"},{"type":"custom_tool_call","id":"ctc_current","call_id":"call_current","name":"apply_patch","input":"synthetic patch"},{"type":"custom_tool_call_output","call_id":"call_current","output":[{"type":"input_text","text":"done"}]}]}`
	out, err := probeToolBody([]byte(body), "a", "a", "salt")
	if err != nil || !strings.Contains(string(out), "opaque-current") || !strings.Contains(string(out), "call_current") {
		t.Fatal("current reasoning/pair changed")
	}
	if _, err := probeToolBody([]byte(body), "b", "a", "salt"); err == nil {
		t.Fatal("current turn crossed accounts")
	}
}

func TestProbeToolsRejectUnsafeHistory(t *testing.T) {
	for _, item := range []string{
		`{"type":"function_call_output","call_id":"private","output":"x"}`,
		`{"type":"function_call","call_id":"private","name":"read","arguments":"{}"}`,
		`{"type":"item_reference","id":"private"}`,
		`{"type":"compaction","encrypted_content":"private"}`,
		`{"type":"custom_tool_call","call_id":"private","name":"patch","input":null}`,
		`{"type":"reasoning","summary":[],"private":"private"}`,
	} {
		_, err := probeToolBody([]byte(`{"input":[{"role":"user","content":"x"},`+item+`]}`), "a", "a", "salt")
		var detail *probeBodyError
		if !errors.As(err, &detail) {
			t.Fatal("unsafe history accepted")
		}
		encoded, _ := json.Marshal(detail)
		if strings.Contains(string(encoded), "private") {
			t.Fatal("diagnostic leaked")
		}
	}
	for _, body := range []string{
		`{"previous_response_id":"private","input":[]}`,
		`{"input":[{"role":"user","content":"x"}],"tools":[{"type":"web_search"}]}`,
		`{"input":[{"role":"assistant","content":[{"type":"output_text","text":"x","annotations":[{"file_id":"private"}]}]},{"role":"user","content":"next"}]}`,
		`{"input":[{"role":"user","content":null}]}`,
		`{"input":[{"role":"user","content":"x"},{"type":"function_call","call_id":"c","name":"read","arguments":"{}"},{"type":"custom_tool_call_output","call_id":"c","output":"x"}]}`,
		`{"input":[{"role":"user","content":"x"},{"type":"function_call","call_id":"c","name":"read","arguments":"{}"},{"role":"user","content":"next"},{"type":"function_call_output","call_id":"c","output":"x"}]}`,
	} {
		if _, err := probeToolBody([]byte(body), "a", "a", "salt"); err == nil {
			t.Fatal("unsafe shape accepted")
		}
	}
}

func TestProbeTurnBoundary(t *testing.T) {
	for _, kind := range []string{"function_call", "custom_tool_call", "message", "compaction"} {
		recorder := httptest.NewRecorder()
		w := &probeTurnWriter{ResponseWriter: recorder}
		s := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"" + kind + "\",\"role\":\"assistant\",\"phase\":\"final_answer\"}}\r\n\r\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
		for _, b := range []byte(s) {
			_, _ = w.Write([]byte{b})
		}
		if recorder.Body.String() != s {
			t.Fatal("stream modified")
		}
		if kind == "compaction" {
			if w.valid() {
				t.Fatal("unknown output accepted")
			}
			continue
		}
		if !w.valid() || w.pending() != (kind != "message") {
			t.Fatal("wrong turn boundary")
		}
	}
	w := &probeTurnWriter{ResponseWriter: httptest.NewRecorder()}
	w.feed([]byte(strings.Repeat("x", (1<<20)+1)))
	if !w.invalid || w.valid() {
		t.Fatal("unbounded frame")
	}
}

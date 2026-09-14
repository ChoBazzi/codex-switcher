package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProbeBodyDiagnosticPrivacy(t *testing.T) {
	for _, tc := range []struct {
		body, reason string
		item, part   int
	}{
		{`{"input":[{"role":"user","content":"synthetic-secret","phase":"synthetic-secret"}]}`, "message_phase_invalid", 0, -1},
		{`{"input":[{"role":"user","content":"x","synthetic-secret":"x"}]}`, "message_field_unknown", 0, -1},
		{`{"input":[{"type":"reasoning","encrypted_content":"synthetic-secret"}]}`, "item_type_reasoning", 0, -1},
		{`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"synthetic-secret"}]}]}`, "content_type_input_image", 0, 0},
		{`{"input":[],"previous_response_id":"synthetic-secret"}`, "server_reference_previous_response_id", -1, -1},
		{`{"input":[{"type":"synthetic-secret"}]}`, "item_type_unknown", 0, -1},
	} {
		_, err := probeTextBody([]byte(tc.body))
		var detail *probeBodyError
		if !errors.As(err, &detail) || detail.Reason != tc.reason || detail.Item != tc.item || detail.Part != tc.part {
			t.Fatal("incorrect diagnostic category")
		}
		encoded, _ := json.Marshal(detail)
		if strings.Contains(string(encoded)+err.Error(), "synthetic-secret") {
			t.Fatal("private value leaked")
		}
	}
}

func TestProbeAdmissionCode(t *testing.T) {
	for _, tc := range []struct {
		err               error
		busy, failed      bool
		session, id, want string
	}{
		{errors.New("synthetic"), false, false, "", "", "probe_identity_invalid"},
		{nil, true, false, "one", "one", "probe_request_in_progress"},
		{nil, false, true, "one", "one", "probe_previous_request_failed"},
		{nil, false, false, "one", "two", "probe_conversation_changed"},
		{nil, false, false, "", "one", ""},
		{nil, false, false, "one", "one", ""},
	} {
		if got := probeAdmissionCode(tc.err, tc.busy, tc.failed, tc.session, tc.id); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}

func TestProbeTextBody(t *testing.T) {
	out, err := probeTextBody([]byte(`{"input":[{"type":"message","role":"assistant","id":"msg_old","content":[{"type":"output_text","text":"blue apple"}]}],"tools":[{"type":"function"}]}`))
	if err != nil || strings.Contains(string(out), "msg_old") || !strings.Contains(string(out), "blue apple") || !strings.Contains(string(out), `"tools":[]`) {
		t.Fatal("text-only normalization failed")
	}
	for _, body := range []string{
		`{"input":[],"previous_response_id":"resp_old"}`,
		`{"input":[],"conversation":"conv_old"}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`,
		`{"input":[{"type":"item_reference","id":"old"}]}`,
		`{"input":[{"type":"function_call_output","call_id":"old","output":"x"}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`,
	} {
		if _, err := probeTextBody([]byte(body)); err == nil {
			t.Fatal("opaque state accepted")
		}
	}
}

func TestProbeMessagePhase(t *testing.T) {
	for _, phase := range []string{"commentary", "final_answer"} {
		body := `{"input":[{"role":"assistant","phase":"` + phase + `","content":"synthetic answer"}]}`
		out, err := probeTextBody([]byte(body))
		if err != nil || !strings.Contains(string(out), `"phase":"`+phase+`"`) {
			t.Fatal("phase not preserved")
		}
	}
	for _, value := range []string{`null`, `1`, `"unknown"`, `""`, `{}`} {
		if _, err := probeTextBody([]byte(`{"input":[{"role":"assistant","phase":` + value + `,"content":"x"}]}`)); err == nil {
			t.Fatal("invalid phase accepted")
		}
	}
}

func TestProbeInlineToolDeclarations(t *testing.T) {
	body := `{"input":[{"type":"additional_tools","role":"developer","id":"synthetic-definition","tools":[{"type":"function","name":"synthetic_tool","parameters":{"type":"object"}}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic blue apple"}]}]}`
	out, err := probeTextBody([]byte(body))
	if err != nil || strings.Contains(string(out), "synthetic_tool") || strings.Contains(string(out), "synthetic-definition") || !strings.Contains(string(out), "synthetic blue apple") {
		t.Fatal("inline declaration normalization failed")
	}
	for _, body := range []string{
		`{"input":[{"type":"additional_tools","tools":null}]}`,
		`{"input":[{"type":"additional_tools","tools":[],"previous_response_id":"synthetic"}]}`,
		`{"input":[{"type":"additional_tools","tools":"synthetic"}]}`,
	} {
		if _, err := probeTextBody([]byte(body)); err == nil {
			t.Fatal("malformed declaration accepted")
		}
	}
}

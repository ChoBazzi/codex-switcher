package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProbePortableAgentDiagnostic(t *testing.T) {
	for _, scope := range []string{"root", "auxiliary"} {
		raw, _ := json.Marshal(map[string]any{"event": "probe_blocked", "scope": scope, "detail": map[string]string{"reason": "agent_message_portable_owner_unavailable"}, "body": "synthetic-secret"})
		d := sanitizedProbeDiagnostic(raw)
		if d == nil || d.Code != "agent_message_portable_owner_unavailable" || d.Scope != scope {
			t.Fatal("portable rejection lost")
		}
		raw, _ = json.Marshal(d)
		var forwarded map[string]any
		_ = json.Unmarshal(raw, &forwarded)
		forwarded["event"] = "probe_diagnostic"
		forwarded["secret"] = "synthetic-secret"
		raw, _ = json.Marshal(forwarded)
		next := sanitizedProbeDiagnostic(raw)
		if next == nil || next.Code != d.Code || next.Scope != scope {
			t.Fatal("relay lost portable rejection")
		}
		raw, _ = json.Marshal(next)
		if strings.Contains(string(raw), "synthetic-secret") {
			t.Fatal("diagnostic leaked private value")
		}
	}
}

func TestProbeDiagnosticRedaction(t *testing.T) {
	for _, tc := range []struct{ raw, code, scope string }{
		{`{"event":"probe_request_finished","last_http_status":409,"local_rejection_code":"history_owner_unavailable"}`, "history_owner_unavailable", "root"},
		{`{"event":"probe_blocked","code":"probe_tool_history_unsupported","detail":{"reason":"compaction_owner_unavailable","item_index":99}}`, "compaction_owner_unavailable", "root"},
		{`{"event":"probe_blocked","code":"probe_tool_history_unsupported","detail":{"reason":"agent_message_shape_unsupported"}}`, "agent_message_shape_unsupported", "root"},
		{`{"event":"probe_auxiliary_finished","last_http_status":409,"local_rejection_code":"auxiliary_credential_changed"}`, "auxiliary_credential_changed", "auxiliary"},
		{`{"event":"probe_auxiliary_finished","last_http_status":409,"local_rejection_detail":"agent_message_owner_unavailable"}`, "agent_message_owner_unavailable", "auxiliary"},
		{`{"event":"probe_auxiliary_finished","last_http_status":409,"code":"probe_auxiliary_restart_required"}`, "auxiliary_restart_required", "auxiliary"},
		{`{"event":"probe_request_finished","last_http_status":401,"local_rejection_code":"authentication_expired"}`, "authentication_expired", "root"},
		{`{"event":"probe_request_finished","last_http_status":503,"local_rejection_code":"credential_store_unavailable"}`, "credential_store_unavailable", "root"},
		{`{"event":"probe_auxiliary_finished","last_http_status":408,"code":"request_body_timeout"}`, "request_body_timeout", "auxiliary"},
		{`{"event":"probe_request_finished","last_http_status":408,"local_rejection_code":"request_canceled"}`, "request_canceled", "root"},
		{`{"event":"probe_auxiliary_finished","last_http_status":408,"local_rejection_code":"request_canceled"}`, "request_canceled", "auxiliary"},
		{`{"event":"probe_blocked","scope":"auxiliary","code":"auxiliary_capacity_reached"}`, "auxiliary_capacity_reached", "auxiliary"},
		{`{"event":"probe_request_finished","last_http_status":429}`, "upstream_rate_limited", "root"},
		{`{"event":"probe_request_finished","last_http_status":403}`, "upstream_auth_rejected", "root"},
		{`{"event":"probe_blocked","scope":"synthetic-secret","code":"synthetic-secret","detail":{"reason":"synthetic-secret"}}`, "request_rejected", "root"},
		{`{"event":"probe_request_finished","last_http_status":200,"response_failure_code":"synthetic-secret"}`, "response_interrupted", "root"},
	} {
		var event map[string]any
		if json.Unmarshal([]byte(tc.raw), &event) != nil {
			t.Fatal("invalid fixture")
		}
		event["authorization"] = "synthetic-secret"
		event["account_id"] = "synthetic-secret"
		event["body"] = "synthetic-secret"
		event["codex_home"] = "/synthetic-secret"
		raw, _ := json.Marshal(event)
		d := sanitizedProbeDiagnostic(raw)
		if d == nil || d.Code != tc.code || d.Scope != tc.scope {
			t.Fatalf("wrong diagnostic: %+v", d)
		}
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), "synthetic-secret") || strings.Contains(string(b), "item_index") {
			t.Fatal("raw content escaped")
		}
		if _, err := time.Parse(time.RFC3339, d.At); err != nil {
			t.Fatal("invalid local timestamp")
		}
	}
	for _, raw := range []string{`{"event":"probe_request_finished","last_http_status":200}`, `{"event":"usage_snapshot","code":"history_owner_unavailable"}`, `{"event":"probe_ready","code":"history_owner_unavailable"}`} {
		if sanitizedProbeDiagnostic([]byte(raw)) != nil {
			t.Fatal("nonfailure became diagnostic")
		}
	}
}

func TestServiceDiagnosticReconnectAndOriginalCause(t *testing.T) {
	input, commands := io.Pipe()
	defer input.Close()
	defer commands.Close()
	b := &serviceBroker{cache: map[string][]byte{}, input: commands}
	write := func(s string) {
		if _, err := b.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"event":"probe_request_finished","last_http_status":409,"local_rejection_code":"history_owner_unavailable"}`)
	write(`{"event":"probe_blocked","code":"probe_previous_request_failed"}`)
	write(`{"event":"probe_blocked","code":"probe_recovery_requires_new_input"}`)
	write(`{"event":"probe_auxiliary_finished","last_http_status":429}`)
	write(`{"event":"probe_request_finished","last_http_status":200}`)
	for range 2 {
		server, client := net.Pipe()
		done := make(chan struct{})
		go func() { b.attach(server); close(done) }()
		reader := bufio.NewReader(client)
		root := brokerEvent(t, reader, client)
		aux := brokerEvent(t, reader, client)
		if root["code"] != "history_owner_unavailable" || root["scope"] != "root" || aux["code"] != "upstream_rate_limited" || aux["scope"] != "auxiliary" {
			t.Fatal("diagnostic cause or scope lost on reconnect")
		}
		client.Close()
		<-done
	}
	if len(b.cache) != 2 || len(b.diagnostics) != 2 {
		t.Fatal("diagnostic storage not bounded by scope")
	}
}

func TestServiceAuthenticationCacheIsTransient(t *testing.T) {
	b := &serviceBroker{cache: map[string][]byte{}}
	_, err := b.Write([]byte(`{"event":"probe_state","busy":true,"authentication":[{"slot":"a","waiting":1,"refreshing":true,"canceled":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var cached map[string]json.RawMessage
	if json.Unmarshal(b.cache["probe_state"], &cached) != nil || cached["authentication"] != nil || string(cached["busy"]) != "true" {
		t.Fatal("cached state retained transient authentication or lost busy guard")
	}
}

func TestAuxiliaryDiagnosticFromParser(t *testing.T) {
	var events [][]byte
	a := &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: "synthetic-child", Slot: "a"}, report: func(e any) { b, _ := json.Marshal(e); events = append(events, b) }}
	var mu sync.Mutex
	body := `{"input":[{"role":"user","content":"synthetic request"},{"type":"agent_message","content":"synthetic-secret"}]}`
	w := httptest.NewRecorder()
	a.serve(w, httptest.NewRequest("POST", "/responses", strings.NewReader(body)), &mu, syntheticProbeAccess{}, "http://127.0.0.1:1", "synthetic-salt", func() bool { return true }, nil)
	if w.Code != 409 || len(events) != 1 {
		t.Fatal("missing auxiliary rejection")
	}
	d := sanitizedProbeDiagnostic(events[0])
	if d == nil || d.Code != "agent_message_shape_unsupported" || d.Scope != "auxiliary" || strings.Contains(string(events[0]), "synthetic-secret") {
		t.Fatal("auxiliary parser cause lost or exposed")
	}
}

func TestProcessBuildIdentity(t *testing.T) {
	bytes, err := hex.DecodeString(processBuildID)
	if err != nil || len(bytes) != 32 || executableBuildID() != processBuildID {
		t.Fatal("process build fingerprint unavailable or unstable")
	}
}

func TestRelayBuildPrecedesLegacyReady(t *testing.T) {
	server, client := net.Pipe()
	input, commands := io.Pipe()
	defer input.Close()
	defer commands.Close()
	output, sink := io.Pipe()
	defer output.Close()
	done := make(chan error, 1)
	go func() { defer sink.Close(); done <- relayProxy(client, input, sink) }()
	go func() {
		defer server.Close()
		io.WriteString(server, "{\"event\":\"probe_ready\",\"codex_home\":\"/synthetic-home\"}\n")
	}()
	reader := bufio.NewReader(output)
	for i := 0; i < 2; i++ {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if json.Unmarshal(line, &event) != nil {
			t.Fatal("invalid relay JSON")
		}
		if i == 0 && (event["event"] != "helper_build" || event["build_id"] != processBuildID || event["protocol_version"] != float64(controlProtocolVersion)) {
			t.Fatal("relay fingerprint missing")
		}
		if i == 1 && (event["event"] != "probe_ready" || event["build_id"] != nil) {
			t.Fatal("legacy daemon build was guessed")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceForwardedQuotaDiagnostic(t *testing.T) {
	b := &serviceBroker{cache: map[string][]byte{}}
	b.Write([]byte(`{"event":"probe_diagnostic","scope":"root","code":"usage_limit_reached","at":"2026-09-22T00:00:00Z","secret":"synthetic-secret"}`))
	if !strings.Contains(string(b.cache["diagnostic_root"]), "usage_limit_reached") || strings.Contains(string(b.cache["diagnostic_root"]), "synthetic-secret") {
		t.Fatal("coordinator diagnostic lost or leaked")
	}
	for _, bad := range []string{
		`{"event":"probe_diagnostic","scope":"root","code":"synthetic-secret","at":"2026-09-22T00:00:00Z"}`,
		`{"event":"probe_diagnostic","scope":"synthetic-secret","code":"usage_limit_reached","at":"2026-09-22T00:00:00Z"}`,
		`{"event":"probe_diagnostic","scope":"root","code":"usage_limit_reached","at":"synthetic-secret"}`,
	} {
		if sanitizedProbeDiagnostic([]byte(bad)) != nil {
			t.Fatal("invalid summary accepted")
		}
	}
}

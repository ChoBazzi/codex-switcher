package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"github.com/ChoBazzi/codex-switcher/internal/credentialstore"
	"github.com/ChoBazzi/codex-switcher/internal/livetest"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
	"github.com/ChoBazzi/codex-switcher/internal/usage"
)

// Plain-text-only experiment: never transfer opaque server continuation state.
// Message text is unchanged; server message ids are removed on EVERY request.
type probeBodyError struct {
	Reason string `json:"reason"`
	Item   int    `json:"item_index"`
	Part   int    `json:"part_index"`
}

func (e *probeBodyError) Error() string { return "probe_requires_plain_text_history" }

// Only fixed categories escape the parser; never echo arbitrary field/type names.
func probeCategory(value string) string {
	switch value {
	case "additional_tools", "reasoning", "item_reference", "function_call", "function_call_output", "input_image", "input_file", "phase", "channel", "encrypted_content":
		return value
	default:
		return "unknown"
	}
}

func probeTextBody(body []byte) ([]byte, error) {
	bad := func(reason string, item, part int) error { return &probeBodyError{reason, item, part} }
	var p map[string]json.RawMessage
	if json.Unmarshal(body, &p) != nil || p == nil {
		return nil, bad("invalid_json_object", -1, -1)
	}
	for _, key := range []string{"previous_response_id", "conversation"} {
		if v, ok := p[key]; ok && string(v) != "null" && string(v) != `""` {
			return nil, bad("server_reference_"+key, -1, -1)
		}
		delete(p, key)
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(p["input"], &items) != nil {
		return nil, bad("input_not_message_array", -1, -1)
	}
	textItems := make([]map[string]json.RawMessage, 0, len(items))
	for i, item := range items {
		var kind, role string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["role"], &role)
		if kind == "additional_tools" {
			// Installed CLI sends tool declarations inside input, not only at
			// the top level. This experiment disables tools at both locations.
			for key := range item {
				if key != "type" && key != "role" && key != "id" && key != "tools" {
					return nil, bad("additional_tools_shape_invalid", i, -1)
				}
			}
			var definitions []map[string]json.RawMessage
			if json.Unmarshal(item["tools"], &definitions) != nil || definitions == nil {
				return nil, bad("additional_tools_shape_invalid", i, -1)
			}
			continue
		}
		if kind != "" && kind != "message" {
			return nil, bad("item_type_"+probeCategory(kind), i, -1)
		}
		if role != "user" && role != "assistant" && role != "system" && role != "developer" {
			return nil, bad("unsupported_role", i, -1)
		}
		if raw, exists := item["phase"]; exists {
			var phase string
			if role != "assistant" || json.Unmarshal(raw, &phase) != nil || phase != "commentary" && phase != "final_answer" {
				return nil, bad("message_phase_invalid", i, -1)
			}
		}
		for key := range item {
			if key != "type" && key != "role" && key != "content" && key != "id" && key != "status" && key != "phase" {
				return nil, bad("message_field_"+probeCategory(key), i, -1)
			}
		}
		var parts []struct {
			Type string `json:"type"`
		}
		var plain string
		if json.Unmarshal(item["content"], &plain) != nil {
			if json.Unmarshal(item["content"], &parts) != nil {
				return nil, bad("invalid_content_shape", i, -1)
			}
			for j, part := range parts {
				if part.Type != "input_text" && part.Type != "output_text" {
					return nil, bad("content_type_"+probeCategory(part.Type), i, j)
				}
			}
		}
		delete(item, "id")
		textItems = append(textItems, item)
	}
	p["input"], _ = json.Marshal(textItems)
	// No tools may run during this narrow compatibility experiment.
	p["tools"] = json.RawMessage(`[]`)
	delete(p, "additional_tools")
	p["tool_choice"] = json.RawMessage(`"none"`)
	return json.Marshal(p)
}

func probeAdmissionCode(identityErr error, busy, failed bool, session, id string) string {
	switch {
	case identityErr != nil:
		return "probe_identity_invalid"
	case busy:
		return "probe_request_in_progress"
	case failed:
		return "probe_previous_request_failed"
	case session != "" && session != id:
		return "probe_conversation_changed"
	default:
		return ""
	}
}

func switchProbe(args []string, input io.Reader, output io.Writer) error {
	parent, err := os.UserConfigDir()
	if err != nil {
		return errors.New("private_state_directory_unavailable")
	}
	access := accounts.NewRefreshing(credentialstore.New(), filepath.Join(parent, "com.bazzi.codex-switcher"), accounts.NewOAuthRefresher())
	return switchProbeWithAccess(args, input, output, access, livetest.Upstream)
}

type probeAccess interface {
	Access(string, time.Time) (accounts.Access, error)
}

func probeSelectionAllowed(target string, expected, revision uint64, busy, failed bool) bool {
	return (accountslot.Valid(target)) && expected == revision && !busy && !failed
}

func switchProbeWithAccess(args []string, input io.Reader, output io.Writer, access probeAccess, upstream string) error {
	return switchProbeWithUsage(args, input, output, access, upstream, nil)
}

func switchProbeWithUsage(args []string, input io.Reader, output io.Writer, access probeAccess, upstream string, fetcher usage.Fetcher) error {
	return switchProbeWithUsageTiming(args, input, output, access, upstream, fetcher, usage.Interval, 5*time.Second)
}

// Timing overrides are for synthetic tests only, never CLI flags.
func switchProbeWithUsageTiming(args []string, input io.Reader, output io.Writer, access probeAccess, upstream string, fetcher usage.Fetcher, interval, cooldown time.Duration) error {
	f := flag.NewFlagSet("switch-probe", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	confirm := f.Bool("allow-live", false, "explicit live-account experiment")
	managed := f.Bool("managed", false, "private JSON stdin control for owning app")
	automatic := f.Bool("auto", false, "select using fresh quota at request boundaries")
	toolsMode := f.Bool("tools", false, "experimental local function/custom tool history")
	if f.Parse(args) != nil || f.NArg() != 0 || !*confirm {
		return errors.New("usage: switch-probe --allow-live")
	}
	if !*automatic {
		for _, slot := range []string{"a", "b"} {
			if _, err := access.Access(slot, time.Now()); err != nil {
				return err
			}
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	secret := newDirectSecret()
	var mu sync.Mutex
	slot, session := "a", ""
	busy, failed := false, false
	turnPending, previousSlot := false, ""
	var requestDone chan struct{}
	terminalReady, waiting := false, false
	var lastBodyHash [32]byte
	var lastUserBoundary [32]byte
	recoveryRequired := false
	compactOwners := probeCompactRegistry{}
	var activeCompactItems []map[string]json.RawMessage
	var activeCredential [32]byte
	opaqueSlot := ""
	chosen := false
	samples := make([]usage.Snapshot, accountslot.Capacity)
	for i, slot := range accountslot.All() {
		samples[i] = usage.Snapshot{Slot: slot, State: "unknown", Stale: true}
	}
	var usageEpoch [accountslot.Capacity]uint64
	var accountChanging [accountslot.Capacity]bool
	usageRunning := *automatic
	var usageRequestID uint64
	var usageFinished time.Time
	usageWake := make(chan struct{}, 1)
	var turnLease io.Closer
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if turnLease != nil {
			turnLease.Close()
		}
	}()
	var revision uint64
	var outMu sync.Mutex
	report := func(v any) { outMu.Lock(); defer outMu.Unlock(); _ = json.NewEncoder(output).Encode(v) }
	refreshEvent := func(id uint64, status string, succeeded bool) {
		report(map[string]any{"event": "usage_refresh", "request_id": id, "status": status, "succeeded": succeeded})
	}
	publishUsage := func() {
		aged := make([]usage.Snapshot, len(samples))
		for i, s := range samples {
			aged[i] = s.At(time.Now())
		}
		report(map[string]any{"event": "usage_snapshot", "interval_seconds": 60, "accounts": aged})
	}
	if *automatic {
		if fetcher == nil {
			client := usage.NewClient()
			defer client.Close()
			fetcher = client
		}
		monitor := usage.NewMonitor(access, fetcher)
		done := make(chan struct{})
		defer func() { cancel(); <-done }()
		go func() {
			defer close(done)
			var observed [accountslot.Capacity]uint64
			for {
				if ctx.Err() != nil {
					return
				}
				mu.Lock()
				epoch := usageEpoch
				mu.Unlock()
				if epoch != observed {
					monitor = usage.NewMonitor(access, fetcher)
					observed = epoch
				}
				next := monitor.Refresh(ctx, accountslot.All())
				mu.Lock()
				succeeded := true
				for i, s := range next {
					if epoch[i] == usageEpoch[i] && !accountChanging[i] {
						samples[i] = s
					} else {
						succeeded = false
					}
					if s.State != "ok" && s.State != "limit_reached" && s.State != "not_registered" {
						succeeded = false
					}
				}
				publishUsage()
				if usageRequestID != 0 {
					refreshEvent(usageRequestID, "finished", succeeded)
					usageRequestID = 0
				}
				usageRunning = false
				usageFinished = time.Now()
				timer := time.NewTimer(interval)
				mu.Unlock()
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				case <-usageWake:
				}
				timer.Stop()
				mu.Lock()
				// A manual request racing the timer belongs to this same round.
				select {
				case <-usageWake:
				default:
				}
				usageRunning = true
				mu.Unlock()
			}
		}()
	}
	// Caller holds mu. A single ordered pipe carries observations and acknowledgments.
	state := func(event string, accepted bool) {
		if !busy && !turnPending && turnLease != nil {
			turnLease.Close()
			turnLease = nil
		}
		report(map[string]any{"event": event, "accepted": accepted, "slot": slot, "busy": busy || turnPending, "failed": failed, "connected": session != "", "revision": revision, "completion_pending": terminalReady,
			"can_abandon_turn": probeAbandonAllowed(revision, revision, busy, waiting, turnPending, failed)})
	}
	resolve := func(r *http.Request) (proxy.Identity, error) {
		mu.Lock()
		defer mu.Unlock()
		c, err := usage.RequestAccess(access, slot, time.Now())
		activeCredential = sha256.Sum256([]byte(c.Token))
		for _, item := range activeCompactItems {
			if !compactOwners.permits(item, slot, activeCredential) {
				return proxy.Identity{}, errors.New("compaction_owner_unavailable")
			}
		}
		return proxy.Identity{Session: session, Token: c.Token, AccountID: c.AccountID}, err
	}
	validateCompact := func(body []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if err := compactOwners.accept(body, slot, activeCredential); err != nil {
			return err
		}
		opaqueSlot = slot
		return nil
	}
	h, err := proxy.New(upstream, resolve)
	if err != nil {
		return err
	}
	if *toolsMode {
		h.ValidateCompaction = validateCompact
	}
	defer func() { mu.Lock(); defer mu.Unlock(); h.Close() }()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("probe_listen_failed")
	}
	defer listener.Close()
	home, err := os.MkdirTemp("", "switcher-probe-")
	if err != nil {
		return errors.New("probe_home_failed")
	}
	// Keep local CLI history for user inspection; never recursively delete it.
	profile := fmt.Sprintf(`model_provider = "switch_probe"
approval_policy = "never"
sandbox_mode = "read-only"
web_search = "disabled"
check_for_update_on_startup = false
[model_providers.switch_probe]
name = "Experimental account switch"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
[model_providers.switch_probe.http_headers]
X-Switcher-Run = %q
`, "http://"+listener.Addr().String(), secret)
	path := filepath.Join(home, "config.toml")
	if os.WriteFile(path, []byte(profile), 0600) != nil {
		return errors.New("probe_profile_failed")
	}
	defer func() {
		actual, err := os.ReadFile(path)
		if err == nil && bytes.Equal(actual, []byte(profile)) {
			_ = os.Remove(path)
		}
	}()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("X-Switcher-Run")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Switcher-Run")), []byte(secret)) != 1 {
			http.Error(w, "probe_unauthorized", 401)
			return
		}
		compact := *toolsMode && r.URL.Path == "/responses/compact"
		if r.Method != http.MethodPost || r.URL.RawQuery != "" || (r.URL.Path != "/responses" && !compact) {
			http.Error(w, "probe_unsupported_route", 404)
			return
		}
		id, err := cliidentity.ThreadID(r.Header)
		mu.Lock()
		waited := false
		if *toolsMode && err == nil && id == session && busy && terminalReady && !waiting {
			waiting = true
			done := requestDone
			mu.Unlock()
			report(map[string]any{"event": "probe_request_waiting"})
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-done:
				waited = true
			case <-r.Context().Done():
				timer.Stop()
				mu.Lock()
				waiting = false
				mu.Unlock()
				return
			case <-timer.C:
				mu.Lock()
				waiting = false
				mu.Unlock()
				http.Error(w, "probe_request_wait_timeout", 409)
				return
			}
			timer.Stop()
			mu.Lock()
			waiting = false
		}
		if r.Context().Err() != nil {
			mu.Unlock()
			return
		}
		if code := probeAdmissionCode(err, busy, failed, session, id); code != "" {
			mu.Unlock()
			http.Error(w, code, 409)
			report(map[string]any{"event": "probe_blocked", "code": code,
				"thread_header_count":    len(r.Header.Values("Thread-Id")),
				"session_header_count":   len(r.Header.Values("Session-Id")),
				"identity_headers_equal": r.Header.Get("Thread-Id") == r.Header.Get("Session-Id")})
			return
		}
		if compact && turnPending {
			mu.Unlock()
			http.Error(w, "probe_compaction_turn_pending", 409)
			return
		}
		if *automatic && !turnPending {
			current := ""
			if chosen {
				current = slot
			}
			next, reason := probeQuotaSelection(current, samples, time.Now())
			if next != "" && opaqueSlot != "" && next != opaqueSlot {
				next, reason = "", "probe_compaction_account_pinned"
			}
			if next == "" {
				mu.Unlock()
				http.Error(w, reason, http.StatusConflict)
				report(map[string]any{"event": "probe_blocked", "code": reason})
				return
			}
			slot, chosen = next, true
		}
		if turnLease == nil {
			if owner, ok := access.(interface{ BeginTurn() (io.Closer, error) }); ok {
				lease, leaseErr := owner.BeginTurn()
				if leaseErr != nil {
					mu.Unlock()
					http.Error(w, "account_operation_busy", 409)
					return
				}
				turnLease = lease
			}
		}
		busy, session = true, id
		activeCompactItems = nil
		activeCredential = [32]byte{}
		requestDone = make(chan struct{})
		finished := requestDone
		terminalReady = false
		revision++
		selected := slot
		owner := previousSlot
		state("probe_state", false)
		mu.Unlock()
		completed := false
		attempted := false
		diagnostic := proxy.Diagnostics{ResponseFailure: "probe_request_interrupted"}
		defer func() {
			if !completed && attempted && diagnostic.ResponseFailure == "probe_request_interrupted" {
				diagnostic = h.Diagnostics()
				if diagnostic.ResponseFailure == "" {
					diagnostic.ResponseFailure = "probe_request_interrupted"
				}
				if r.Context().Err() != nil {
					diagnostic.ResponseFailure = "probe_client_canceled"
				}
			}
			report(struct {
				Event string `json:"event"`
				Slot  string `json:"slot"`
				proxy.Diagnostics
			}{"probe_request_finished", selected, diagnostic})
			mu.Lock()
			if !completed {
				failed = true
				turnPending = false
			}
			busy = false
			activeCompactItems = nil
			activeCredential = [32]byte{}
			terminalReady = false
			revision++
			state("probe_state", false)
			close(finished)
			mu.Unlock()
		}()
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		r.Body.Close()
		if err == nil {
			hash := sha256.Sum256(body)
			boundary, hasUser := probeUserBoundary(body)
			mu.Lock()
			needsInput := recoveryRequired && (!hasUser || boundary == lastUserBoundary)
			duplicate := waited && hash == lastBodyHash
			if !duplicate && !needsInput {
				lastBodyHash = hash
				lastUserBoundary = boundary
			}
			mu.Unlock()
			if needsInput {
				completed = true
				diagnostic = proxy.Diagnostics{Status: 409, Rejection: "probe_recovery_requires_new_input"}
				http.Error(w, "probe_recovery_requires_new_input", 409)
				return
			}
			if duplicate {
				completed = true
				diagnostic = proxy.Diagnostics{Status: 409, Rejection: "probe_duplicate_followup"}
				http.Error(w, "probe_duplicate_followup", 409)
				return
			}
		}
		if err == nil {
			if *toolsMode {
				items := probeCompactItems(body)
				var credential [32]byte
				if len(items) > 0 {
					c, accessErr := access.Access(selected, time.Now())
					if accessErr == nil {
						credential = sha256.Sum256([]byte(c.Token))
					}
				}
				body, err = probeToolBodyWithCompaction(body, selected, owner, secret, func(item map[string]json.RawMessage) bool {
					mu.Lock()
					defer mu.Unlock()
					return compactOwners.permits(item, selected, credential)
				})
				if err == nil {
					mu.Lock()
					activeCompactItems = items
					if len(items) == 0 {
						opaqueSlot = ""
					}
					mu.Unlock()
				}
			} else {
				body, err = probeTextBody(body)
			}
		}
		if err != nil {
			mu.Lock()
			failed = true
			mu.Unlock()
			code := "probe_requires_plain_text_history"
			if *toolsMode {
				code = "probe_tool_history_unsupported"
			}
			http.Error(w, code, 409)
			diagnostic = proxy.Diagnostics{Status: 409, Rejection: code}
			detail := &probeBodyError{Reason: "request_unreadable", Item: -1, Part: -1}
			var parsed *probeBodyError
			if errors.As(err, &parsed) {
				detail = parsed
			}
			report(map[string]any{"event": "probe_blocked", "code": code, "detail": detail})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		recoveryRequired = false
		mu.Unlock()
		r.ContentLength = int64(len(body))
		turn := &probeTurnWriter{ResponseWriter: w, holdTerminal: *toolsMode, onTerminal: func() { mu.Lock(); terminalReady = true; state("probe_state", false); mu.Unlock() }}
		attempted = true
		if *toolsMode && !compact {
			h.ServeHTTP(turn, r)
		} else {
			h.ServeHTTP(w, r)
		}
		d := h.Diagnostics()
		mu.Lock()
		failed = d.Status != 200 || d.ResponseFailure != "" || d.Rejection != ""
		if compact && !failed {
			previousSlot = selected
		}
		if *toolsMode && !compact {
			failed = failed || !turn.valid()
			turnPending = !failed && turn.pending()
			if !failed {
				previousSlot = selected
			}
		}
		completed = !failed
		mu.Unlock()
		if *toolsMode && !compact && !turn.valid() && d.Status == http.StatusOK {
			d.ResponseFailure = "probe_tool_response_unsupported"
		}
		diagnostic = d
		if *toolsMode && !compact && completed {
			if err := turn.release(); err != nil {
				completed = false
				diagnostic.ResponseFailure = "probe_client_write_failed"
			}
		}
	})}
	defer server.Close()
	go server.Serve(listener)
	instruction := "Run CODEX_HOME=<codex_home> codex in another terminal. After a completed reply, type b here. No hooks. Plain text only. Local history remains in this temporary directory."
	if *toolsMode {
		instruction = "Run CODEX_HOME=<codex_home> codex in another terminal. Experimental local tools; read-only sandbox by default. Switch after the whole tool turn finishes. CLI text compaction is portable; native encrypted compaction is pinned to its originating account. Server references are unsupported."
	}
	report(map[string]any{"event": "probe_ready", "slot": "a", "codex_home": home, "instruction": instruction})
	mu.Lock()
	state("probe_state", false)
	mu.Unlock()
	go func() {
		s := bufio.NewScanner(input)
		for s.Scan() {
			target := s.Text()
			mu.Lock()
			expected := revision
			if *managed {
				var command struct {
					Action    string `json:"action"`
					Slot      string `json:"slot"`
					Revision  uint64 `json:"revision"`
					RequestID uint64 `json:"request_id"`
				}
				if json.Unmarshal(s.Bytes(), &command) != nil {
					state("probe_selection", false)
					mu.Unlock()
					continue
				}
				if command.Action == "status" {
					// A UI may disappear before its account_changed notification. Once the
					// credential operation lock is free, reconcile pending slots locally.
					if owner, ok := access.(interface{ AccountsIdle() bool }); ok && owner.AccountsIdle() {
						changed := false
						for i, pending := range accountChanging {
							if pending {
								accountChanging[i] = false
								usageEpoch[i]++
								changed = true
								samples[i] = usage.LocalSnapshot(access, accountslot.All()[i], time.Now())
							}
						}
						if changed {
							publishUsage()
						}
					}
					state("probe_state", false)
					mu.Unlock()
					continue
				}
				if command.Action == "shutdown" {
					ok := !busy && !waiting && !turnPending
					state("probe_shutdown", ok)
					if ok {
						failed = true
						cancel()
						mu.Unlock()
						return
					}
					mu.Unlock()
					continue
				}
				if command.Action == "abandon_turn" {
					ok := command.Slot == slot && probeAbandonAllowed(command.Revision, revision, busy, waiting, turnPending, failed)
					if ok {
						// This is the user's assertion that local CLI tools stopped,
						// not an automatic cancellation inference or a successful turn.
						turnPending, failed = false, true
						revision++
					}
					state("probe_abandonment", ok)
					mu.Unlock()
					continue
				}
				if *automatic && command.Action == "usage_refresh" {
					id := command.RequestID
					switch {
					case id == 0:
						refreshEvent(id, "invalid", false)
					case usageRequestID != 0:
						refreshEvent(id, "busy", false)
					case !usageRunning && time.Since(usageFinished) < cooldown:
						refreshEvent(id, "cooldown", false)
					default:
						usageRequestID = id
						refreshEvent(id, "started", false)
						if !usageRunning {
							usageRunning = true
							usageWake <- struct{}{}
						}
					}
					mu.Unlock()
					continue
				}
				if *automatic && command.Action == "usage" {
					publishUsage()
					mu.Unlock()
					continue
				}
				if *automatic && (command.Action == "account_changing" || command.Action == "account_changed") && (accountslot.Valid(command.Slot)) {
					i := accountslot.Index(command.Slot)
					usageEpoch[i]++
					accountChanging[i] = command.Action == "account_changing"
					samples[i] = usage.Snapshot{Slot: command.Slot, State: "unknown", Stale: true}
					if command.Action == "account_changed" {
						samples[i] = usage.LocalSnapshot(access, command.Slot, time.Now())
					}
					publishUsage()
					mu.Unlock()
					continue
				}
				if command.Action != "select" {
					state("probe_selection", false)
					mu.Unlock()
					continue
				}
				target, expected = command.Slot, command.Revision
			}
			recovering := failed && target != slot
			ok := probeSelectionAllowed(target, expected, revision, busy || turnPending || waiting, failed && !recovering)
			if opaqueSlot != "" && target != opaqueSlot {
				ok = false
			}
			if ok && *automatic {
				next, _ := probeQuotaSelection(target, samples, time.Now())
				ok = next == target
			}
			if ok {
				if _, err := access.Access(target, time.Now()); err != nil {
					ok = false
				}
			}
			if ok {
				if recovering {
					next, err := proxy.New(upstream, resolve)
					if err != nil {
						state("probe_selection", false)
						mu.Unlock()
						continue
					}
					h.Close()
					h = next
					if *toolsMode {
						h.ValidateCompaction = validateCompact
					}
					failed, recoveryRequired = false, true
					previousSlot = ""
				}
				slot = target
				chosen = true
				revision++
			}
			state("probe_selection", ok)
			mu.Unlock()
		}
		cancel()
	}()
	<-ctx.Done()
	return nil
}

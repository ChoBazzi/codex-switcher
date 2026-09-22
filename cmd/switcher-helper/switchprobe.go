package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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
	"sort"
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
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
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
		if err := probeTextMessage(item, i); err != nil {
			return nil, err
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

// Share the existing text contract without serializing each message into a
// temporary request and decoding it again in the tools path.
func probeTextMessage(item map[string]json.RawMessage, i int) error {
	bad := func(reason string, item, part int) error { return &probeBodyError{reason, item, part} }
	kind, role := probeString(item, "type"), probeString(item, "role")
	if kind != "" && kind != "message" {
		return bad("item_type_"+probeCategory(kind), i, -1)
	}
	if role != "user" && role != "assistant" && role != "system" && role != "developer" {
		return bad("unsupported_role", i, -1)
	}
	if raw, exists := item["phase"]; exists {
		var phase string
		if role != "assistant" || json.Unmarshal(raw, &phase) != nil || phase != "commentary" && phase != "final_answer" {
			return bad("message_phase_invalid", i, -1)
		}
	}
	for key := range item {
		if key != "type" && key != "role" && key != "content" && key != "id" && key != "status" && key != "phase" {
			return bad("message_field_"+probeCategory(key), i, -1)
		}
	}
	var parts []struct {
		Type string `json:"type"`
	}
	var plain string
	if json.Unmarshal(item["content"], &plain) != nil {
		if json.Unmarshal(item["content"], &parts) != nil {
			return bad("invalid_content_shape", i, -1)
		}
		for j, part := range parts {
			if part.Type != "input_text" && part.Type != "output_text" {
				return bad("content_type_"+probeCategory(part.Type), i, j)
			}
		}
	}
	return nil
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

func probeHistoryCredential(access probeAccess, slot string) ([32]byte, error) {
	if local, ok := access.(interface {
		HistoryCredential(string) ([32]byte, error)
	}); ok {
		return local.HistoryCredential(slot)
	}
	c, err := access.Access(slot, time.Now())
	if err != nil {
		return [32]byte{}, err
	}
	return c.HistoryCredential(), nil
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
	return switchProbeWithCheckpoint(args, input, output, access, upstream, fetcher, interval, cooldown, "")
}

func switchProbeWithCheckpoint(args []string, input io.Reader, output io.Writer, access probeAccess, upstream string, fetcher usage.Fetcher, interval, cooldown time.Duration, checkpointDir string) error {
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
	var saved *probeCheckpoint
	var checkpointErr error
	if checkpointDir != "" {
		saved, checkpointErr = readProbeCheckpoint(checkpointDir)
		if checkpointErr != nil {
			return checkpointErr
		}
	}
	secret := newDirectSecret()
	if saved != nil {
		secret = saved.Secret
	}
	var mu sync.Mutex
	var handlers sync.WaitGroup
	closing := false
	slot, session := "a", ""
	busy, failed := false, false
	turnPending, previousSlot := false, ""
	var previousCredential, activeTurnOwner [32]byte
	var requestDone chan struct{}
	terminalReady, waiting := false, false
	var lastBodyHash [32]byte
	var lastUserBoundary [32]byte
	recoveryRequired := false
	compactOwners := probeCompactRegistry{}
	var reasoningOwners probeReasoningOwners
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
	auxiliary := map[string]*probeAuxiliary{}
	auxActive := func() bool {
		for _, a := range auxiliary {
			if a.busy || a.pending {
				return true
			}
		}
		return false
	}
	auxBusy := func() bool {
		for _, a := range auxiliary {
			if a.busy {
				return true
			}
		}
		return false
	}
	auxPending := func() bool {
		for _, a := range auxiliary {
			if a.pending {
				return true
			}
		}
		return false
	}
	defer func() {
		for _, a := range auxiliary {
			if a.handler != nil {
				a.handler.Close()
			}

		}
	}()
	var revision uint64
	if saved != nil {
		slot, session, previousSlot = saved.Slot, saved.Session, saved.PreviousSlot
		previousCredential = saved.PreviousCredential
		for _, binding := range saved.Auxiliary {
			auxiliary[binding.Thread] = &probeAuxiliary{binding: binding, failed: true}
		}
		chosen, failed = saved.Chosen, saved.Failed || saved.Busy || saved.TurnPending
		recoveryRequired = saved.RecoveryRequired || session != ""
		lastBodyHash, lastUserBoundary = saved.LastBody, saved.LastUser
		revision, opaqueSlot = saved.Revision+1, saved.OpaqueSlot
		for _, owner := range saved.Owners {
			compactOwners[owner.Key] = probeCompactOwner{owner.Slot, owner.Credential}
		}
	}
	var checkpointAddress, checkpointHome string
	checkpointRetired := false
	var lastCheckpoint *probeCheckpoint
	persist := func() bool {
		if checkpointDir == "" {
			return true
		}
		if checkpointErr != nil {
			return false
		}
		c := &probeCheckpoint{Retired: checkpointRetired, Version: 1, Address: checkpointAddress, Home: checkpointHome, Secret: secret,
			Session: session, Slot: slot, PreviousSlot: previousSlot, Chosen: chosen, Busy: busy,
			Failed: failed, TurnPending: turnPending, RecoveryRequired: recoveryRequired,
			LastBody: lastBodyHash, LastUser: lastUserBoundary, PreviousCredential: previousCredential, Revision: revision, OpaqueSlot: opaqueSlot}
		for _, a := range auxiliary {
			c.Auxiliary = append(c.Auxiliary, a.binding)
		}
		sort.Slice(c.Auxiliary, func(i, j int) bool { return c.Auxiliary[i].Thread < c.Auxiliary[j].Thread })
		if sameProbeCheckpointRegistry(lastCheckpoint, c, compactOwners) {
			return true
		}
		for key, owner := range compactOwners {
			c.Owners = append(c.Owners, probeCheckpointOwner{key, owner.credential, owner.slot})
		}
		checkpointErr = writeProbeCheckpoint(checkpointDir, c)
		if checkpointErr != nil {
			failed = true
			cancel()
			return false
		}
		lastCheckpoint = c
		return true
	}
	var outMu sync.Mutex
	report := func(v any) { outMu.Lock(); defer outMu.Unlock(); _ = json.NewEncoder(output).Encode(v) }
	for _, a := range auxiliary {
		a.report = report
	}
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
		if !persist() {
			accepted = false
		}
		if !busy && !turnPending && !auxActive() && turnLease != nil {
			turnLease.Close()
			turnLease = nil
		}
		var authentication []accounts.AuthenticationStatus
		if source, ok := access.(interface {
			AuthenticationStatus() []accounts.AuthenticationStatus
		}); ok {
			authentication = source.AuthenticationStatus()
		}
		report(map[string]any{"event": event, "accepted": accepted, "slot": slot, "busy": busy || turnPending || auxActive(), "failed": failed, "connected": session != "", "revision": revision, "completion_pending": terminalReady,
			"authentication":  authentication,
			"auxiliary_count": len(auxiliary), "auxiliary_limit": probeAuxiliaryLimit,
			"can_recover_current": checkpointDir != "" && failed && !busy && !turnPending && !auxActive(),
			"can_abandon_turn":    (!busy && !waiting && !auxBusy() && (auxPending() || probeAbandonAllowed(revision, revision, busy, waiting, turnPending, failed)))})
	}
	resolve := func(r *http.Request) (proxy.Identity, error) {
		mu.Lock()
		selected, conversation := slot, session
		mu.Unlock()
		c, err := usage.RequestAccessContext(r.Context(), access, selected, time.Now())
		if err != nil {
			return proxy.Identity{}, probeAccessError(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if closing || checkpointErr != nil || slot != selected || session != conversation || r.Context().Err() != nil {
			return proxy.Identity{}, proxy.ErrAccountUnavailable
		}
		activeCredential = c.HistoryCredential()
		if activeTurnOwner != ([32]byte{}) && activeCredential != activeTurnOwner {
			return proxy.Identity{}, proxy.ErrHistoryOwner
		}
		for _, item := range activeCompactItems {
			if !compactOwners.permits(item, slot, activeCredential) {
				return proxy.Identity{}, proxy.ErrCompactionOwner
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
		var window struct{ Output []map[string]json.RawMessage }
		_ = json.Unmarshal(body, &window)
		keys := map[[32]byte]bool{}
		for _, item := range window.Output {
			if probeString(item, "type") == "reasoning" {
				keys[probeReasoningKey(item)] = true
			}
		}
		if !reasoningOwners.accept(keys, activeCredential, lastUserBoundary) {
			return errors.New("reasoning_owner_unavailable")
		}
		opaqueSlot = slot
		if !persist() {
			return errCheckpoint
		}
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
	address := "127.0.0.1:0"
	if saved != nil {
		address = saved.Address
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return errors.New("probe_listen_failed")
	}
	defer listener.Close()
	var home string
	if saved != nil {
		home = saved.Home
		err = privateServiceDir(home)
	} else {
		home, err = os.MkdirTemp(checkpointDir, "switcher-probe-")
	}
	checkpointAddress, checkpointHome = listener.Addr().String(), home
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
	if saved != nil {
		f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return errors.New("probe_profile_failed")
		}
		info, e := f.Stat()
		f.Close()
		if e != nil {
			return errors.New("probe_profile_failed")
		}
		st, owned := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !owned || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
			return errors.New("probe_profile_failed")
		}
	} else if writeProbeProfile(path, []byte(profile)) != nil {
		return errors.New("probe_profile_failed")
	}
	defer func() {
		if checkpointDir != "" {
			return
		}
		actual, err := os.ReadFile(path)
		if err == nil && bytes.Equal(actual, []byte(profile)) {
			_ = os.Remove(path)
		}
	}()
	server := probeHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if closing {
			mu.Unlock()
			http.Error(w, "proxy_service_stopping", 503)
			return
		}
		handlers.Add(1)
		mu.Unlock()
		defer handlers.Done()
		if len(r.Header.Values("X-Switcher-Run")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Switcher-Run")), []byte(secret)) != 1 {
			http.Error(w, "probe_unauthorized", 401)
			return
		}
		compact := *toolsMode && r.URL.Path == "/responses/compact"
		if r.Method != http.MethodPost || r.URL.RawQuery != "" || (r.URL.Path != "/responses" && !compact) {
			http.Error(w, "probe_unsupported_route", 404)
			return
		}
		id, root, identityErr := cliidentity.Conversation(r.Header)
		if *toolsMode && identityErr == nil && id != root {
			mu.Lock()
			if closing {
				mu.Unlock()
				http.Error(w, "proxy_service_stopping", 503)
				return
			}
			if session != "" && root != session {
				mu.Unlock()
				report(map[string]any{"event": "probe_blocked", "scope": "auxiliary", "code": "probe_conversation_changed"})
				http.Error(w, "probe_conversation_changed", 409)
				return
			}
			a := auxiliary[id]
			if a == nil {
				if code := probeAuxiliaryAdmission(failed, checkpointErr != nil, len(auxiliary)); code != "" {
					mu.Unlock()
					report(map[string]any{"event": "probe_blocked", "scope": "auxiliary", "code": code})
					http.Error(w, code, 409)
					return
				}
				if *automatic && !chosen {
					next, reason := probeQuotaSelection("", samples, time.Now())
					if next == "" {
						mu.Unlock()
						report(map[string]any{"event": "probe_blocked", "scope": "auxiliary", "code": reason})
						http.Error(w, reason, 409)
						return
					}
					slot, chosen = next, true
				}
				if session == "" {
					session = root
				}
				a = &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: id, Root: root, Slot: slot}, report: report}
				auxiliary[id] = a
			}
			mu.Unlock()
			a.serve(w, r, &mu, access, upstream, secret, func() bool { revision++; state("probe_state", false); return checkpointErr == nil }, func() error {
				if closing || checkpointErr != nil {
					return errCheckpoint
				}
				if turnLease == nil {
					if owner, ok := access.(interface{ BeginTurn() (io.Closer, error) }); ok {
						lease, err := owner.BeginTurn()
						if err != nil {
							return err
						}
						turnLease = lease
					}
				}
				return nil
			})
			return
		}
		id, err := cliidentity.ThreadID(r.Header)
		mu.Lock()
		if closing {
			mu.Unlock()
			http.Error(w, "proxy_service_stopping", 503)
			return
		}
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
		if code := probeAdmissionCode(err, busy, failed || checkpointErr != nil, session, id); code != "" {
			mu.Unlock()
			responseCode := code
			if code == "probe_identity_invalid" {
				responseCode += ": " + cliidentity.Diagnostic(r.Header)
				if !*toolsMode {
					responseCode += ": auxiliary_disabled"
				}
			}
			http.Error(w, responseCode, 409)
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
		if *automatic && !turnPending && !auxActive() {
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
		activeTurnOwner = [32]byte{}
		requestDone = make(chan struct{})
		finished := requestDone
		terminalReady = false
		revision++
		selected := slot
		owner := previousSlot
		ownerCredential := previousCredential
		state("probe_state", false)
		if checkpointErr != nil {
			busy = false
			mu.Unlock()
			http.Error(w, "proxy_checkpoint_unavailable", 503)
			return
		}
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
			activeTurnOwner = [32]byte{}
			terminalReady = false
			revision++
			state("probe_state", false)
			close(finished)
			mu.Unlock()
		}()
		body, err := readProbeBody(w, r)
		if err != nil {
			status, code := probeBodyRejection(err)
			diagnostic = proxy.Diagnostics{Status: status, Rejection: code}
			http.Error(w, code, status)
			return
		}
		var parsedInput *probeToolInput
		if *toolsMode {
			parsedInput = parseProbeToolInput(body)
		}
		if err == nil {
			hash := sha256.Sum256(body)
			var boundary [32]byte
			var hasUser bool
			if parsedInput != nil {
				boundary, hasUser = probeItemsUserBoundary(parsedInput.items)
			} else {
				boundary, hasUser = probeUserBoundary(body)
			}
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
				items := parsedInput.compactItems()
				credential, accessErr := probeHistoryCredential(access, selected)
				if accessErr != nil {
					status, code := probeAuthenticationRejection(accessErr)
					diagnostic = proxy.Diagnostics{Status: status, Rejection: code}
					http.Error(w, code, status)
					return
				}
				if credential == ([32]byte{}) || credential != ownerCredential {
					owner = ""
				}
				body, err = parsedInput.normalize(selected, owner, secret+":"+hex.EncodeToString(credential[:]), func(item map[string]json.RawMessage) bool {
					mu.Lock()
					defer mu.Unlock()
					return compactOwners.permits(item, selected, credential)
				}, func(item map[string]json.RawMessage) bool {
					mu.Lock()
					defer mu.Unlock()
					return reasoningOwners.permits(item, credential)
				})
				if err == nil {
					mu.Lock()
					activeCompactItems = items
					if parsedInput.needsTurnOwner {
						activeTurnOwner = ownerCredential
					}
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
			diagnostic.RejectionDetail = detail.Reason
			report(map[string]any{"event": "probe_blocked", "code": code, "detail": detail})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		recoveryRequired = false
		stored := persist()
		mu.Unlock()
		if !stored {
			http.Error(w, "proxy_checkpoint_unavailable", 503)
			return
		}
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
			previousCredential = activeCredential
		}
		if *toolsMode && !compact {
			failed = failed || !turn.valid()
			if !failed && !reasoningOwners.accept(turn.reasoning, activeCredential, lastUserBoundary) {
				failed = true
				d.ResponseFailure = "probe_reasoning_ownership_unavailable"
			}
			turnPending = !failed && turn.pending()
			if !failed {
				previousSlot = selected
				previousCredential = activeCredential
			}
		}
		completed = !failed
		if !persist() {
			completed = false
		}
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
	}))
	defer server.Close()
	instruction := "Run CODEX_HOME=<codex_home> codex in another terminal. After a completed reply, type b here. No hooks. Plain text only. Local history remains in this temporary directory."
	if *toolsMode {
		instruction = "Run CODEX_HOME=<codex_home> codex in another terminal. Experimental local tools; read-only sandbox by default. Switch after the whole tool turn finishes. CLI text compaction is portable; native encrypted compaction is pinned to its originating account. Server references are unsupported."
	}
	mu.Lock()
	stored := persist()
	mu.Unlock()
	if !stored {
		return errCheckpoint
	}
	go server.Serve(listener)
	mu.Lock()
	report(map[string]any{"event": "probe_ready", "slot": slot, "codex_home": home, "instruction": instruction, "build_id": processBuildID, "protocol_version": controlProtocolVersion})
	state("probe_state", false)
	mu.Unlock()
	go func() {
		s := bufio.NewScanner(input)
		for s.Scan() {
			target := s.Text()
			mu.Lock()
			if closing {
				mu.Unlock()
				return
			}
			expected := revision
			if *managed {
				var command struct {
					Action     string `json:"action"`
					NewSession bool   `json:"new_session"`
					Slot       string `json:"slot"`
					Revision   uint64 `json:"revision"`
					RequestID  uint64 `json:"request_id"`
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
					ok := !busy && !waiting && !turnPending && !auxActive()
					if ok && command.NewSession {
						checkpointRetired = true
					}
					if ok {
						closing = true
					}
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
					ok := command.Slot == slot && command.Revision == revision && !busy && !waiting && !auxBusy() && (auxPending() || probeAbandonAllowed(command.Revision, revision, busy, waiting, turnPending, failed))
					if ok {
						// This is the user's assertion that local CLI tools stopped,
						// not an automatic cancellation inference or a successful turn.
						turnPending, failed = false, true
						for _, a := range auxiliary {
							if a.pending {
								a.pending = false
								a.failed = true

							}
						}
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
				if command.Action == "recover" {
					// Explicit preparation only: never dispatch or replay a model request.
					if !*automatic || !failed || busy || turnPending || waiting || auxActive() || command.Revision != revision {
						state("probe_selection", false)
						mu.Unlock()
						continue
					}
					target, _ = probeQuotaSelection("", samples, time.Now())
					if opaqueSlot != "" {
						// Encrypted history must stay with its verified owner.
						target, _ = probeQuotaSelection(opaqueSlot, samples, time.Now())
					}
					command.Slot = target
				} else if command.Action != "select" {
					state("probe_selection", false)
					mu.Unlock()
					continue
				}
				target, expected = command.Slot, command.Revision
			}
			recovering := failed && (target != slot || checkpointDir != "" || *automatic)
			ok := probeSelectionAllowed(target, expected, revision, busy || turnPending || waiting || auxActive(), failed && !recovering)
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
					previousCredential = [32]byte{}
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
	mu.Lock()
	closing = true
	mu.Unlock()
	// Finish all state writes before the daemon releases its lifetime lock.
	server.Close()
	handlers.Wait()
	mu.Lock()
	defer mu.Unlock()
	return checkpointErr
}

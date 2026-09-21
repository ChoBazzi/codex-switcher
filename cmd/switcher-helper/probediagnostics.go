package main

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
	"github.com/ChoBazzi/codex-switcher/internal/proxy"
)

func probeAccessError(err error) error {
	if errors.Is(err, accounts.ErrStore) {
		return proxy.ErrCredentialStore
	}
	if errors.Is(err, accounts.ErrExpired) {
		return proxy.ErrAuthenticationExpired
	}
	if errors.Is(err, accounts.ErrNotRegistered) || errors.Is(err, accounts.ErrIdentity) {
		return proxy.ErrAccountUnavailable
	}
	return err
}

func probeAuthenticationRejection(err error) (int, string) {
	switch probeAccessError(err) {
	case proxy.ErrAuthenticationExpired:
		return 401, "authentication_expired"
	case proxy.ErrAccountUnavailable:
		return 401, "account_unavailable"
	case proxy.ErrCredentialStore:
		return 503, "credential_store_unavailable"
	default:
		return 401, "session_unavailable"
	}
}

// Explicit allowlist: never copy headers, error text, IDs, paths or payloads.
func diagnosticCategory(code string) string {
	switch code {
	case "history_owner_unavailable", "reasoning_owner_unavailable", "tool_turn_owner_unavailable", "probe_reasoning_ownership_unavailable":
		return "history_owner_unavailable"
	case "compaction_owner_unavailable":
		return code
	case "auxiliary_credential_changed":
		return code
	case "authentication_expired", "account_unavailable", "session_unavailable", "credential_store_unavailable", "request_body_timeout", "request_too_large", "request_unreadable", "auxiliary_capacity_reached", "request_canceled":
		return code
	case "probe_identity_invalid":
		return "cli_identity_invalid"
	case "probe_conversation_changed":
		return "conversation_changed"
	case "probe_previous_request_failed", "session_requires_user_action":
		return "previous_request_failed"
	case "probe_recovery_requires_new_input":
		return "new_input_required"
	case "probe_auxiliary_compaction_unsupported":
		return "auxiliary_compaction_unsupported"
	case "tool_output_missing", "tool_output_orphan_or_duplicate", "tool_pair_crosses_user_boundary":
		return "tool_history_incomplete"
	case "probe_tool_history_unsupported", "probe_requires_plain_text_history":
		return "history_unsupported"
	case "proxy_checkpoint_unavailable":
		return "checkpoint_unavailable"
	case "probe_request_in_progress", "probe_request_wait_timeout":
		return "request_busy"
	case "account_operation_busy":
		return "account_busy"
	case "probe_duplicate_followup":
		return "duplicate_followup"
	case "probe_usage_unavailable", "probe_no_available_account":
		return "account_unavailable"
	case "history_type_unsupported", "message_shape_unsupported", "message_content_unsupported", "agent_message_shape_unsupported", "reasoning_shape_unsupported", "tool_declaration_unsupported", "tool_call_invalid", "tool_output_invalid":
		return code
	}
	return ""
}

type probeDiagnostic struct {
	Event string `json:"event"`
	Scope string `json:"scope"`
	Code  string `json:"code"`
	At    string `json:"at"`
}

func sanitizedProbeDiagnostic(data []byte) *probeDiagnostic {
	var e struct {
		Event, Scope, Code string
		Detail             struct{ Reason string }
		proxy.Diagnostics
	}
	if json.Unmarshal(data, &e) != nil {
		return nil
	}
	if e.Event != "probe_blocked" && e.Event != "probe_request_finished" && e.Event != "probe_auxiliary_finished" {
		return nil
	}
	scope := "root"
	if e.Scope == "auxiliary" || e.Event == "probe_auxiliary_finished" {
		scope = "auxiliary"
	}
	code := diagnosticCategory(e.RejectionDetail)
	if code == "" {
		code = diagnosticCategory(e.Detail.Reason)
	}
	if code == "" {
		code = diagnosticCategory(e.Code)
	}
	if code == "" {
		code = diagnosticCategory(e.Rejection)
	}
	if code == "" {
		code = diagnosticCategory(e.ResponseFailure)
	}
	if code == "" {
		switch {
		case e.Status == 429:
			code = "upstream_rate_limited"
		case e.Status == 401 || e.Status == 403:
			code = "upstream_auth_rejected"
		case e.ResponseFailure != "":
			code = "response_interrupted"
		case e.Rejection != "" || e.Event == "probe_blocked":
			code = "request_rejected"
		case e.Status >= 400:
			code = "upstream_rejected"
		default:
			return nil
		}
	}
	return &probeDiagnostic{"probe_diagnostic", scope, code, time.Now().UTC().Format(time.RFC3339)}
}

// Package cliidentity validates the conversation headers observed in the
// supported Codex CLI. These are routing identifiers, not authentication.
package cliidentity

import (
	"errors"
	"net/http"
)

var ErrIdentity = errors.New("cli_conversation_identity_invalid")

// ThreadID fails closed on missing, repeated, conflicting or malformed values.
// Neither project nor worktree identity is inferred from these headers.
func ThreadID(header http.Header) (string, error) {
	threads, sessions := header.Values("Thread-Id"), header.Values("Session-Id")
	if len(threads) != 1 || len(sessions) != 1 || threads[0] != sessions[0] || !uuid(threads[0]) {
		return "", ErrIdentity
	}
	return threads[0], nil
}

func uuid(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return value != "00000000-0000-0000-0000-000000000000"
}

// Conversation validates the two independent identifiers used by auxiliary CLI
// tasks. Authentication and parent binding remain the caller's responsibility.
func Conversation(header http.Header) (thread, root string, err error) {
	threads, sessions := header.Values("Thread-Id"), header.Values("Session-Id")
	if len(threads) != 1 || len(sessions) != 1 || !uuid(threads[0]) || !uuid(sessions[0]) {
		return "", "", ErrIdentity
	}
	return threads[0], sessions[0], nil
}

// Diagnostic returns only fixed categories, never header values or identifiers.
func Diagnostic(header http.Header) string {
	threads, sessions := header.Values("Thread-Id"), header.Values("Session-Id")
	switch {
	case len(threads) == 0:
		return "thread_missing"
	case len(threads) != 1:
		return "thread_repeated"
	case len(sessions) == 0:
		return "session_missing"
	case len(sessions) != 1:
		return "session_repeated"
	case !uuid(threads[0]):
		return "thread_format_invalid"
	case !uuid(sessions[0]):
		return "session_format_invalid"
	case threads[0] != sessions[0]:
		return "distinct_valid_identifiers"
	default:
		return "matching_valid_identifiers"
	}
}

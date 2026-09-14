package cliidentity

import (
	"net/http"
	"testing"
)

func TestThreadID(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name              string
		threads, sessions []string
		valid             bool
	}{
		{"valid", []string{id}, []string{id}, true},
		{"missing", nil, nil, false},
		{"missing-thread", nil, []string{id}, false},
		{"missing-session", []string{id}, nil, false},
		{"conflict", []string{id}, []string{"22222222-2222-4222-8222-222222222222"}, false},
		{"duplicate", []string{id, id}, []string{id}, false},
		{"malformed", []string{"synthetic"}, []string{"synthetic"}, false},
		{"nil-uuid", []string{"00000000-0000-0000-0000-000000000000"}, []string{"00000000-0000-0000-0000-000000000000"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			for _, v := range tc.threads {
				header.Add("Thread-Id", v)
			}
			for _, v := range tc.sessions {
				header.Add("Session-Id", v)
			}
			got, err := ThreadID(header)
			if (err == nil) != tc.valid {
				t.Fatal("unexpected validation result")
			}
			if tc.valid && got != id {
				t.Fatal("wrong identity")
			}
		})
	}
}

func TestConversationIndependentIdentifiers(t *testing.T) {
	const thread = "11111111-1111-4111-8111-111111111111"
	const root = "22222222-2222-4222-8222-222222222222"
	header := http.Header{"Thread-Id": []string{thread}, "Session-Id": []string{root}}
	gotThread, gotRoot, err := Conversation(header)
	if err != nil || gotThread != thread || gotRoot != root {
		t.Fatal("independent identifiers rejected")
	}
	if _, err := ThreadID(header); err == nil {
		t.Fatal("legacy strict contract changed")
	}
	for _, value := range []string{"", "malformed", "00000000-0000-0000-0000-000000000000"} {
		header.Set("Session-Id", value)
		if _, _, err := Conversation(header); err == nil {
			t.Fatal("invalid root accepted")
		}
	}
	header.Set("Session-Id", root)
	header.Add("Thread-Id", thread)
	if _, _, err := Conversation(header); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestDiagnosticNeverReturnsHeaderValues(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		threads, sessions []string
		want              string
	}{
		{nil, nil, "thread_missing"},
		{[]string{id, id}, []string{id}, "thread_repeated"},
		{[]string{id}, nil, "session_missing"},
		{[]string{id}, []string{id, id}, "session_repeated"},
		{[]string{"synthetic-private-value"}, []string{id}, "thread_format_invalid"},
		{[]string{id}, []string{"synthetic-private-value"}, "session_format_invalid"},
		{[]string{id}, []string{"22222222-2222-4222-8222-222222222222"}, "distinct_valid_identifiers"},
		{[]string{id}, []string{id}, "matching_valid_identifiers"},
	} {
		if got := Diagnostic(http.Header{"Thread-Id": tc.threads, "Session-Id": tc.sessions}); got != tc.want {
			t.Fatal("incorrect fixed diagnostic category")
		}
	}
}

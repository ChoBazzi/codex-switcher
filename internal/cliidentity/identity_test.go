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

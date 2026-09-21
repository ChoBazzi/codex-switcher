package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalDiagnostic(t *testing.T) {
	h, err := New("http://127.0.0.1:1", func(*http.Request) (Identity, error) { return Identity{}, errors.New("synthetic-secret") })
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":"synthetic-secret"}`)))
	d := h.Diagnostics()
	if d.Requests != 1 || d.Attempts != 0 || d.Status != 401 || d.Rejection != "session_unavailable" {
		t.Fatalf("unexpected diagnostic: %+v", d)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "synthetic-secret") {
		t.Fatal("secret leaked")
	}
}

func TestUpstreamDiagnostic(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"synthetic-secret"}`))
	}))
	defer up.Close()
	h, err := New(up.URL, func(*http.Request) (Identity, error) {
		return Identity{Session: "synthetic", Token: "synthetic-secret"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":"synthetic"}`)))
	d := h.Diagnostics()
	if d.Requests != 1 || d.Attempts != 1 || d.Status != 400 || d.Rejection != "" {
		t.Fatalf("unexpected diagnostic: %+v", d)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "synthetic-secret") {
		t.Fatal("upstream body leaked")
	}
}

func TestResolverDiagnosticAllowlist(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{
		{ErrHistoryOwner, "history_owner_unavailable", 409},
		{ErrCompactionOwner, "compaction_owner_unavailable", 409},
		{ErrAuxiliaryCredential, "auxiliary_credential_changed", 409},
		{ErrAuthenticationExpired, "authentication_expired", 401},
		{ErrAccountUnavailable, "account_unavailable", 401},
		{ErrCredentialStore, "credential_store_unavailable", 503},
		{errors.New("history_owner_unavailable"), "session_unavailable", 401},
		{errors.New("synthetic-secret"), "session_unavailable", 401},
	} {
		h, err := New("http://127.0.0.1:1", func(*http.Request) (Identity, error) { return Identity{}, tc.err })
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":"synthetic"}`)))
		h.Close()
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "synthetic-secret") || h.Diagnostics().Rejection != tc.code || h.Diagnostics().Attempts != 0 {
			t.Fatal("resolver rejection was hidden, exposed, or dispatched")
		}
	}
}

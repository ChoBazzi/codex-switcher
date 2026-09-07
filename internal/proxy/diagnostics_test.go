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

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManagedStatusReadinessAndOwnerEOF(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret := strings.Repeat("synthetic-control-", 3)
	t.Setenv("SWITCHER_CONTROL_TOKEN", secret)
	input, owner := io.Pipe()
	output, ready := io.Pipe()
	defer input.Close()
	defer owner.Close()
	defer output.Close()
	defer ready.Close()
	done := make(chan error, 1)
	go func() { done <- statusCommandWithIO([]string{"--managed", "--port", "0"}, input, ready) }()
	var event struct {
		Event string `json:"event"`
		Port  int    `json:"port"`
	}
	decoded := make(chan error, 1)
	go func() { decoded <- json.NewDecoder(output).Decode(&event) }()
	select {
	case err := <-decoded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readiness timeout")
	}
	if event.Event != "status_server_ready" || event.Port < 1 {
		t.Fatal("bad readiness")
	}
	url := "http://127.0.0.1:" + strconv.Itoa(event.Port) + "/control/sessions"
	client := &http.Client{Timeout: time.Second}
	for _, authorized := range []bool{false, true} {
		req, _ := http.NewRequest("GET", url, nil)
		if authorized {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		want := http.StatusUnauthorized
		if authorized {
			want = http.StatusOK
		}
		if resp.StatusCode != want || strings.Contains(string(body), secret) {
			t.Fatal("authentication or redaction failed")
		}
	}
	owner.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server survived owner EOF")
	}
	if resp, err := client.Get(url); err == nil {
		resp.Body.Close()
		t.Fatal("server still listening")
	}
}

package cliprobe

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProfileLoopbackOnly(t *testing.T) {
	for _, endpoint := range []string{"https://example.com", "http://localhost:8765", "http://127.0.0.1", "http://127.0.0.1:8765/path", "http://user@127.0.0.1:8765", "http://127.0.0.1:8765?secret=x", "http://127.0.0.1:8765#fragment"} {
		if _, err := Profile(endpoint); err == nil {
			t.Errorf("accepted unsafe endpoint %q", endpoint)
		}
	}
	for _, endpoint := range []string{"http://127.0.0.1:8765", "http://[::1]:8765"} {
		profile, err := Profile(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		for _, setting := range []string{"request_max_retries = 0", "stream_max_retries = 0", "supports_websockets = false", "requires_openai_auth = false", endpoint} {
			if !strings.Contains(profile, setting) {
				t.Errorf("missing %s", setting)
			}
		}
	}
}

func TestSyntheticScenarios(t *testing.T) {
	for _, scenario := range []string{"success", "rate-limit", "server-error", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			u := &Upstream{Scenario: scenario}
			w := httptest.NewRecorder()
			u.ServeHTTP(w, httptest.NewRequest("POST", "/responses", strings.NewReader(`{"input":"synthetic"}`)))
			response := w.Result()
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			status := 200
			if scenario == "rate-limit" {
				status = 429
			}
			if scenario == "server-error" {
				status = 503
			}
			if response.StatusCode != status || u.Calls.Load() != 1 {
				t.Fatal("wrong status or request count")
			}
			if strings.Contains(string(body), `"type":"response.completed"`) != (scenario == "success") {
				t.Fatal("incorrect terminal event")
			}
		})
	}
}

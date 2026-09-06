package livetest

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

func Profile(endpoint, secret, model string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", errors.New("invalid_live_endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || len(secret) < 16 {
		return "", errors.New("invalid_live_endpoint")
	}
	if len(model) > 100 || strings.ContainsAny(model, "\r\n\x00") {
		return "", errors.New("invalid_model")
	}
	modelLine := ""
	if model != "" {
		modelLine = fmt.Sprintf("model = %q\n", model)
	}
	return fmt.Sprintf(`%smodel_provider = "switcher_live_test"
approval_policy = "never"
sandbox_mode = "read-only"
web_search = "disabled"
check_for_update_on_startup = false
cli_auth_credentials_store = "file"

[model_providers.switcher_live_test]
name = "Switcher single-account test"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
stream_idle_timeout_ms = 30000

[model_providers.switcher_live_test.http_headers]
X-Switcher-Run = %q
`, modelLine, endpoint, secret), nil
}

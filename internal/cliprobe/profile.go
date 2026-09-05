package cliprobe

import (
	"errors"
	"fmt"
	"net"
	"net/url"
)

// Profile is synthetic-only and accepts numeric loopback URLs. Write it only
// inside an isolated test CODEX_HOME, not the user's normal configuration.
func Profile(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", errors.New("invalid_probe_endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", errors.New("numeric_loopback_endpoint_required")
	}
	return fmt.Sprintf(`model = "switcher-synthetic"
model_provider = "switcher_probe"
approval_policy = "never"
sandbox_mode = "read-only"
web_search = "disabled"
check_for_update_on_startup = false
cli_auth_credentials_store = "file"

[model_providers.switcher_probe]
name = "Synthetic local probe"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
stream_idle_timeout_ms = 5000

[model_providers.switcher_probe.http_headers]
X-Switcher-Demo-Session = "demo"
`, endpoint), nil
}

package directcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Connection struct{ Endpoint, HookSecret string }

func validConnection(c Connection) bool {
	u, err := url.Parse(c.Endpoint)
	return err == nil && u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback() && u.Port() != "" && u.Path == "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && len(c.HookSecret) >= 32
}

func Profile(endpoint, modelSecret, helper, connectionPath string) (string, error) {
	if !validConnection(Connection{endpoint, modelSecret}) || !filepath.IsAbs(helper) || !filepath.IsAbs(connectionPath) {
		return "", ErrSession
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	command := quote(helper) + " direct-hook --connection " + quote(connectionPath)
	profile := fmt.Sprintf(`model_provider = "switcher"
[model_providers.switcher]
name = "Codex Switcher local proxy"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
stream_idle_timeout_ms = 30000
[model_providers.switcher.http_headers]
X-Switcher-Run = %q
`, endpoint, modelSecret)
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "SessionEnd"} {
		profile += fmt.Sprintf("\n[[hooks.%s]]\n[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = %q\ntimeout = 3\n", event, event, command)
	}
	return profile, nil
}

// Install only creates exclusive files. Existing config, profiles, symlinks and
// credentials are never overwritten. Cleanup removes only unchanged creations.
func Install(dir, helper, endpoint, modelSecret, hookSecret string) (func(), error) {
	if !validConnection(Connection{endpoint, hookSecret}) || modelSecret == hookSecret {
		return nil, ErrSession
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return nil, ErrSession
	}
	connectionPath := filepath.Join(dir, "switcher-connection.json")
	profile, err := Profile(endpoint, modelSecret, helper, connectionPath)
	if err != nil {
		return nil, err
	}
	connection, _ := json.Marshal(Connection{endpoint, hookSecret})
	created := map[string][]byte{}
	cleanup := func() {
		for path, expected := range created {
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			actual, err := os.ReadFile(path)
			if err == nil && bytes.Equal(actual, expected) {
				_ = os.Remove(path)
			}
		}
	}
	for _, file := range []struct {
		path string
		data []byte
	}{{connectionPath, connection}, {filepath.Join(dir, "switcher.config.toml"), []byte(profile)}} {
		f, err := os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			cleanup()
			return nil, ErrSession
		}
		_, writeErr := f.Write(file.data)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(file.path)
			cleanup()
			return nil, ErrSession
		}
		created[file.path] = file.data
	}
	return cleanup, nil
}

func SendHook(path string, e Event) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return ErrSession
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ErrSession
	}
	var connection Connection
	if json.Unmarshal(data, &connection) != nil || !validConnection(connection) {
		return ErrSession
	}
	body, _ := json.Marshal(e)
	req, err := http.NewRequest("POST", connection.Endpoint+"/control/cli-hook", bytes.NewReader(body))
	if err != nil {
		return ErrSession
	}
	req.Header.Set("X-Switcher-Hook", connection.HookSecret)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return ErrSession
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ErrSession
	}
	return nil
}

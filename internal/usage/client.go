// Package usage reads account quota metadata; it never calls a model.
package usage

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/accounts"
)

const Endpoint = "https://chatgpt.com/backend-api/wham/usage"

type Error struct {
	Code       string
	HTTPStatus int
}

func (e *Error) Error() string              { return e.Code }
func failure(code string, status int) error { return &Error{Code: code, HTTPStatus: status} }

type Client struct {
	endpoint  string
	transport *http.Transport
}

func NewClient() *Client { c, _ := newClient(Endpoint); return c }

// Only package tests can change the endpoint; production always uses Endpoint.
func newClient(endpoint string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, failure("usage_invalid_endpoint", 0)
	}
	ip := net.ParseIP(u.Hostname())
	if endpoint != Endpoint && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, failure("usage_invalid_endpoint", 0)
	}
	return &Client{endpoint: endpoint, transport: &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		ForceAttemptHTTP2: false, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}}, nil
}
func (c *Client) Close() { c.transport.CloseIdleConnections() }

func (c *Client) Fetch(ctx context.Context, access accounts.Access) (Data, error) {
	if access.Token == "" || access.AccountID == "" || !access.ExpiresAt.After(time.Now().Add(30*time.Second)) {
		return Data{}, failure("usage_auth_expired", 0)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "GET", c.endpoint, nil)
	if err != nil {
		return Data{}, failure("usage_request_failed", 0)
	}
	r.Close = true
	r.Header.Set("Authorization", "Bearer "+access.Token)
	r.Header.Set("ChatGPT-Account-ID", access.AccountID)
	r.Header.Set("Accept", "application/json")
	resp, err := c.transport.RoundTrip(r)
	if err != nil {
		return Data{}, failure("usage_network_error", 0)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		code := "usage_http_error"
		switch resp.StatusCode {
		case 401, 403:
			code = "usage_auth_required"
		case 429:
			code = "usage_rate_limited"
		}
		return Data{}, failure(code, resp.StatusCode)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return Data{}, failure("usage_encoding_unsupported", 200)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return Data{}, failure("usage_body_unreadable", 200)
	}
	defer clear(data)
	parsed, err := Parse(data)
	if err != nil {
		return Data{}, failure("usage_schema_unsupported", 200)
	}
	return parsed, nil
}

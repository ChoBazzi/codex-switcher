package accounts

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const MaxAuthBytes = 64 << 10

var ErrAuthFormat = errors.New("login_credentials_unsupported")

// Credentials may only be serialized into the credential vault, never logs or
// control API responses. JWT claims here are format checks, not signature verification.
type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token"`
	AccountID    string    `json:"account_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (Credentials) String() string   { return "[redacted credentials]" }
func (Credentials) GoString() string { return "[redacted credentials]" }

func ParseAuth(data []byte) (Credentials, error) {
	var file struct {
		Mode   string      `json:"auth_mode"`
		APIKey *string     `json:"OPENAI_API_KEY"`
		Tokens Credentials `json:"tokens"`
	}
	if len(data) > MaxAuthBytes || json.Unmarshal(data, &file) != nil || file.Mode != "chatgpt" || file.APIKey != nil {
		return Credentials{}, ErrAuthFormat
	}
	c := file.Tokens
	if !safeValue(c.AccessToken, 32<<10) || !safeValue(c.RefreshToken, 8192) || !safeValue(c.IDToken, 32<<10) || !safeValue(c.AccountID, 256) {
		return Credentials{}, ErrAuthFormat
	}
	parts := strings.Split(c.AccessToken, ".")
	if len(parts) != 3 {
		return Credentials{}, ErrAuthFormat
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Credentials{}, ErrAuthFormat
	}
	var claims struct {
		Expires int64 `json:"exp"`
		Auth    struct {
			Account string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Expires <= 0 || claims.Expires > 253402300799 || (claims.Auth.Account != "" && claims.Auth.Account != c.AccountID) {
		return Credentials{}, ErrAuthFormat
	}
	c.ExpiresAt = time.Unix(claims.Expires, 0).UTC()
	return c, nil
}

func safeValue(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, c := range s {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}

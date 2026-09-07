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
var ErrIdentity = errors.New("account_user_identity_unavailable")

// Credentials may only be serialized into the credential vault, never logs or
// control API responses. JWT claims here are format checks, not signature verification.
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
	// Derived from the access token, never an independently trusted JSON field.
	UserID    string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
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
	claims, err := accessClaims(c.AccessToken)
	if err != nil || claims.Expires <= 0 || claims.Expires > 253402300799 || (claims.Auth.Account != "" && claims.Auth.Account != c.AccountID) {
		return Credentials{}, ErrAuthFormat
	}
	if !safeValue(claims.Auth.User, 256) {
		return Credentials{}, ErrIdentity
	}
	c.UserID = claims.Auth.User
	c.ExpiresAt = time.Unix(claims.Expires, 0).UTC()
	return c, nil
}

type tokenClaims struct {
	Expires int64 `json:"exp"`
	Auth    struct {
		Account string `json:"chatgpt_account_id"`
		User    string `json:"chatgpt_user_id"`
	} `json:"https://api.openai.com/auth"`
}

func accessClaims(token string) (tokenClaims, error) {
	var claims tokenClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, ErrAuthFormat
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, ErrAuthFormat
	}
	defer clear(payload)
	if json.Unmarshal(payload, &claims) != nil {
		return claims, ErrAuthFormat
	}
	return claims, nil
}

func sameIdentity(a, b Credentials) bool {
	return a.UserID != "" && b.UserID != "" && a.AccountID == b.AccountID && a.UserID == b.UserID
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

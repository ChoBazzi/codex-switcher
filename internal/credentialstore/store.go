// Package credentialstore stores secrets using the operating-system Keychain.
// There is deliberately no plaintext fallback.
package credentialstore

import "errors"

// Five bounded credential records, including access and ID tokens.
const MaxBytes = 512 << 10
const Service = "com.bazzi.codex-switcher.accounts.v1"

var (
	ErrNotFound    = errors.New("credentials_not_found")
	ErrUnavailable = errors.New("keychain_unavailable")
	ErrInvalid     = errors.New("credentials_invalid")
)

type Vault interface {
	Read() ([]byte, error)
	Write([]byte) error
}

type Keychain struct{ service string }

func New() *Keychain { return &Keychain{service: Service} }

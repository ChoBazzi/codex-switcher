//go:build !darwin || !cgo

package credentialstore

func (k *Keychain) Read() ([]byte, error) { return nil, ErrUnavailable }
func (k *Keychain) Write([]byte) error    { return ErrUnavailable }

//go:build darwin && cgo

package credentialstore

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"testing"
)

func TestNativeKeychain(t *testing.T) {
	if os.Getenv("SWITCHER_KEYCHAIN_INTEGRATION") != "1" {
		t.Skip("opt-in synthetic Keychain test; may prompt for access")
	}
	k := &Keychain{service: Service + ".test." + rand.Text()}
	t.Cleanup(func() {
		if err := k.delete(); err != nil {
			t.Error("synthetic Keychain item cleanup failed")
		}
	})
	if _, err := k.Read(); !errors.Is(err, ErrNotFound) {
		t.Fatal("expected absent test item")
	}
	for _, size := range []int{1, (128 << 10) + 1, 256 << 10, MaxBytes, 16} {
		data := bytes.Repeat([]byte("s"), size)
		if err := k.Write(data); err != nil {
			t.Fatalf("Keychain write at %d bytes: %v", size, err)
		}
		// Reopen through a new store value to verify persistent retrieval.
		got, err := (&Keychain{service: k.service}).Read()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("Keychain round trip at %d bytes failed: %v", size, err)
		}
		// Rejected writes must not overwrite the last readable registry.
		for _, invalid := range [][]byte{nil, make([]byte, MaxBytes+1)} {
			if err := k.Write(invalid); !errors.Is(err, ErrInvalid) {
				t.Fatal("expected invalid size rejection")
			}
		}
		got, err = (&Keychain{service: k.service}).Read()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("rejected write changed stored data")
		}
	}
}

func TestKeychainWriteSizeGuard(t *testing.T) {
	// Validation happens before any native call; this test never opens Keychain.
	k := &Keychain{service: Service + ".test.invalid-size." + rand.Text()}
	for _, data := range [][]byte{nil, make([]byte, MaxBytes+1)} {
		if err := k.Write(data); !errors.Is(err, ErrInvalid) {
			t.Fatal("expected invalid size rejection before Keychain access")
		}
	}
}

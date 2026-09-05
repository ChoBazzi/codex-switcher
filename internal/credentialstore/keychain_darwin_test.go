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
	for _, data := range [][]byte{[]byte("synthetic-first"), []byte("synthetic-update")} {
		if err := k.Write(data); err != nil {
			t.Fatal(err)
		}
		// Reopen through a new store value to verify persistent retrieval.
		got, err := (&Keychain{service: k.service}).Read()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("Keychain round trip failed")
		}
	}
}

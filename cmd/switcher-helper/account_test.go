package main

import (
	"io"
	"testing"
)

// Invalid invocations must fail before touching Keychain or opening a browser.
func TestAccountCommandRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"login"}, {"login", "c"}, {"status", "a"}, {"reauth", "a", "extra"}} {
		if err := accountCommand(args, io.Discard); err == nil {
			t.Fatal("invalid account command accepted")
		}
	}
}

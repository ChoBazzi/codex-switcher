package main

import (
	"io"
	"testing"
)

func TestUsageRejectsInvalidArgumentsBeforeKeychain(t *testing.T) {
	for _, args := range [][]string{{"f"}, {"a", "b"}, {"--unknown"}, {"a", "--watch"}} {
		if err := usageCommand(args, io.Discard); err == nil {
			t.Fatal("accepted invalid arguments")
		}
	}
}

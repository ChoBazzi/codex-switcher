package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/ChoBazzi/codex-switcher/internal/projectidentity"
)

func projectCommand(args []string, out io.Writer) error {
	if len(args) > 1 {
		return errors.New("usage: switcher-helper project [directory]")
	}
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	origin, err := projectidentity.Resolve(context.Background(), dir)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(origin)
}

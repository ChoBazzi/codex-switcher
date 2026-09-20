package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
)

// Only self-contained local tool images can cross account boundaries. Never
// resolve a URL/file ID or read a path here. Preserve the original bytes/detail;
// raster validity and model-specific limits remain the upstream's responsibility.
func probeInlineToolImage(part map[string]json.RawMessage) bool {
	if !probeKeys(part, "type image_url detail") || probeString(part, "type") != "input_image" || !probeHasString(part, "image_url") {
		return false
	}
	if _, exists := part["detail"]; exists {
		switch probeString(part, "detail") {
		case "auto", "low", "high", "original":
		default:
			return false
		}
	}
	url := probeString(part, "image_url")
	// The complete request (images, text, and JSON) is independently capped at
	// 4 MiB by both ingress and dispatch. Do not increase those limits here.
	if len(url) > 4<<20 {
		return false
	}
	header, encoded, ok := strings.Cut(url, ",")
	if !ok || encoded == "" || strings.ContainsAny(encoded, "\r\n") {
		return false
	}
	// NewDecoder validates chunks independently; padding at a chunk boundary
	// must not allow another base64 segment to follow it.
	if pad := strings.IndexByte(encoded, '='); pad >= 0 && encoded[pad:] != "=" && encoded[pad:] != "==" {
		return false
	}
	switch header {
	case "data:image/png;base64", "data:image/jpeg;base64", "data:image/webp;base64", "data:image/gif;base64":
	default:
		return false
	}
	// Strict rejects noncanonical trailing bits. CR/LF need the explicit check
	// above because even Go's strict base64 decoder silently ignores them.
	_, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)))
	return err == nil
}

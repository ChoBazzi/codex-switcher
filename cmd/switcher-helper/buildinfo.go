package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

const controlProtocolVersion = 1

// Capture at process start, before a later build replaces the executable path.
// Commit alone is insufficient for different uncommitted development builds.
var processBuildID = executableBuildID()

func executableBuildID() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

package main

import (
	"encoding/json"
	"errors"
	"github.com/ChoBazzi/codex-switcher/internal/cliidentity"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"github.com/ChoBazzi/codex-switcher/internal/accountslot"
)

var errCheckpoint = errors.New("proxy_checkpoint_unavailable")

// No request/response bodies, OAuth credentials or account identifiers are stored.
// Secret is only the local CLI-to-proxy capability, already present in config.toml.
type probeCheckpoint struct {
	Auxiliary                                           []probeAuxiliaryBinding `json:",omitempty"`
	Retired                                             bool
	Version                                             int
	Address, Home, Secret                               string
	Session, Slot, PreviousSlot                         string
	Chosen, Busy, Failed, TurnPending, RecoveryRequired bool
	LastBody, LastUser                                  [32]byte
	Revision                                            uint64
	OpaqueSlot                                          string
	Owners                                              []probeCheckpointOwner
}
type probeCheckpointOwner struct {
	Key, Credential [32]byte
	Slot            string
}

func readProbeCheckpoint(dir string) (*probeCheckpoint, error) {
	if err := privateServiceDir(dir); err != nil {
		return nil, errCheckpoint
	}
	f, err := os.OpenFile(filepath.Join(dir, "checkpoint.json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errCheckpoint
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, errCheckpoint
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return nil, errCheckpoint
	}
	var c probeCheckpoint
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errCheckpoint
	}
	host, port, err := net.SplitHostPort(c.Address)
	if err != nil || host != "127.0.0.1" || port == "0" || len(c.Secret) < 32 || c.Version != 1 || !accountslot.Valid(c.Slot) || !filepath.IsAbs(c.Home) || filepath.Dir(c.Home) != dir || len(c.Owners) > 1024 {
		return nil, errCheckpoint
	}
	for _, s := range []string{c.PreviousSlot, c.OpaqueSlot} {
		if s != "" && !accountslot.Valid(s) {
			return nil, errCheckpoint
		}
	}
	for _, o := range c.Owners {
		if !accountslot.Valid(o.Slot) {
			return nil, errCheckpoint
		}
	}
	seenAux := map[string]bool{}
	if len(c.Auxiliary) > 128 {
		return nil, errCheckpoint
	}
	for _, a := range c.Auxiliary {
		headers := http.Header{"Thread-Id": []string{a.Thread}, "Session-Id": []string{a.Root}}
		if _, _, err := cliidentity.Conversation(headers); err != nil || a.Thread == a.Root || a.Root != c.Session || !accountslot.Valid(a.Slot) || seenAux[a.Thread] {
			return nil, errCheckpoint
		}
		seenAux[a.Thread] = true
	}
	if c.Retired {
		return nil, nil
	}
	return &c, nil
}

func writeProbeCheckpoint(dir string, c *probeCheckpoint) error {
	data, err := json.Marshal(c)
	if err != nil {
		return errCheckpoint
	}
	f, err := os.CreateTemp(dir, ".checkpoint-")
	if err != nil {
		return errCheckpoint
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || os.Rename(f.Name(), filepath.Join(dir, "checkpoint.json")) != nil {
		return errCheckpoint
	}
	d, err := os.Open(dir)
	if err != nil {
		return errCheckpoint
	}
	defer d.Close()
	if d.Sync() != nil {
		return errCheckpoint
	}
	return nil
}

func writeProbeProfile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errCheckpoint
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errCheckpoint
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return errCheckpoint
	}
	defer d.Close()
	if d.Sync() != nil {
		return errCheckpoint
	}
	return nil
}

// Registry iteration order is irrelevant; compare all persisted fields without
// retaining another copy of credentials or treating a status poll as a change.
func sameProbeCheckpoint(a, b *probeCheckpoint) bool {
	if a == nil || b == nil {
		return a == b
	}
	left, right := *a, *b
	left.Owners, right.Owners = nil, nil
	if !reflect.DeepEqual(left, right) || len(a.Owners) != len(b.Owners) {
		return false
	}
	owners := make(map[probeCheckpointOwner]int, len(a.Owners))
	for _, owner := range a.Owners {
		owners[owner]++
	}
	for _, owner := range b.Owners {
		if owners[owner] == 0 {
			return false
		}
		owners[owner]--
	}
	return true
}

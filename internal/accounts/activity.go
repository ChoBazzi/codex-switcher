package accounts

import (
	"io"
	"path/filepath"

	"github.com/ChoBazzi/codex-switcher/internal/applock"
)

// Account mutation and a complete model/tool turn exclude each other even when
// the UI is absent. Renewal uses the separate credential-operation lock.
func AcquireActivity(stateDir string) (*applock.Lock, error) {
	return applock.Acquire(filepath.Join(stateDir, "activity"))
}
func (m *Manager) BeginTurn() (io.Closer, error) {
	if m.tempParent == "" {
		return nil, nil
	}
	lease, err := AcquireActivity(m.tempParent)
	if err != nil {
		return nil, err
	}
	return lease, nil
}
func (m *Manager) AccountsIdle() bool {
	lock, err := applock.Acquire(m.tempParent)
	if err != nil {
		return false
	}
	lock.Close()
	return true
}

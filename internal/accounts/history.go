package accounts

import (
	"crypto/sha256"
	"encoding/json"
)

// historyBinding is stored only in the credential vault. A verified refresh
// carries Owner forward and binds it to the new credential. Unverified token or
// identity replacements cannot inherit the old owner through a stale binding.
type historyBinding struct {
	Owner   [32]byte `json:"owner"`
	Current [32]byte `json:"current"`
}

// HistoryCredential binds opaque history to an account/user/login incarnation.
// Only a verified helper refresh preserves ownership across token changes. It
// is not authentication and must never be accepted from a client. Zero means
// ownership is unavailable; legacy registrations keep their original digest.
func (a Access) HistoryCredential() [32]byte {
	current := a.credentialDigest()
	if current == ([32]byte{}) {
		return current
	}
	if a.history != nil && a.history.Current == current && a.history.Owner != ([32]byte{}) {
		return a.history.Owner
	}
	return current
}

func (a Access) credentialDigest() [32]byte {
	if !safeValue(a.Token, 32<<10) || !safeValue(a.AccountID, 256) || !safeValue(a.UserID, 256) {
		return [32]byte{}
	}
	data, _ := json.Marshal([]string{"codex-switcher/history-owner/v1", a.AccountID, a.UserID, a.Registration, a.Token})
	defer clear(data)
	return sha256.Sum256(data)
}

// HistoryCredential reads ownership without returning usable authentication or
// refreshing it. Expiry is checked by RequestAccess at dispatch, allowing an
// expired token's history to pass prevalidation before a verified refresh.
func (m *Manager) HistoryCredential(slot string) ([32]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validSlot(slot) {
		return [32]byte{}, ErrSlot
	}
	r, err := m.read()
	if err != nil {
		return [32]byte{}, err
	}
	for _, a := range r.Accounts {
		if a.Slot == slot {
			// In this process dispatch waits for the active exchange to commit.
			// After failure/restart there is no live exchange to join.
			if a.RefreshBlocked && m.refreshSlot != slot {
				return [32]byte{}, ErrRefresh
			}
			return a.access().HistoryCredential(), nil
		}
	}
	return [32]byte{}, ErrNotRegistered
}

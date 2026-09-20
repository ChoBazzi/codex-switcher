package accounts

import (
	"crypto/sha256"
	"encoding/json"
)

// HistoryCredential binds opaque history to the credential actually used for
// dispatch, including its account/user identity and login incarnation. It is
// not authentication and must never be accepted from a client. A zero digest
// means ownership is unavailable. Legacy registrations remain readable, but a
// subsequent login always gets a new incarnation even if tokens are identical.
func (a Access) HistoryCredential() [32]byte {
	if !safeValue(a.Token, 32<<10) || !safeValue(a.AccountID, 256) || !safeValue(a.UserID, 256) {
		return [32]byte{}
	}
	data, _ := json.Marshal([]string{"codex-switcher/history-owner/v1", a.AccountID, a.UserID, a.Registration, a.Token})
	defer clear(data)
	return sha256.Sum256(data)
}

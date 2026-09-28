package proxy

import "errors"

// Trusted resolvers return these sentinels. Never expose arbitrary error text.
var (
	ErrHistoryOwner          = errors.New("history_owner_unavailable")
	ErrCompactionOwner       = errors.New("compaction_owner_unavailable")
	ErrAuxiliaryCredential   = errors.New("auxiliary_credential_changed")
	ErrAuthenticationExpired = errors.New("authentication_expired")
	ErrAccountUnavailable    = errors.New("account_unavailable")
	ErrCredentialStore       = errors.New("credential_store_unavailable")
)

func identityRejection(err error) (int, string) {
	if errors.Is(err, ErrCredentialStore) {
		return 503, ErrCredentialStore.Error()
	}
	for _, known := range []error{ErrHistoryOwner, ErrCompactionOwner, ErrAuxiliaryCredential} {
		if errors.Is(err, known) {
			return 409, known.Error()
		}
	}
	for _, known := range []error{ErrAuthenticationExpired, ErrAccountUnavailable} {
		if errors.Is(err, known) {
			return 401, known.Error()
		}
	}
	return 401, "session_unavailable"
}

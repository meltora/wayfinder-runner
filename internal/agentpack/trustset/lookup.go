package trustset

import (
	"errors"
	"slices"
	"time"
)

var (
	ErrKeyAbsent          = errors.New("the signing key is not in the trusted key set")
	ErrKeyRevoked         = errors.New("the signing key is revoked by the trusted key set")
	ErrKeyOutsideValidity = errors.New("the signing key is outside its validity window")
)

func (key Key) ValidAt(now time.Time) bool {
	if now.Before(key.NotBefore) {
		return false
	}
	return key.NotAfter == nil || now.Before(*key.NotAfter)
}

func (verified Verified) TrustedKey(keyID string, now time.Time) (Key, error) {
	if slices.Contains(verified.Document.RevokedKeyIDs, keyID) {
		return Key{}, ErrKeyRevoked
	}
	for _, key := range verified.Document.Keys {
		if key.KeyID != keyID {
			continue
		}
		if !key.ValidAt(now) {
			return Key{}, ErrKeyOutsideValidity
		}
		return key, nil
	}
	return Key{}, ErrKeyAbsent
}

func (verified Verified) TrustedKeyIDs(now time.Time) []string {
	ids := make([]string, 0, len(verified.Document.Keys))
	for _, key := range verified.Document.Keys {
		if key.ValidAt(now) {
			ids = append(ids, key.KeyID)
		}
	}
	return ids
}

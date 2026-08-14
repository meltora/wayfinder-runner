package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CommandContentDigest identifies immutable command intent while excluding
// envelope delivery metadata such as sent_at, which changes on redelivery.
func CommandContentDigest(payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&canonical); err != nil {
		return "", err
	}
	canonicalRaw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonicalRaw)
	return hex.EncodeToString(digest[:]), nil
}

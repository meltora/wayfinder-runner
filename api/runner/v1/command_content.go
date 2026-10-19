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
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(canonical); err != nil {
		return "", err
	}
	canonicalRaw := unescapeLineSeparators(bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
	digest := sha256.Sum256(canonicalRaw)
	return hex.EncodeToString(digest[:]), nil
}

func unescapeLineSeparators(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	backslashes := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && backslashes%2 == 0 && i+5 < len(raw) &&
			(string(raw[i+1:i+6]) == "u2028" || string(raw[i+1:i+6]) == "u2029") {
			if raw[i+5] == '8' {
				out = append(out, " "...)
			} else {
				out = append(out, " "...)
			}
			i += 5
			backslashes = 0
			continue
		}
		if raw[i] == '\\' {
			backslashes++
		} else {
			backslashes = 0
		}
		out = append(out, raw[i])
	}
	return out
}

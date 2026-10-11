package trustset

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/example/app-runner/internal/agentpack/trustroot"
	"github.com/example/app-runner/internal/strictjson"
)

const (
	Format           = 1
	MaxKeys          = 64
	MaxRevokedKeyIDs = 256
	MaxDocumentBytes = 64 << 10
	MaxSequence      = 1<<53 - 1
	timestampLayout  = "2006-01-02T15:04:05Z"
)

var (
	keyIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)
	integerPattern   = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	documentFields   = []string{"format", "issuedAt", "keys", "revokedKeyIds", "sequence"}
	keyFields        = []string{"keyId", "notAfter", "notBefore", "publicKeySpkiBase64"}
	requiredKeyField = []string{"keyId", "notBefore", "publicKeySpkiBase64"}
)

type Key struct {
	KeyID               string
	PublicKeySPKIBase64 string
	NotBefore           time.Time
	NotAfter            *time.Time
}

type Document struct {
	Format        int
	Sequence      int64
	IssuedAt      time.Time
	Keys          []Key
	RevokedKeyIDs []string
}

func ParseCanonical(raw []byte) (Document, error) {
	document, err := ParseInput(raw)
	if err != nil {
		return Document{}, err
	}
	if !bytes.Equal(document.Canonical(), raw) {
		return Document{}, refuse(CodeNonCanonical, "document bytes are not the canonical encoding")
	}
	return document, nil
}

func ParseInput(raw []byte) (Document, error) {
	if len(raw) > MaxDocumentBytes {
		return Document{}, refuse(CodeTooLarge, "document exceeds %d bytes", MaxDocumentBytes)
	}
	for _, value := range raw {
		if value >= 0x80 || value == '\\' {
			return Document{}, refuse(CodeNonCanonical, "document must be ASCII without escape sequences")
		}
	}
	fields, err := strictjson.Object(raw)
	if err != nil {
		return Document{}, refuse(CodeMalformed, "%v", err)
	}
	if err := strictjson.ExactFields(fields, documentFields, documentFields); err != nil {
		return Document{}, refuse(CodeMalformed, "%v", err)
	}
	var document Document
	format, err := integer(fields["format"], CodeUnsupportedFormat)
	if err != nil {
		return Document{}, err
	}
	if format != Format {
		return Document{}, refuse(CodeUnsupportedFormat, "format %d is not supported", format)
	}
	document.Format = Format
	sequence, err := integer(fields["sequence"], CodeInvalidSequence)
	if err != nil {
		return Document{}, err
	}
	if sequence < 1 || sequence > MaxSequence {
		return Document{}, refuse(CodeInvalidSequence, "sequence must be between 1 and %d", int64(MaxSequence))
	}
	document.Sequence = sequence
	if document.IssuedAt, err = timestamp(fields["issuedAt"], "issuedAt"); err != nil {
		return Document{}, err
	}
	if document.Keys, err = parseKeys(fields["keys"]); err != nil {
		return Document{}, err
	}
	if document.RevokedKeyIDs, err = parseRevokedKeyIDs(fields["revokedKeyIds"], document.Keys); err != nil {
		return Document{}, err
	}
	return document, nil
}

func integer(raw json.RawMessage, outOfRange string) (int64, error) {
	number, err := strictjson.Number(raw)
	if err != nil {
		return 0, refuse(CodeMalformed, "expected an integer")
	}
	if !integerPattern.MatchString(number.String()) {
		return 0, refuse(CodeNonCanonical, "integers must not use fractions or exponents")
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil {
		return 0, refuse(outOfRange, "integer is out of range")
	}
	return value, nil
}

func timestamp(raw json.RawMessage, name string) (time.Time, error) {
	text, err := strictjson.String(raw)
	if err != nil {
		return time.Time{}, refuse(CodeMalformed, "%s must be a string", name)
	}
	if !timestampPattern.MatchString(text) {
		return time.Time{}, refuse(CodeInvalidTimestamp, "%s must be YYYY-MM-DDTHH:MM:SSZ", name)
	}
	parsed, err := time.Parse(timestampLayout, text)
	if err != nil {
		return time.Time{}, refuse(CodeInvalidTimestamp, "%s is not a valid UTC time", name)
	}
	return parsed.UTC(), nil
}

func keyID(raw json.RawMessage, name string) (string, error) {
	text, err := strictjson.String(raw)
	if err != nil {
		return "", refuse(CodeMalformed, "%s must be a string", name)
	}
	if !keyIDPattern.MatchString(text) {
		return "", refuse(CodeInvalidKeyID, "%s %q is not a valid key id", name, text)
	}
	return text, nil
}

func parseKeys(raw json.RawMessage) ([]Key, error) {
	items, err := strictjson.Array(raw)
	if err != nil {
		return nil, refuse(CodeMalformed, "keys must be an array")
	}
	if len(items) > MaxKeys {
		return nil, refuse(CodeTooManyKeys, "at most %d keys are allowed", MaxKeys)
	}
	keys := make([]Key, 0, len(items))
	publicKeys := map[string]string{}
	owners := map[string]string{}
	for _, item := range items {
		key, err := parseKey(item)
		if err != nil {
			return nil, err
		}
		if existing, ok := publicKeys[key.KeyID]; ok {
			if existing == key.PublicKeySPKIBase64 {
				return nil, refuse(CodeDuplicateKeyID, "key %s is listed twice", key.KeyID)
			}
			return nil, refuse(CodeKeyIDConflict, "key %s is listed with two public keys", key.KeyID)
		}
		if owner, ok := owners[key.PublicKeySPKIBase64]; ok {
			return nil, refuse(CodeDuplicatePublicKey, "keys %s and %s share one public key", owner, key.KeyID)
		}
		publicKeys[key.KeyID] = key.PublicKeySPKIBase64
		owners[key.PublicKeySPKIBase64] = key.KeyID
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].KeyID < keys[j].KeyID })
	return keys, nil
}

func parseKey(raw json.RawMessage) (Key, error) {
	fields, err := strictjson.Object(raw)
	if err != nil {
		return Key{}, refuse(CodeMalformed, "key: %v", err)
	}
	if err := strictjson.ExactFields(fields, keyFields, requiredKeyField); err != nil {
		return Key{}, refuse(CodeMalformed, "key: %v", err)
	}
	var key Key
	if key.KeyID, err = keyID(fields["keyId"], "keyId"); err != nil {
		return Key{}, err
	}
	encoded, err := strictjson.String(fields["publicKeySpkiBase64"])
	if err != nil {
		return Key{}, refuse(CodeMalformed, "key %s publicKeySpkiBase64 must be a string", key.KeyID)
	}
	if _, _, err := trustroot.Ed25519FromSPKIBase64(encoded); err != nil {
		return Key{}, refuse(CodeInvalidPublicKey, "key %s: %v", key.KeyID, err)
	}
	key.PublicKeySPKIBase64 = encoded
	if key.NotBefore, err = timestamp(fields["notBefore"], "notBefore"); err != nil {
		return Key{}, err
	}
	if rawNotAfter, ok := fields["notAfter"]; ok {
		notAfter, err := timestamp(rawNotAfter, "notAfter")
		if err != nil {
			return Key{}, err
		}
		if !notAfter.After(key.NotBefore) {
			return Key{}, refuse(CodeInvalidValidity, "key %s notAfter must be later than notBefore", key.KeyID)
		}
		key.NotAfter = &notAfter
	}
	return key, nil
}

func parseRevokedKeyIDs(raw json.RawMessage, keys []Key) ([]string, error) {
	items, err := strictjson.Array(raw)
	if err != nil {
		return nil, refuse(CodeMalformed, "revokedKeyIds must be an array")
	}
	if len(items) > MaxRevokedKeyIDs {
		return nil, refuse(CodeTooManyRevokedKeyIDs, "at most %d revoked key ids are allowed", MaxRevokedKeyIDs)
	}
	listed := make(map[string]bool, len(keys))
	for _, key := range keys {
		listed[key.KeyID] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		id, err := keyID(item, "revoked key id")
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, refuse(CodeDuplicateRevokedKeyID, "revoked key %s is listed twice", id)
		}
		if listed[id] {
			return nil, refuse(CodeKeyListedAndRevoked, "key %s is both listed and revoked", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

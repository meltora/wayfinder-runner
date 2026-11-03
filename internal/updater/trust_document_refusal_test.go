package updater

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func trustKeyEntry(t *testing.T, id string) map[string]any {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&throwawayTrustKey(t).PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"key_id": id, "public_key_b64": base64.StdEncoding.EncodeToString(der), "not_before": "2020-01-01T00:00:00Z", "not_after": "2099-01-01T00:00:00Z"}
}

func trustKeyEntries(t *testing.T, count int) []map[string]any {
	t.Helper()
	entries := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		entries = append(entries, trustKeyEntry(t, fmt.Sprintf("release-%02d", i)))
	}
	return entries
}

func trustDocumentJSON(t *testing.T, document map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseTrustDocumentRefusesEachInvalidShape(t *testing.T) {
	t.Parallel()
	valid := trustDocumentJSON(t, map[string]any{"schema_version": 1, "signature_threshold": 1, "keys": trustKeyEntries(t, 1)})
	if _, err := ParseTrustDocument([]byte(valid)); err != nil {
		t.Fatalf("the valid control document was refused: %v", err)
	}
	full := trustDocumentJSON(t, map[string]any{"schema_version": 1, "signature_threshold": 32, "keys": trustKeyEntries(t, 32)})
	if policy, err := ParseTrustDocument([]byte(full)); err != nil || len(policy.Keys) != 32 || policy.Threshold != 32 {
		t.Fatalf("32 keys with threshold 32 got %+v, %v", policy, err)
	}
	twoKeys := trustKeyEntries(t, 2)
	duplicate := []map[string]any{twoKeys[0], trustKeyEntry(t, twoKeys[0]["key_id"].(string))}
	keyWithExtra := trustKeyEntry(t, "release-extra")
	keyWithExtra["comment"] = "not part of the schema"
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"schema version 2", trustDocumentJSON(t, map[string]any{"schema_version": 2, "signature_threshold": 1, "keys": trustKeyEntries(t, 1)}), "schema or threshold"},
		{"schema version 0", trustDocumentJSON(t, map[string]any{"schema_version": 0, "signature_threshold": 1, "keys": trustKeyEntries(t, 1)}), "schema or threshold"},
		{"threshold above the key count", trustDocumentJSON(t, map[string]any{"schema_version": 1, "signature_threshold": 2, "keys": trustKeyEntries(t, 1)}), "threshold exceeds the trusted keys"},
		{"more than 32 keys", trustDocumentJSON(t, map[string]any{"schema_version": 1, "signature_threshold": 1, "keys": trustKeyEntries(t, 33)}), "threshold exceeds the trusted keys"},
		{"duplicate key id", trustDocumentJSON(t, map[string]any{"schema_version": 1, "signature_threshold": 1, "keys": duplicate}), "invalid or duplicate key id"},
		{"unsafe key id with a path separator", trustDocumentJSON(t, map[string]any{"schema_version": 1, "signature_threshold": 1, "keys": []map[string]any{trustKeyEntry(t, "release/../escape")}}), "invalid or duplicate key id"},
		{"empty key id", trustDocumentJSON(t, map[string]any{"schema_version": 1, "signature_threshold": 1, "keys": []map[string]any{trustKeyEntry(t, "")}}), "invalid or duplicate key id"},
		{"unknown top-level field", strings.TrimSuffix(valid, "}") + `,"channel":"production"}`, "unknown field"},
		{"unknown key field", trustDocumentJSON(t, map[string]any{"schema_version": 1, "signature_threshold": 1, "keys": []map[string]any{keyWithExtra}}), "unknown field"},
		{"duplicate JSON field", `{"schema_version":1,` + strings.TrimPrefix(valid, "{"), "duplicate field"},
		{"trailing data", valid + `{}`, "trailing data"},
		{"trailing garbage", valid + ` x`, "trailing data"},
	}
	for _, tc := range cases {
		_, err := ParseTrustDocument([]byte(tc.raw))
		if !errors.Is(err, ErrTrustInvalid) {
			t.Fatalf("%s: got %v, want ErrTrustInvalid", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

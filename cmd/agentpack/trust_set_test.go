package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type rootKey struct {
	path     string
	keyID    string
	spki     string
	rootFile string
}

func newRootKey(t *testing.T) rootKey {
	t.Helper()
	path, identity := keygen(t)
	key := rootKey{path: path, keyID: identity["spkiSha256"].(string), spki: identity["publicKeySpkiBase64"].(string)}
	key.rootFile = writeRootsFile(t, key)
	return key
}

func writeRootsFile(t *testing.T, keys ...rootKey) string {
	t.Helper()
	roots := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		roots = append(roots, map[string]string{"keyId": key.keyID, "publicKeySpkiBase64": key.spki})
	}
	raw, err := json.Marshal(map[string]any{"format": 1, "roots": roots})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "roots.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTextFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func setInput(packKeys map[string]string, sequence string, revoked string) string {
	entries := make([]string, 0, len(packKeys))
	for id, public := range packKeys {
		entries = append(entries, fmt.Sprintf(`{"keyId": %q, "publicKeySpkiBase64": %q, "notBefore": "2026-01-01T00:00:00Z"}`, id, public))
	}
	return fmt.Sprintf(`{
  "sequence": %s,
  "format": 1,
  "issuedAt": "2026-10-10T00:00:00Z",
  "keys": [%s],
  "revokedKeyIds": %s
}`, sequence, strings.Join(entries, ", "), revoked)
}

func signSet(t *testing.T, root rootKey, input string) (string, invocation) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "set.signed.json")
	return out, invoke("trust-set", "sign", "--root-key", root.path, "--in", writeTextFile(t, input), "--out", out)
}

func expectJSONError(t *testing.T, result invocation, code string) {
	t.Helper()
	if result.code != exitFailure || result.stdout != "" {
		t.Fatalf("result = %+v, want exit 1 with no stdout", result)
	}
	var failure struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(strings.NewReader(result.stderr))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&failure); err != nil {
		t.Fatalf("stderr is not a JSON error: %q", result.stderr)
	}
	if failure.Error.Code != code || failure.Error.Message == "" {
		t.Fatalf("error = %+v, want code %s", failure.Error, code)
	}
}

func TestTrustSetSignProducesCanonicalByteIdenticalOutput(t *testing.T) {
	root := newRootKey(t)
	packKey := newRootKey(t)
	input := setInput(map[string]string{"platform-2026": packKey.spki}, "1", `[]`)
	first, result := signSet(t, root, input)
	if result.code != exitOK {
		t.Fatalf("sign = %+v", result)
	}
	assertNoSecretOutput(t, result, root.path)
	summary := decodeObject(t, result.stdout)
	if summary["rootKeyId"] != root.keyID || summary["sequence"] != float64(1) || len(summary["documentSha256"].(string)) != 64 {
		t.Fatalf("summary = %v", summary)
	}
	second, again := signSet(t, root, input)
	if again.code != exitOK {
		t.Fatalf("second sign = %+v", again)
	}
	firstBytes, _ := os.ReadFile(first)
	secondBytes, _ := os.ReadFile(second)
	if len(firstBytes) == 0 || !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("the same input produced different signed sets")
	}
	assertNoSecretOutput(t, invocation{stdout: string(firstBytes)}, root.path)
	var signed map[string]string
	decoder := json.NewDecoder(bytes.NewReader(firstBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&signed); err != nil || strings.Join(sortedStringKeys(signed), ",") != "document_base64,root_key_id,signature_base64" {
		t.Fatalf("signed set = %v %v", signed, err)
	}
	verified := invoke("trust-set", "verify", "--roots", root.rootFile, "--in", first)
	if verified.code != exitOK {
		t.Fatalf("verify = %+v", verified)
	}
	output := decodeObject(t, verified.stdout)
	if output["valid"] != true || output["sequence"] != float64(1) || output["rootKeyId"] != root.keyID {
		t.Fatalf("verify output = %v", output)
	}
	if keys := output["keyIds"].([]any); len(keys) != 1 || keys[0] != "platform-2026" {
		t.Fatalf("verified key ids = %v", keys)
	}
}

func sortedStringKeys(value map[string]string) []string {
	converted := make(map[string]any, len(value))
	for key, item := range value {
		converted[key] = item
	}
	return sortedKeys(converted)
}

func TestTrustSetSignRefusesInvalidSets(t *testing.T) {
	root := newRootKey(t)
	first, second := newRootKey(t), newRootKey(t)
	many := map[string]string{}
	for index := range 65 {
		many[fmt.Sprintf("k%02d", index)] = first.spki
	}
	cases := map[string]struct {
		input string
		code  string
	}{
		"duplicate key id":            {strings.Replace(setInput(map[string]string{"a": first.spki}, "1", `[]`), `"keys": [`, fmt.Sprintf(`"keys": [{"keyId": "a", "publicKeySpkiBase64": %q, "notBefore": "2026-01-01T00:00:00Z"}, `, first.spki), 1), "duplicate_key_id"},
		"key id with two public keys": {strings.Replace(setInput(map[string]string{"a": first.spki}, "1", `[]`), `"keys": [`, fmt.Sprintf(`"keys": [{"keyId": "a", "publicKeySpkiBase64": %q, "notBefore": "2026-01-01T00:00:00Z"}, `, second.spki), 1), "key_id_conflict"},
		"listed and revoked":          {setInput(map[string]string{"a": first.spki}, "1", `["a"]`), "key_listed_and_revoked"},
		"sequence zero":               {setInput(map[string]string{"a": first.spki}, "0", `[]`), "invalid_sequence"},
		"fractional":                  {setInput(map[string]string{"a": first.spki}, "1.5", `[]`), "non_canonical"},
		"too many keys":               {setInput(many, "1", `[]`), "too_many_keys"},
		"unknown field":               {strings.Replace(setInput(map[string]string{"a": first.spki}, "1", `[]`), `"format": 1,`, `"format": 1, "scope": "x",`, 1), "malformed"},
	}
	for name, tc := range cases {
		out, result := signSet(t, root, tc.input)
		expectJSONError(t, result, tc.code)
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("%s: a refused set left output behind", name)
		}
	}
}

func TestTrustSetSignNeverOverwritesOutput(t *testing.T) {
	root := newRootKey(t)
	input := writeTextFile(t, setInput(map[string]string{"a": newRootKey(t).spki}, "1", `[]`))
	out := filepath.Join(t.TempDir(), "set.signed.json")
	if err := os.WriteFile(out, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := invoke("trust-set", "sign", "--root-key", root.path, "--in", input, "--out", out)
	expectJSONError(t, result, "output_exists")
	if raw, _ := os.ReadFile(out); string(raw) != "keep" {
		t.Fatal("sign overwrote an existing file")
	}
}

func TestTrustSetVerifyRefusesForeignRootsAndNonCanonicalDocuments(t *testing.T) {
	develop, production := newRootKey(t), newRootKey(t)
	signed, result := signSet(t, develop, setInput(map[string]string{"a": newRootKey(t).spki}, "3", `[]`))
	if result.code != exitOK {
		t.Fatal(result)
	}
	expectJSONError(t, invoke("trust-set", "verify", "--roots", production.rootFile, "--in", signed), "unknown_root")
	both := writeRootsFile(t, develop, production)
	if ok := invoke("trust-set", "verify", "--roots", both, "--in", signed); ok.code != exitOK {
		t.Fatalf("verify with both roots = %+v", ok)
	}
	raw, _ := os.ReadFile(signed)
	var envelope map[string]string
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["signature_base64"] = strings.Repeat("A", 86) + "=="
	tampered, _ := json.Marshal(envelope)
	expectJSONError(t, invoke("trust-set", "verify", "--roots", both, "--in", writeTextFile(t, string(tampered))), "signature_invalid")
	expectJSONError(t, invoke("trust-set", "verify", "--roots", both, "--in", writeTextFile(t, `{"document_base64":"e30=","root_key_id":"x"}`)), "invalid_envelope")
}

func TestTrustSetUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{"trust-set"},
		{"trust-set", "rotate"},
		{"trust-set", "sign", "--in", "a", "--out", "b"},
		{"trust-set", "verify", "--in", "a"},
		{"verify", "--roots", "r"},
	} {
		if result := invoke(args...); result.code != exitUsage || result.stdout != "" {
			t.Errorf("%v = %+v, want exit 2", args, result)
		}
	}
}

func TestVerifyChecksTheReleaseAgainstTheTrustedKeySet(t *testing.T) {
	root := newRootKey(t)
	packKeyPath, packIdentity := keygen(t)
	archive := buildGolden(t)
	signedRequest := invoke(signArgs(packKeyPath, archive, "--url", "https://packs.example.test/fixture.tgz", "--request-id", "publish-1")...)
	if signedRequest.code != exitOK {
		t.Fatalf("sign = %+v", signedRequest)
	}
	request := writeTextFile(t, signedRequest.stdout)
	trusted, result := signSet(t, root, setInput(map[string]string{"platform-2026": packIdentity["publicKeySpkiBase64"].(string)}, "2", `[]`))
	if result.code != exitOK {
		t.Fatal(result)
	}
	verify := func(set, archivePath string) invocation {
		return invoke("verify", "--roots", root.rootFile, "--trust-set", set, "--publish-request", request, "--archive", archivePath)
	}
	ok := verify(trusted, archive)
	if ok.code != exitOK {
		t.Fatalf("verify = %+v", ok)
	}
	output := decodeObject(t, ok.stdout)
	if output["valid"] != true || output["digest"] != goldenDigest || output["keyId"] != "platform-2026" || output["trustSetSequence"] != float64(2) {
		t.Fatalf("verify output = %v", output)
	}
	other := newRootKey(t)
	absent, _ := signSet(t, root, setInput(map[string]string{"other": other.spki}, "3", `[]`))
	expectJSONError(t, verify(absent, archive), "signing_key_untrusted")
	revoked, _ := signSet(t, root, setInput(map[string]string{"other": other.spki}, "4", `["platform-2026"]`))
	expectJSONError(t, verify(revoked, archive), "signing_key_revoked")
	impostor, _ := signSet(t, root, setInput(map[string]string{"platform-2026": other.spki}, "5", `[]`))
	expectJSONError(t, verify(impostor, archive), "attestation_invalid")
	otherArchive := filepath.Join(t.TempDir(), "other.tgz")
	if build := invoke("build", "--source", goldenSource, "--pack-key", "fixture", "--version", "1.0.1", "--provider", "claude-code", "--out", otherArchive); build.code != exitOK {
		t.Fatalf("build = %+v", build)
	}
	expectJSONError(t, verify(trusted, otherArchive), "archive_mismatch")
	foreign := newRootKey(t)
	foreignSet, _ := signSet(t, foreign, setInput(map[string]string{"platform-2026": packIdentity["publicKeySpkiBase64"].(string)}, "6", `[]`))
	expectJSONError(t, verify(foreignSet, archive), "unknown_root")
}

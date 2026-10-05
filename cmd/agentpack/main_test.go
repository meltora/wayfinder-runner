package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
)

const (
	goldenSource = "../../internal/agentpack/testdata/conformance/source"
	goldenDigest = "a0941e4ea443cb83a16ac465891ae2f9292196fc7d17fa32964828dc8ea46970"
)

type invocation struct {
	code   int
	stdout string
	stderr string
}

func invoke(args ...string) invocation {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return invocation{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func assertNoSecretOutput(t *testing.T, result invocation, keyPath string) {
	t.Helper()
	combined := result.stdout + result.stderr
	if strings.Contains(combined, "PRIVATE") || strings.Contains(combined, "-----BEGIN") {
		t.Fatalf("output contains PEM material: %q", combined)
	}
	if keyPath == "" {
		return
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if len(line) >= 16 && !strings.HasPrefix(line, "-----") && strings.Contains(combined, line[:16]) {
			t.Fatal("output contains private key material")
		}
	}
}

func decodeObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("stdout is not JSON: %q", raw)
	}
	return value
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func keygen(t *testing.T) (string, map[string]any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys", "platform.pem")
	result := invoke("keygen", "--out", path)
	if result.code != exitOK {
		t.Fatalf("keygen = %+v", result)
	}
	assertNoSecretOutput(t, result, path)
	return path, decodeObject(t, result.stdout)
}

func buildGolden(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "pack.tgz")
	result := invoke("build", "--source", goldenSource, "--pack-key", "fixture", "--version", "1.0.0", "--provider", "claude-code", "--out", out)
	if result.code != exitOK {
		t.Fatalf("build = %+v", result)
	}
	return out
}

func TestUsageErrorsExitTwo(t *testing.T) {
	cases := [][]string{
		{},
		{"unknown"},
		{"keygen", "--bogus"},
		{"keygen"},
		{"public-key", "--key"},
		{"build", "--source", goldenSource},
		{"build", "--nope", "1"},
		{"digest"},
		{"digest", "a", "b"},
		{"digest", "--x", "a"},
		{"prove", "--key", "k"},
		{"sign", "--unknown"},
		{"sign", "--key", "k", "--archive", "a", "--scope", "platform", "--pack-key", "p", "--pack-name", "n", "--provider", "claude-code", "--protocol-version", "1", "--minimum-runner-version", "0.1.0", "--key-id", "k"},
		{"sign", "--key", "k", "--archive", "a", "--scope", "platform", "--pack-key", "p", "--pack-name", "n", "--provider", "claude-code", "--protocol-version", "1", "--minimum-runner-version", "0.1.0", "--key-id", "k", "--url", "https://a.test/x", "--gateway-origin", "https://a.test"},
		{"sign", "--key", "k", "--archive", "a", "--scope", "global", "--pack-key", "p", "--pack-name", "n", "--provider", "claude-code", "--protocol-version", "1", "--minimum-runner-version", "0.1.0", "--key-id", "k", "--url", "https://a.test/x"},
		{"sign", "--key", "k", "--archive", "a", "--scope", "platform", "--pack-key", "p", "--pack-name", "n", "--provider", "claude-code", "--protocol-version", "one", "--minimum-runner-version", "0.1.0", "--key-id", "k", "--url", "https://a.test/x"},
		{"keygen", "-h"},
	}
	for _, args := range cases {
		result := invoke(args...)
		if result.code != exitUsage || result.stdout != "" {
			t.Errorf("%v = %+v, want exit 2 and no stdout", args, result)
		}
	}
}

func TestKeygenAndPublicKeyPrintOnlyPublicIdentity(t *testing.T) {
	path, identity := keygen(t)
	if keys := sortedKeys(identity); strings.Join(keys, ",") != "publicKeySpkiBase64,spkiSha256" {
		t.Fatalf("keygen keys = %v", keys)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v %v", info, err)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || parent.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %v %v", parent, err)
	}
	der, err := base64.StdEncoding.DecodeString(identity["publicKeySpkiBase64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParsePKIXPublicKey(der); err != nil {
		t.Fatal(err)
	}
	public := invoke("public-key", "--key", path)
	if public.code != exitOK {
		t.Fatalf("public-key = %+v", public)
	}
	assertNoSecretOutput(t, public, path)
	if again := decodeObject(t, public.stdout); again["publicKeySpkiBase64"] != identity["publicKeySpkiBase64"] || again["spkiSha256"] != identity["spkiSha256"] {
		t.Fatalf("public-key = %v, keygen = %v", again, identity)
	}
	before, _ := os.ReadFile(path)
	repeat := invoke("keygen", "--out", path)
	if repeat.code != exitFailure || repeat.stdout != "" || !strings.Contains(repeat.stderr, "never overwritten") {
		t.Fatalf("second keygen = %+v", repeat)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("keygen overwrote an existing key")
	}
	assertNoSecretOutput(t, repeat, path)
}

func TestBuildIsDeterministicAndRefusesOverwrite(t *testing.T) {
	out := buildGolden(t)
	result := invoke("build", "--source", goldenSource, "--pack-key", "fixture", "--version", "1.0.0", "--provider", "claude-code", "--out", out)
	if result.code != exitFailure || !strings.Contains(result.stderr, "--force") {
		t.Fatalf("overwrite = %+v", result)
	}
	forced := invoke("build", "--source", goldenSource, "--pack-key", "fixture", "--version", "1.0.0", "--provider", "claude-code", "--out", out, "--force")
	if forced.code != exitOK {
		t.Fatalf("forced = %+v", forced)
	}
	output := decodeObject(t, forced.stdout)
	if output["digest"] != goldenDigest || output["files"] != float64(5) || strings.Join(sortedKeys(output), ",") != "bytes,digest,files" {
		t.Fatalf("build output = %v", output)
	}
	raw, _ := os.ReadFile(out)
	if output["bytes"] != float64(len(raw)) {
		t.Fatalf("bytes = %v, file = %d", output["bytes"], len(raw))
	}
	digest := invoke("digest", out)
	if digest.code != exitOK || decodeObject(t, digest.stdout)["digest"] != goldenDigest {
		t.Fatalf("digest = %+v", digest)
	}
	for name, args := range map[string][]string{
		"provider":           {"build", "--source", goldenSource, "--pack-key", "fixture", "--version", "1.0.0", "--provider", "codex", "--out", filepath.Join(t.TempDir(), "x.tgz")},
		"version":            {"build", "--source", goldenSource, "--pack-key", "fixture", "--version", "1", "--provider", "claude-code", "--out", filepath.Join(t.TempDir(), "x.tgz")},
		"source":             {"build", "--source", t.TempDir(), "--pack-key", "fixture", "--version", "1.0.0", "--provider", "claude-code", "--out", filepath.Join(t.TempDir(), "x.tgz")},
		"out dir":            {"build", "--source", goldenSource, "--pack-key", "fixture", "--version", "1.0.0", "--provider", "claude-code", "--out", filepath.Join(t.TempDir(), "missing", "x.tgz")},
		"force on directory": {"build", "--source", goldenSource, "--pack-key", "fixture", "--version", "1.0.0", "--provider", "claude-code", "--out", t.TempDir(), "--force"},
	} {
		t.Run(name, func(t *testing.T) {
			if result := invoke(args...); result.code != exitFailure || result.stdout != "" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestDigestRejectsInvalidArchivesWithReason(t *testing.T) {
	invalid := invoke("digest", "../../internal/agentpack/testdata/conformance/tar_type_symlink.tgz")
	if invalid.code != exitFailure || invalid.stdout != "" || !strings.Contains(invalid.stderr, "tar_type") {
		t.Fatalf("invalid = %+v", invalid)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing.tgz"), t.TempDir()} {
		if result := invoke("digest", path); result.code != exitFailure {
			t.Fatalf("%s = %+v", path, result)
		}
	}
}

func proofMessage(spki, purpose, organization string, expiresAt time.Time) []byte {
	return []byte(`{"actorUserId":"user-a","challengeId":"privileged_reauth_` + strings.Repeat("a", 64) +
		`","expiresAt":"` + expiresAt.UTC().Format("2006-01-02T15:04:05.000Z") + `","keyId":"release-2026","nonce":"` + strings.Repeat("b", 64) +
		`","notAfter":"2099-01-01T00:00:00.000Z","notBefore":"2026-01-01T00:00:00.000Z","organizationId":` + organization +
		`,"purpose":"` + purpose + `","scopeType":"ORGANIZATION","spkiSha256":"` + spki + `"}`)
}

func writeMessage(t *testing.T, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proof-message.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProveSignsTheCanonicalProofMessageBytes(t *testing.T) {
	keyPath, identity := keygen(t)
	spki := identity["spkiSha256"].(string)
	contents := proofMessage(spki, agentpack.SigningKeyProofPurpose, `"org-a"`, time.Now().Add(5*time.Minute))
	result := invoke("prove", "--key", keyPath, "--message-file", writeMessage(t, contents))
	if result.code != exitOK {
		t.Fatalf("prove = %+v", result)
	}
	assertNoSecretOutput(t, result, keyPath)
	output := decodeObject(t, result.stdout)
	if strings.Join(sortedKeys(output), ",") != "signatureBase64" {
		t.Fatalf("prove keys = %v", sortedKeys(output))
	}
	signature, _ := base64.StdEncoding.DecodeString(output["signatureBase64"].(string))
	der, _ := base64.StdEncoding.DecodeString(identity["publicKeySpkiBase64"].(string))
	parsed, _ := x509.ParsePKIXPublicKey(der)
	if !ed25519.Verify(parsed.(ed25519.PublicKey), contents, signature) {
		t.Fatal("proof signature does not verify")
	}
	missing := invoke("prove", "--key", keyPath, "--message-file", filepath.Join(t.TempDir(), "missing"))
	if missing.code != exitFailure {
		t.Fatalf("missing message = %+v", missing)
	}
}

func TestProveRefusesAnythingButACanonicalProofForTheOwnKey(t *testing.T) {
	keyPath, identity := keygen(t)
	spki := identity["spkiSha256"].(string)
	future := time.Now().Add(5 * time.Minute)
	valid := proofMessage(spki, agentpack.SigningKeyProofPurpose, `"org-a"`, future)
	forged := v1.SignedAgentPackAuthority{
		SchemaVersion: v1.AgentPackAuthoritySchemaVersion, RegistryVersionID: "unpublished",
		Scope: v1.AgentPackAuthorityScope{Kind: v1.AgentPackScopePlatform}, PackKey: "attacker-pack", PackName: "Attacker",
		Provider: "claude-code", ProtocolVersion: v1.ProtocolVersion, MinimumRunnerVersion: "0.1.0",
		AgentPack: v1.AgentPackRef{Version: "1.0.0", Digest: strings.Repeat("f", 64), URL: "https://evil.example/pack.tgz", KeyID: "platform-2026"},
	}
	forgedDigest, err := agentpack.PublicationAttestationDigest(forged)
	if err != nil {
		t.Fatal(err)
	}
	forgedBytes, _ := hex.DecodeString(forgedDigest)
	cases := map[string][]byte{
		"publication attestation digest": forgedBytes,
		"arbitrary text":                 []byte("hello"),
		"other purpose":                  proofMessage(spki, "shortstack.agent-pack.publication.v1", `"org-a"`, future),
		"other key":                      proofMessage(strings.Repeat("d", 64), agentpack.SigningKeyProofPurpose, `"org-a"`, future),
		"non-canonical whitespace":       []byte(strings.Replace(string(valid), `,"keyId"`, `, "keyId"`, 1)),
		"trailing newline":               append(append([]byte(nil), valid...), '\n'),
		"expired":                        proofMessage(spki, agentpack.SigningKeyProofPurpose, `"org-a"`, time.Now().Add(-time.Second)),
		"organization missing":           proofMessage(spki, agentpack.SigningKeyProofPurpose, `null`, future),
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			result := invoke("prove", "--key", keyPath, "--message-file", writeMessage(t, contents))
			if result.code != exitFailure || result.stdout != "" || !strings.Contains(result.stderr, "proof message") {
				t.Fatalf("result = %+v", result)
			}
			assertNoSecretOutput(t, result, keyPath)
		})
	}
}

func signArgs(keyPath, archive string, extra ...string) []string {
	return append([]string{
		"sign", "--key", keyPath, "--archive", archive, "--scope", "platform", "--pack-key", "fixture",
		"--pack-name", "Fixture <Pack> & \"Co\"", "--provider", "claude-code", "--protocol-version", "1",
		"--minimum-runner-version", "0.1.0", "--key-id", "platform-2026",
	}, extra...)
}

func TestSignProducesVerifiablePublishRequest(t *testing.T) {
	keyPath, identity := keygen(t)
	archive := buildGolden(t)
	result := invoke(signArgs(keyPath, archive, "--gateway-origin", "https://gateway.example.test")...)
	if result.code != exitOK {
		t.Fatalf("sign = %+v", result)
	}
	assertNoSecretOutput(t, result, keyPath)
	var request agentpack.PublishRequest
	decoder := json.NewDecoder(strings.NewReader(result.stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.AgentPack.URL != "https://gateway.example.test/v1/agent-packs/sha256/"+goldenDigest || request.AgentPack.Digest != goldenDigest ||
		request.AgentPack.Version != "1.0.0" || request.Scope.Kind != "platform" || request.PackName != "Fixture <Pack> & \"Co\"" {
		t.Fatalf("request = %+v", request)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(request.RequestID) {
		t.Fatalf("request id = %q", request.RequestID)
	}
	if strings.Contains(result.stdout, "\\"+"u003c") || !strings.Contains(result.stdout, "<Pack>") {
		t.Fatalf("pack name was HTML-escaped: %s", result.stdout)
	}
	authority := v1.SignedAgentPackAuthority{
		SchemaVersion: 1, RegistryVersionID: "r", Scope: v1.AgentPackAuthorityScope{Kind: "platform"},
		PackKey: request.PackKey, PackName: request.PackName, Provider: request.Provider, ProtocolVersion: request.ProtocolVersion,
		MinimumRunnerVersion: request.MinimumRunnerVersion, AgentPack: request.AgentPack, SignatureBase64: request.SignatureBase64,
	}
	authority.AttestationDigest, _ = agentpack.PublicationAttestationDigest(authority)
	if err := agentpack.VerifyDeliveryAuthority(authority, authority.AgentPack, identity["publicKeySpkiBase64"].(string), "claude", "0.1.0"); err != nil {
		t.Fatalf("sign output does not verify: %v", err)
	}
	digestBytes, _ := hex.DecodeString(authority.AttestationDigest)
	if len(digestBytes) != 32 {
		t.Fatal("attestation digest is not 32 bytes")
	}

	organization := invoke(signArgs(keyPath, archive, "--url", "https://packs.example.test/fixture.tgz", "--request-id", "publish-1")...)
	if organization.code != exitOK {
		t.Fatalf("explicit url = %+v", organization)
	}
	if output := decodeObject(t, organization.stdout); output["requestId"] != "publish-1" {
		t.Fatalf("request id = %v", output["requestId"])
	}
	scoped := invoke(append(signArgs(keyPath, archive, "--url", "https://packs.example.test/fixture.tgz"), "--scope", "organization", "--organization-id", "org-1")...)
	if scoped.code != exitOK || !strings.Contains(scoped.stdout, `"organizationId":"org-1"`) {
		t.Fatalf("organization scope = %+v", scoped)
	}
}

func TestSignRefusesManifestMismatchAndInvalidInputs(t *testing.T) {
	keyPath, _ := keygen(t)
	archive := buildGolden(t)
	cases := map[string][]string{
		"pack key":          append(signArgs(keyPath, archive, "--url", "https://p.test/a"), "--pack-key", "other"),
		"provider":          append(signArgs(keyPath, archive, "--url", "https://p.test/a"), "--provider", "codex"),
		"version":           append(signArgs(keyPath, archive, "--url", "https://p.test/a"), "--version", "2.0.0"),
		"origin with path":  signArgs(keyPath, archive, "--gateway-origin", "https://gateway.example.test/base"),
		"http url":          signArgs(keyPath, archive, "--url", "http://p.test/a"),
		"missing org":       append(signArgs(keyPath, archive, "--url", "https://p.test/a"), "--scope", "organization"),
		"platform with org": append(signArgs(keyPath, archive, "--url", "https://p.test/a"), "--organization-id", "org-1"),
		"bad request id":    append(signArgs(keyPath, archive, "--url", "https://p.test/a"), "--request-id", "../x"),
		"invalid archive":   signArgs(keyPath, "../../internal/agentpack/testdata/conformance/manifest_whitespace.tgz", "--url", "https://p.test/a"),
		"missing key":       signArgs(filepath.Join(t.TempDir(), "missing.pem"), archive, "--url", "https://p.test/a"),
		"protocol":          append(signArgs(keyPath, archive, "--url", "https://p.test/a"), "--protocol-version", "2"),
		"blank name":        append(signArgs(keyPath, archive, "--url", "https://p.test/a"), "--pack-name", "  "),
		"bom name":          append(signArgs(keyPath, archive, "--url", "https://p.test/a"), "--pack-name", "\ufeff"),
		"numeric url host":  signArgs(keyPath, archive, "--url", "https://1.2.3.256/a"),
		"url with query":    signArgs(keyPath, archive, "--url", "https://p.test/a?b=c"),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			result := invoke(args...)
			if result.code != exitFailure || result.stdout != "" || result.stderr == "" {
				t.Fatalf("result = %+v", result)
			}
			assertNoSecretOutput(t, result, keyPath)
		})
	}
	readable := filepath.Join(t.TempDir(), "readable.pem")
	raw, _ := os.ReadFile(keyPath)
	if err := os.WriteFile(readable, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		signArgs(readable, archive, "--url", "https://p.test/a"),
		{"public-key", "--key", readable},
		{"prove", "--key", readable, "--message-file", archive},
	} {
		result := invoke(args...)
		if result.code != exitFailure || result.stdout != "" || !strings.Contains(result.stderr, readable) {
			t.Fatalf("%v = %+v", args[0], result)
		}
		assertNoSecretOutput(t, result, readable)
	}
}

func TestBuiltBinaryIsDeterministicAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the CLI binary")
	}
	directory := t.TempDir()
	binary := filepath.Join(directory, "agentpack")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cli: %v\n%s", err, output)
	}
	digests := []string{}
	for _, name := range []string{"a.tgz", "b.tgz"} {
		command := exec.Command(binary, "build", "--source", goldenSource, "--pack-key", "fixture", "--version", "1.0.0", "--provider", "claude-code", "--out", filepath.Join(directory, name))
		output, err := command.Output()
		if err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
		digests = append(digests, decodeObject(t, string(output))["digest"].(string))
	}
	if digests[0] != goldenDigest || digests[1] != goldenDigest {
		t.Fatalf("digests = %v", digests)
	}
	first, _ := os.ReadFile(filepath.Join(directory, "a.tgz"))
	second, _ := os.ReadFile(filepath.Join(directory, "b.tgz"))
	if !bytes.Equal(first, second) {
		t.Fatal("archives differ across processes")
	}
	unknown := exec.Command(binary, "build", "--unknown")
	var stderr bytes.Buffer
	unknown.Stderr = &stderr
	err := unknown.Run()
	var exitError *exec.ExitError
	if err == nil || !errors.As(err, &exitError) || exitError.ExitCode() != exitUsage {
		t.Fatalf("unknown flag exit = %v", err)
	}
	keyPath := filepath.Join(directory, "keys", "k.pem")
	keygenOutput, err := exec.Command(binary, "keygen", "--out", keyPath).CombinedOutput()
	if err != nil || strings.Contains(string(keygenOutput), "PRIVATE") {
		t.Fatalf("keygen: %v %q", err, keygenOutput)
	}
	signOutput, err := exec.Command(binary, signArgs(keyPath, filepath.Join(directory, "a.tgz"), "--gateway-origin", "https://gateway.example.test")...).CombinedOutput()
	if err != nil || strings.Contains(string(signOutput), "PRIVATE") || !strings.Contains(string(signOutput), goldenDigest) {
		t.Fatalf("sign: %v %q", err, signOutput)
	}
}

package remoteconfig

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const testRunnerID = "runner_0f5e7c1a-1111-4d4d-9999-123456789abc"
const testJobImage = "ghcr.io/example/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type testPackKey struct {
	id          string
	spkiBase64  string
	fingerprint string
}

func newTestPackKey(t *testing.T, id string) testPackKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return testPackKey{id: id, spkiBase64: base64.StdEncoding.EncodeToString(der), fingerprint: hex.EncodeToString(sum[:])}
}

func dockerConfig(keys ...testPackKey) map[string]any {
	publicKeys := map[string]any{}
	for _, key := range keys {
		publicKeys[key.id] = key.spkiBase64
	}
	return map[string]any{
		"isolation":     "docker",
		"job_image":     testJobImage,
		"job_network":   "none",
		"job_memory_mb": 4096,
		"job_cpus":      2,
		"job_pids":      512,
		"provider":      "claude",
		"labels":        map[string]any{"pool": "eu-linux"},
		"repositories": map[string]any{
			"repo-1": map[string]any{"url": "https://github.com/acme/api.git"},
			"repo-2": map[string]any{"url": "https://github.com/acme/web.git"},
		},
		"agent_pack_public_keys":           publicKeys,
		"source_publication_installations": map[string]any{"installation-1": map[string]any{"provider": "github"}},
	}
}

func directConfig() map[string]any {
	return map[string]any{
		"isolation":                        "direct",
		"allow_unsafe_direct":              true,
		"provider":                         "codex",
		"labels":                           map[string]any{},
		"repositories":                     map[string]any{},
		"agent_pack_public_keys":           map[string]any{},
		"source_publication_installations": map[string]any{},
	}
}

func deliveryBody(config map[string]any, keys ...testPackKey) map[string]any {
	fingerprints := map[string]any{}
	for _, key := range keys {
		fingerprints[key.id] = key.fingerprint
	}
	return map[string]any{
		"schema_version":        1,
		"runner_id":             testRunnerID,
		"profile_revision":      3,
		"digest":                strings.Repeat("d", 64),
		"config":                config,
		"pack_key_fingerprints": fingerprints,
		"omitted_repositories":  []any{map[string]any{"id": "repo-3", "reason": "clone_url_unavailable"}},
	}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

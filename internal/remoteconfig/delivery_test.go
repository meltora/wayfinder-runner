package remoteconfig

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseDeliveryAcceptsDockerDocument(t *testing.T) {
	key := newTestPackKey(t, "pack-key-1")
	delivery, err := ParseDelivery(encode(t, deliveryBody(dockerConfig(key), key)), testRunnerID)
	if err != nil {
		t.Fatal(err)
	}
	remote := delivery.Remote
	if delivery.RunnerID != testRunnerID || delivery.ProfileRevision != 3 || delivery.Digest != strings.Repeat("d", 64) {
		t.Fatalf("envelope = %+v", delivery)
	}
	if remote.Isolation != "docker" || remote.AllowUnsafeDirect || remote.Provider != "claude" || remote.JobImage != testJobImage ||
		remote.JobNetwork != "none" || remote.JobMemoryMB != 4096 || remote.JobCPUs != 2 || remote.JobPIDs != 512 {
		t.Fatalf("remote = %+v", remote)
	}
	if remote.Labels["pool"] != "eu-linux" || len(remote.Repositories) != 2 || remote.Repositories["repo-1"] != "https://github.com/acme/api.git" {
		t.Fatalf("remote maps = %+v", remote)
	}
	if remote.SourcePublicationInstallations["installation-1"] != "github" {
		t.Fatalf("installations = %+v", remote.SourcePublicationInstallations)
	}
	if len(delivery.PackKeys) != 1 || delivery.PackKeys[0] != (PackKey{ID: "pack-key-1", Fingerprint: key.fingerprint, PublicKey: key.spkiBase64}) {
		t.Fatalf("pack keys = %+v", delivery.PackKeys)
	}
	if len(delivery.OmittedRepositories) != 1 || delivery.OmittedRepositories[0].ID != "repo-3" {
		t.Fatalf("omitted = %+v", delivery.OmittedRepositories)
	}
}

func TestParseDeliveryAcceptsDirectAndEmptyDocuments(t *testing.T) {
	delivery, err := ParseDelivery(encode(t, deliveryBody(directConfig())), testRunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Remote.Isolation != "direct" || !delivery.Remote.AllowUnsafeDirect || len(delivery.PackKeys) != 0 || len(delivery.Remote.Repositories) != 0 {
		t.Fatalf("direct = %+v", delivery)
	}
}

func TestParseDeliveryRejectsHostOwnedAndUnknownKeys(t *testing.T) {
	for _, name := range []string{"state_dir", "control_plane_url", "provider_environment", "provider_binaries", "token_file", "supervisor", "checks", "Isolation", "unknown"} {
		config := dockerConfig()
		config[name] = "x"
		if _, err := ParseDelivery(encode(t, deliveryBody(config)), testRunnerID); err == nil {
			t.Fatalf("accepted remote key %s", name)
		}
	}
	for _, mutate := range []func(map[string]any){
		func(c map[string]any) {
			c["repositories"] = map[string]any{"repo-1": map[string]any{"url": "https://github.com/a/b.git", "token_file": "/x"}}
		},
		func(c map[string]any) {
			c["source_publication_installations"] = map[string]any{"i": map[string]any{"provider": "github", "token_file": "/secret"}}
		},
		func(c map[string]any) {
			c["source_publication_installations"] = map[string]any{"i": map[string]any{"provider": "gitlab"}}
		},
		func(c map[string]any) {
			c["repositories"] = map[string]any{"repo-1": map[string]any{"URL": "https://github.com/a/b.git"}}
		},
		func(c map[string]any) {
			c["repositories"] = map[string]any{"repo-1": map[string]any{"url": "/srv/local/repo"}}
		},
		func(c map[string]any) {
			c["repositories"] = map[string]any{"repo-1": map[string]any{"url": "file:///srv/repo"}}
		},
		func(c map[string]any) { c["allow_unsafe_direct"] = true },
		func(c map[string]any) { delete(c, "job_image") },
		func(c map[string]any) { delete(c, "repositories") },
		func(c map[string]any) { c["isolation"] = "firecracker" },
		func(c map[string]any) { c["job_memory_mb"] = "4096" },
		func(c map[string]any) { c["labels"] = map[string]any{"pool": 1} },
	} {
		config := dockerConfig()
		mutate(config)
		if _, err := ParseDelivery(encode(t, deliveryBody(config)), testRunnerID); err == nil {
			t.Fatalf("accepted invalid remote document %v", config)
		}
	}
	direct := directConfig()
	direct["job_image"] = testJobImage
	if _, err := ParseDelivery(encode(t, deliveryBody(direct)), testRunnerID); err == nil {
		t.Fatal("accepted direct document with a job image")
	}
	direct = directConfig()
	delete(direct, "allow_unsafe_direct")
	if _, err := ParseDelivery(encode(t, deliveryBody(direct)), testRunnerID); err == nil {
		t.Fatal("accepted direct document without allow_unsafe_direct")
	}
}

func TestParseDeliveryRejectsMalformedEnvelopes(t *testing.T) {
	key := newTestPackKey(t, "pack-key-1")
	other := newTestPackKey(t, "pack-key-2")
	valid := func() map[string]any { return deliveryBody(dockerConfig(key), key) }
	cases := map[string]func(map[string]any){
		"unknown field":        func(b map[string]any) { b["organization_id"] = "org" },
		"schema version":       func(b map[string]any) { b["schema_version"] = 2 },
		"other runner":         func(b map[string]any) { b["runner_id"] = "runner-other" },
		"revision":             func(b map[string]any) { b["profile_revision"] = 0 },
		"digest":               func(b map[string]any) { b["digest"] = "XYZ" },
		"missing fingerprints": func(b map[string]any) { delete(b, "pack_key_fingerprints") },
		"fingerprint mismatch": func(b map[string]any) { b["pack_key_fingerprints"] = map[string]any{"pack-key-1": other.fingerprint} },
		"fingerprint extra key": func(b map[string]any) {
			b["pack_key_fingerprints"] = map[string]any{"pack-key-1": key.fingerprint, "pack-key-2": other.fingerprint}
		},
		"fingerprint missing": func(b map[string]any) { b["pack_key_fingerprints"] = map[string]any{} },
		"omitted reason":      func(b map[string]any) { b["omitted_repositories"] = []any{map[string]any{"id": "r", "reason": "x"}} },
		"omitted extra": func(b map[string]any) {
			b["omitted_repositories"] = []any{map[string]any{"id": "r", "reason": "clone_url_unavailable", "url": "x"}}
		},
		"not ed25519": func(b map[string]any) {
			b["config"].(map[string]any)["agent_pack_public_keys"] = map[string]any{"pack-key-1": "AAAA"}
		},
	}
	for name, mutate := range cases {
		body := valid()
		mutate(body)
		if _, err := ParseDelivery(encode(t, body), testRunnerID); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	for name, raw := range map[string]string{
		"duplicate key":   `{"schema_version":1,"schema_version":1}`,
		"trailing":        string(encode(t, valid())) + `{}`,
		"not object":      `[]`,
		"empty":           ``,
		"nested dup keys": strings.Replace(string(encode(t, valid())), `"isolation":"docker"`, `"isolation":"docker","isolation":"direct"`, 1),
	} {
		if _, err := ParseDelivery([]byte(raw), testRunnerID); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if _, err := ParseDelivery(encode(t, valid()), testRunnerID); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}
}

func TestParseDeliveryAcceptsOnlyTheKnownOmissionReasons(t *testing.T) {
	withReasons := func(reasons ...string) map[string]any {
		body := deliveryBody(directConfig())
		entries := make([]any, 0, len(reasons))
		for index, reason := range reasons {
			entries = append(entries, map[string]any{"id": "repo-" + string(rune('a'+index)), "reason": reason})
		}
		body["omitted_repositories"] = entries
		return body
	}
	delivery, err := ParseDelivery(encode(t, withReasons("clone_url_unavailable", "installation_inactive")), testRunnerID)
	if err != nil {
		t.Fatal(err)
	}
	want := []OmittedRepository{{ID: "repo-a", Reason: "clone_url_unavailable"}, {ID: "repo-b", Reason: "installation_inactive"}}
	if len(delivery.OmittedRepositories) != 2 || delivery.OmittedRepositories[0] != want[0] || delivery.OmittedRepositories[1] != want[1] {
		t.Fatalf("omitted = %+v", delivery.OmittedRepositories)
	}
	for _, reason := range []string{"installation_suspended", "", "INSTALLATION_INACTIVE", "installation_inactive "} {
		_, err := ParseDelivery(encode(t, withReasons(reason)), testRunnerID)
		if err == nil || !strings.HasSuffix(err.Error(), "omitted_repositories entry is invalid") {
			t.Fatalf("reason %q: err = %v", reason, err)
		}
	}
}

func revokedTestDigests(count int) []any {
	out := make([]any, 0, count)
	for index := range count {
		out = append(out, fmt.Sprintf("%064x", index+1))
	}
	return out
}

func TestParseDeliveryReadsRevokedAgentPackDigests(t *testing.T) {
	delivery, err := ParseDelivery(encode(t, deliveryBody(dockerConfig())), testRunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Remote.RevokedAgentPackDigests != nil {
		t.Fatalf("absent key parsed as %v", delivery.Remote.RevokedAgentPackDigests)
	}
	for _, digests := range [][]any{{}, revokedTestDigests(1), revokedTestDigests(256)} {
		config := dockerConfig()
		config["revoked_agent_pack_digests"] = digests
		delivery, err := ParseDelivery(encode(t, deliveryBody(config)), testRunnerID)
		if err != nil {
			t.Fatalf("%d digests: %v", len(digests), err)
		}
		if delivery.Remote.RevokedAgentPackDigests == nil || len(delivery.Remote.RevokedAgentPackDigests) != len(digests) {
			t.Fatalf("parsed %v", delivery.Remote.RevokedAgentPackDigests)
		}
		for index, digest := range digests {
			if delivery.Remote.RevokedAgentPackDigests[index] != digest {
				t.Fatalf("digest %d = %q", index, delivery.Remote.RevokedAgentPackDigests[index])
			}
		}
	}
}

func TestParseDeliveryRejectsMalformedRevokedAgentPackDigests(t *testing.T) {
	first, second := revokedTestDigests(2)[0], revokedTestDigests(2)[1]
	for name, value := range map[string]any{
		"unsorted":      []any{second, first},
		"duplicate":     []any{first, first},
		"uppercase":     []any{strings.ToUpper(strings.Repeat("a", 64))},
		"short":         []any{strings.Repeat("a", 63)},
		"prefixed":      []any{"sha256:" + strings.Repeat("a", 64)},
		"257 entries":   revokedTestDigests(257),
		"string":        first,
		"object":        map[string]any{"digest": first},
		"null":          nil,
		"number entry":  []any{1},
		"null entry":    []any{nil},
		"nested arrays": []any{[]any{first}},
	} {
		config := dockerConfig()
		config["revoked_agent_pack_digests"] = value
		if _, err := ParseDelivery(encode(t, deliveryBody(config)), testRunnerID); err == nil {
			t.Fatalf("accepted %s revoked_agent_pack_digests", name)
		}
	}
}

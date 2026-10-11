package capability

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestCollectAdvertisesThePlatformTrustChain(t *testing.T) {
	rootA, rootB := strings.Repeat("b", 64), strings.Repeat("a", 64)
	d := fakeDetector().WithAgentPackTrust([]string{rootA, rootB, rootA}, []string{"pack-key-2", "pack-key-1", "pack-key-2"})
	m := d.Collect(context.Background())
	if m.AgentPackTrustChainVersion != "1" {
		t.Fatalf("trust chain version = %q", m.AgentPackTrustChainVersion)
	}
	if len(m.AgentPackTrustRootKeyIDs) != 2 || m.AgentPackTrustRootKeyIDs[0] != rootB || m.AgentPackTrustRootKeyIDs[1] != rootA {
		t.Fatalf("root key ids = %v", m.AgentPackTrustRootKeyIDs)
	}
	if len(m.TrustedAgentPackKeyIDs) != 2 || m.TrustedAgentPackKeyIDs[0] != "pack-key-1" || m.TrustedAgentPackKeyIDs[1] != "pack-key-2" {
		t.Fatalf("trusted key ids = %v", m.TrustedAgentPackKeyIDs)
	}
	m.Signature = ""
	payload, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	want := `"trusted_agent_pack_key_ids":["pack-key-1","pack-key-2"],"agent_pack_trust_chain_version":"1","agent_pack_trust_root_key_ids":["` + rootB + `","` + rootA + `"]`
	if !strings.Contains(string(payload), want) {
		t.Fatalf("trust chain fields are not encoded in protocol order:\n%s", payload)
	}
}

func TestCollectWithoutRootsStillAdvertisesTheTrustChainVersion(t *testing.T) {
	m := fakeDetector().WithAgentPackTrust(nil, nil).Collect(context.Background())
	if m.AgentPackTrustChainVersion != "1" || m.AgentPackTrustRootKeyIDs != nil || m.TrustedAgentPackKeyIDs == nil {
		t.Fatalf("manifest = %+v", m)
	}
	legacy := fakeDetector().Collect(context.Background())
	if legacy.AgentPackTrustChainVersion != "" || legacy.AgentPackTrustRootKeyIDs != nil {
		t.Fatalf("a detector without trust advertised the trust chain: %+v", legacy)
	}
}

func TestCapabilitySignatureCoversTheTrustRoots(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	d := fakeDetector().WithAgentPackTrust([]string{strings.Repeat("c", 64)}, nil)
	m := d.Collect(context.Background())
	if err := d.Sign(&m, key); err != nil {
		t.Fatal(err)
	}
	signature, _ := base64.StdEncoding.DecodeString(m.Signature)
	m.Signature = ""
	m.AgentPackTrustRootKeyIDs = []string{strings.Repeat("d", 64)}
	forged, _ := json.Marshal(&m)
	digest := sha256.Sum256(forged)
	if ecdsa.VerifyASN1(&key.PublicKey, digest[:], signature) {
		t.Fatal("the signature does not cover agent_pack_trust_root_key_ids")
	}
}

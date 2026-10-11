package trustset

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack/trustroot"
)

type memorySigner struct {
	private ed25519.PrivateKey
}

func (signer memorySigner) Sign(_ context.Context, message []byte) ([]byte, error) {
	return ed25519.Sign(signer.private, message), nil
}

func (signer memorySigner) PublicKeySPKI() ([]byte, error) {
	return x509.MarshalPKIXPublicKey(signer.private.Public())
}

type testRootKey struct {
	signer memorySigner
	root   trustroot.Root
}

func newTestRootKey(t *testing.T) testRootKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return testRootKey{
		signer: memorySigner{private: private},
		root:   trustroot.Root{KeyID: hex.EncodeToString(sum[:]), PublicKeySPKIBase64: base64.StdEncoding.EncodeToString(der), PublicKey: public},
	}
}

func rootsOf(channel string, keys ...testRootKey) trustroot.Roots {
	roots := trustroot.Roots{Channel: channel, Source: trustroot.SourceEmbedded}
	for _, key := range keys {
		roots.Keys = append(roots.Keys, key.root)
	}
	return roots
}

func testDocument(t *testing.T, sequence int64, keys map[string]string, revoked ...string) Document {
	t.Helper()
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	document := Document{Format: Format, Sequence: sequence, IssuedAt: notBefore, RevokedKeyIDs: append([]string{}, revoked...)}
	for id, public := range keys {
		document.Keys = append(document.Keys, Key{KeyID: id, PublicKeySPKIBase64: public, NotBefore: notBefore})
	}
	normalized, err := ParseInput(document.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func signTestDocument(t *testing.T, root testRootKey, document Document) v1.AgentPackTrustedKeySet {
	t.Helper()
	signed, err := Sign(context.Background(), root.signer, document)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestSignedSetVerifiesAgainstItsRoot(t *testing.T) {
	root := newTestRootKey(t)
	packKey := generatePublicKey(t)
	signed := signTestDocument(t, root, testDocument(t, 4, map[string]string{"platform-a": packKey}))
	if signed.RootKeyID != root.root.KeyID {
		t.Fatalf("root key id = %s", signed.RootKeyID)
	}
	verified, err := Verify(signed, rootsOf(trustroot.ChannelProduction, root))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Document.Sequence != 4 || verified.RootKeyID != root.root.KeyID || len(verified.DocumentSHA256) != 64 {
		t.Fatalf("verified = %+v", verified)
	}
	document, _ := base64.StdEncoding.DecodeString(signed.DocumentBase64)
	sum := sha256.Sum256(document)
	if verified.DocumentSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("document digest does not cover the signed document bytes")
	}
}

func TestSigningIsDeterministic(t *testing.T) {
	root := newTestRootKey(t)
	document := testDocument(t, 1, map[string]string{"platform-a": generatePublicKey(t), "platform-b": generatePublicKey(t)})
	first := signTestDocument(t, root, document)
	second := signTestDocument(t, root, document)
	if first != second {
		t.Fatal("signing the same document twice produced different output")
	}
}

func TestSetSignedByAnotherChannelRootIsRefused(t *testing.T) {
	develop, production := newTestRootKey(t), newTestRootKey(t)
	signed := signTestDocument(t, develop, testDocument(t, 1, map[string]string{"platform-a": generatePublicKey(t)}))
	_, err := Verify(signed, rootsOf(trustroot.ChannelProduction, production))
	if CodeOf(err) != CodeUnknownRoot {
		t.Fatalf("production runner accepted a develop-signed set: %v", err)
	}
}

func TestSetWithAForgedRootKeyIDIsRefused(t *testing.T) {
	trusted, attacker := newTestRootKey(t), newTestRootKey(t)
	signed := signTestDocument(t, attacker, testDocument(t, 1, map[string]string{"platform-a": generatePublicKey(t)}))
	signed.RootKeyID = trusted.root.KeyID
	if _, err := Verify(signed, rootsOf(trustroot.ChannelProduction, trusted)); CodeOf(err) != CodeSignatureInvalid {
		t.Fatalf("forged root key id = %v", err)
	}
}

func TestTamperedSetIsRefused(t *testing.T) {
	root := newTestRootKey(t)
	document := testDocument(t, 1, map[string]string{"platform-a": generatePublicKey(t)})
	signed := signTestDocument(t, root, document)
	tampered := document
	tampered.Sequence = 2
	signed.DocumentBase64 = base64.StdEncoding.EncodeToString(tampered.Canonical())
	if _, err := Verify(signed, rootsOf(trustroot.ChannelDevelopment, root)); CodeOf(err) != CodeSignatureInvalid {
		t.Fatalf("tampered set = %v", err)
	}
}

func TestSignatureWithoutTheDomainPrefixIsRefused(t *testing.T) {
	root := newTestRootKey(t)
	document := testDocument(t, 1, map[string]string{"platform-a": generatePublicKey(t)})
	signed := signTestDocument(t, root, document)
	signed.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(root.signer.private, document.Canonical()))
	if _, err := Verify(signed, rootsOf(trustroot.ChannelDevelopment, root)); CodeOf(err) != CodeSignatureInvalid {
		t.Fatalf("undomained signature = %v", err)
	}
}

func TestSignedNonCanonicalDocumentIsRefused(t *testing.T) {
	root := newTestRootKey(t)
	raw := []byte(fmt.Sprintf(`{"format":1, "issuedAt":"2026-01-01T00:00:00Z","keys":[{"keyId":"a","notBefore":"2026-01-01T00:00:00Z","publicKeySpkiBase64":"%s"}],"revokedKeyIds":[],"sequence":1}`, generatePublicKey(t)))
	signature := ed25519.Sign(root.signer.private, SigningMessage(raw))
	signed := v1.AgentPackTrustedKeySet{
		DocumentBase64: base64.StdEncoding.EncodeToString(raw), SignatureBase64: base64.StdEncoding.EncodeToString(signature), RootKeyID: root.root.KeyID,
	}
	if _, err := Verify(signed, rootsOf(trustroot.ChannelDevelopment, root)); CodeOf(err) != CodeNonCanonical {
		t.Fatalf("signed non-canonical document = %v", err)
	}
}

func TestMalformedEnvelopesAreRefused(t *testing.T) {
	root := newTestRootKey(t)
	valid := signTestDocument(t, root, testDocument(t, 1, map[string]string{"platform-a": generatePublicKey(t)}))
	roots := rootsOf(trustroot.ChannelDevelopment, root)
	cases := map[string]func(*v1.AgentPackTrustedKeySet){
		"uppercase root id":   func(set *v1.AgentPackTrustedKeySet) { set.RootKeyID = "AB" + set.RootKeyID[2:] },
		"short root id":       func(set *v1.AgentPackTrustedKeySet) { set.RootKeyID = set.RootKeyID[:10] },
		"document not base64": func(set *v1.AgentPackTrustedKeySet) { set.DocumentBase64 = "%%%" },
		"document base64 unpadded": func(set *v1.AgentPackTrustedKeySet) {
			set.DocumentBase64 = base64.RawStdEncoding.EncodeToString([]byte("{}a"))
		},
		"empty document": func(set *v1.AgentPackTrustedKeySet) { set.DocumentBase64 = "" },
		"short signature": func(set *v1.AgentPackTrustedKeySet) {
			set.SignatureBase64 = base64.StdEncoding.EncodeToString([]byte("short"))
		},
		"signature garbage": func(set *v1.AgentPackTrustedKeySet) { set.SignatureBase64 = "not base64" },
	}
	for name, mutate := range cases {
		signed := valid
		mutate(&signed)
		if _, err := Verify(signed, roots); err == nil {
			t.Fatalf("%s: envelope was accepted", name)
		}
	}
	oversized := valid
	oversized.DocumentBase64 = base64.StdEncoding.EncodeToString(make([]byte, MaxDocumentBytes+1))
	if _, err := Verify(oversized, roots); CodeOf(err) != CodeTooLarge {
		t.Fatalf("oversized envelope = %v", err)
	}
}

func TestVerifyWithoutRootsFailsClosed(t *testing.T) {
	root := newTestRootKey(t)
	signed := signTestDocument(t, root, testDocument(t, 1, map[string]string{"platform-a": generatePublicKey(t)}))
	if _, err := Verify(signed, trustroot.Roots{Channel: trustroot.ChannelProduction}); CodeOf(err) != CodeUnknownRoot {
		t.Fatalf("verification without embedded roots = %v", err)
	}
}

func TestTrustedKeyLookup(t *testing.T) {
	root := newTestRootKey(t)
	active, expired, future := generatePublicKey(t), generatePublicKey(t), generatePublicKey(t)
	document := testDocument(t, 2, map[string]string{"active": active}, "revoked-key")
	notAfter := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	document.Keys = append(document.Keys,
		Key{KeyID: "expired", PublicKeySPKIBase64: expired, NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: &notAfter},
		Key{KeyID: "future", PublicKeySPKIBase64: future, NotBefore: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)},
	)
	document, err := ParseInput(document.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(signTestDocument(t, root, document), rootsOf(trustroot.ChannelDevelopment, root))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	key, err := verified.TrustedKey("active", now)
	if err != nil || key.PublicKeySPKIBase64 != active {
		t.Fatalf("active key = %+v, %v", key, err)
	}
	for id, want := range map[string]error{"revoked-key": ErrKeyRevoked, "missing": ErrKeyAbsent, "expired": ErrKeyOutsideValidity, "future": ErrKeyOutsideValidity} {
		if _, err := verified.TrustedKey(id, now); !errors.Is(err, want) {
			t.Fatalf("%s lookup = %v, want %v", id, err, want)
		}
	}
	if _, err := verified.TrustedKey("expired", notAfter); !errors.Is(err, ErrKeyOutsideValidity) {
		t.Fatal("notAfter must be exclusive")
	}
	if _, err := verified.TrustedKey("expired", notAfter.Add(-time.Second)); err != nil {
		t.Fatalf("key inside its window = %v", err)
	}
	ids := verified.TrustedKeyIDs(now)
	if len(ids) != 1 || ids[0] != "active" {
		t.Fatalf("currently trusted ids = %v", ids)
	}
}

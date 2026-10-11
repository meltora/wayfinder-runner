package trustset

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type conformanceCase struct {
	Name          string `json:"name"`
	Document      string `json:"document"`
	Lenient       string `json:"lenient"`
	Canonical     string `json:"canonical"`
	CanonicalForm string `json:"canonicalForm"`
}

func loadConformanceCases(t *testing.T) []conformanceCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/trust-set-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []conformanceCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) < 30 {
		t.Fatalf("conformance corpus has only %d cases", len(corpus.Cases))
	}
	return corpus.Cases
}

func expectOutcome(t *testing.T, name string, err error, want string) {
	t.Helper()
	if want == "ok" {
		if err != nil {
			t.Fatalf("%s: unexpected refusal %v", name, err)
		}
		return
	}
	if got := CodeOf(err); got != want {
		t.Fatalf("%s: code = %q (%v), want %q", name, got, err, want)
	}
}

func TestConformanceCorpusLenientParsing(t *testing.T) {
	for _, tc := range loadConformanceCases(t) {
		document, err := ParseInput([]byte(tc.Document))
		expectOutcome(t, tc.Name, err, tc.Lenient)
		if err == nil && string(document.Canonical()) != tc.CanonicalForm {
			t.Fatalf("%s: canonical form = %s, want %s", tc.Name, document.Canonical(), tc.CanonicalForm)
		}
	}
}

func TestConformanceCorpusCanonicalParsing(t *testing.T) {
	for _, tc := range loadConformanceCases(t) {
		document, err := ParseCanonical([]byte(tc.Document))
		expectOutcome(t, tc.Name, err, tc.Canonical)
		if err == nil && string(document.Canonical()) != tc.Document {
			t.Fatalf("%s: canonical document did not round-trip", tc.Name)
		}
	}
}

func generatePublicKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

func TestSixtyFourDistinctKeysAreAccepted(t *testing.T) {
	keys := make([]string, 0, MaxKeys)
	for index := range MaxKeys {
		keys = append(keys, fmt.Sprintf(`{"keyId":"k%02d","notBefore":"2026-01-01T00:00:00Z","publicKeySpkiBase64":"%s"}`, index, generatePublicKey(t)))
	}
	raw := `{"format":1,"issuedAt":"2026-01-01T00:00:00Z","keys":[` + strings.Join(keys, ",") + `],"revokedKeyIds":[],"sequence":1}`
	document, err := ParseCanonical([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Keys) != MaxKeys {
		t.Fatalf("keys = %d", len(document.Keys))
	}
}

func TestDocumentLargerThanTheLimitIsRefused(t *testing.T) {
	raw := `{"format":1,"issuedAt":"2026-01-01T00:00:00Z","keys":[],"revokedKeyIds":[],"sequence":1}` + strings.Repeat(" ", MaxDocumentBytes)
	if _, err := ParseInput([]byte(raw)); CodeOf(err) != CodeTooLarge {
		t.Fatalf("oversized document = %v", err)
	}
}

func TestCanonicalEncodingIsByteIdenticalForTheSameInput(t *testing.T) {
	cases := loadConformanceCases(t)
	first, err := ParseInput([]byte(cases[1].Document))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseInput([]byte(cases[2].Document))
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Canonical()) != string(second.Canonical()) {
		t.Fatal("equivalent inputs produced different canonical documents")
	}
}

func TestParsedDocumentExposesValuesAndValidity(t *testing.T) {
	cases := loadConformanceCases(t)
	document, err := ParseCanonical([]byte(cases[0].Document))
	if err != nil {
		t.Fatal(err)
	}
	if document.Format != Format || document.Sequence != 3 || !document.IssuedAt.Equal(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("document = %+v", document)
	}
	if len(document.Keys) != 2 || document.Keys[0].KeyID != "platform-a" || document.Keys[0].NotAfter == nil || document.Keys[1].NotAfter != nil {
		t.Fatalf("keys = %+v", document.Keys)
	}
	if len(document.RevokedKeyIDs) != 1 || document.RevokedKeyIDs[0] != "platform-old" {
		t.Fatalf("revoked = %v", document.RevokedKeyIDs)
	}
}

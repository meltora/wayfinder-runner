package v1

import (
	"encoding/json"
	"testing"
)

func TestCommandContentDigestIsCanonicalForObjectKeyOrder(t *testing.T) {
	left, err := CommandContentDigest(map[string]any{"command": "resume_runner", "reason": "ready"})
	if err != nil {
		t.Fatal(err)
	}
	right, err := CommandContentDigest(map[string]any{"reason": "ready", "command": "resume_runner"})
	if err != nil {
		t.Fatal(err)
	}
	if left != right || len(left) != 64 {
		t.Fatalf("digests are not stable: %q != %q", left, right)
	}
}

func TestCommandContentDigestRejectsUnencodableContent(t *testing.T) {
	if _, err := CommandContentDigest(map[string]any{"command": func() {}}); err == nil {
		t.Fatal("unencodable command content was accepted")
	}
}

func TestCommandContentDigestMatchesTypedAndDecodedWireRepresentations(t *testing.T) {
	typed := CommandPayload{Command: CmdRenewLease, AttemptID: "attempt-1", FencingToken: 7}
	raw, err := json.Marshal(typed)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	typedDigest, err := CommandContentDigest(typed)
	if err != nil {
		t.Fatal(err)
	}
	decodedDigest, err := CommandContentDigest(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if typedDigest != decodedDigest {
		t.Fatalf("typed and decoded wire digests differ: %q != %q", typedDigest, decodedDigest)
	}
}

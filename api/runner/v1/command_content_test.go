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

func TestCommandContentDigestMatchesJavaScriptStringifyForHTMLAndLineSeparators(t *testing.T) {
	for _, test := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"b": "Goodbye, <name> & \u2028 \u2029!", "a": 1}, "2f4316aa637d353b5203d13b17db4304675adf55b526ebdeb9ae5f2ced569f04"},
		{map[string]any{"s": "\\u2028 literal"}, "9bb546f760b61402adeb8d4f7b9c121bb25b5e7faa115c211a05ea8d6089da10"},
	} {
		got, err := CommandContentDigest(test.payload)
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("digest = %s, want %s", got, test.want)
		}
	}
}

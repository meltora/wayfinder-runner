package daemon_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"

	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/mockcp"
)

func (f *rotationFixture) currentCertFingerprint() string {
	f.t.Helper()
	certPEM, _, err := identity.NewStore(f.stateDir).LoadCert()
	if err != nil {
		f.t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		f.t.Fatal("current certificate is not PEM")
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

func TestReplayAfterCertificateRotationSendsJournaledBytesOverTheNewCertificate(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	f := &rotationFixture{t: t, cp: cp, stateDir: stateDir, logs: &logBuffer{}}
	certificateA := f.currentCertFingerprint()
	d := f.daemon(0)

	const eventID = "evt-replay-across-rotation"
	journaled, err := d.JournalEventForTest(context.Background(), eventID, map[string]any{
		"kind": "progress", "fencing_token": uint64(9007199254740993), "note": "<a&b>",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.TestRotate(ctx); err != nil {
		t.Fatalf("rotation: %v", err)
	}
	certificateB := f.currentCertFingerprint()
	if certificateB == certificateA {
		t.Fatal("rotation did not switch the client certificate")
	}
	if cp.Activations() != 0 || len(cp.ReceivedEvents()) != 0 {
		t.Fatalf("test setup: activations=%d events=%d before the reconnect", cp.Activations(), len(cp.ReceivedEvents()))
	}

	runDaemon(t, d)
	var replayed []mockcp.ReceivedEvent
	waitFor(t, "replayed event", 15*time.Second, func() bool {
		replayed = replayed[:0]
		for _, event := range cp.ReceivedEvents() {
			if event.ID == eventID {
				replayed = append(replayed, event)
			}
		}
		return len(replayed) > 0
	})
	if len(replayed) != 1 {
		t.Fatalf("event delivered %d times", len(replayed))
	}
	got := replayed[0]
	if !bytes.Equal(got.Raw, journaled) {
		t.Fatalf("replayed bytes differ:\n got %s\nwant %s", got.Raw, journaled)
	}
	var wantFields, gotFields struct {
		ID     string          `json:"id"`
		SentAt json.RawMessage `json:"sent_at"`
	}
	if err := json.Unmarshal(journaled, &wantFields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got.Raw, &gotFields); err != nil {
		t.Fatal(err)
	}
	if gotFields.ID != eventID || wantFields.ID != eventID || !bytes.Equal(gotFields.SentAt, wantFields.SentAt) {
		t.Fatalf("id/sent_at = %s/%s, want %s/%s", gotFields.ID, gotFields.SentAt, wantFields.ID, wantFields.SentAt)
	}
	if got.PeerFingerprint != certificateB {
		t.Fatalf("replay presented certificate %s, want rotated %s (old %s)", got.PeerFingerprint, certificateB, certificateA)
	}
	if cp.Activations() != 1 {
		t.Fatalf("activations = %d, want the rotated certificate activated by the reconnect", cp.Activations())
	}
}

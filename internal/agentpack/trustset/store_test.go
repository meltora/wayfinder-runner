package trustset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/example/app-runner/internal/agentpack/trustroot"
)

func TestStoreAppliesOnlyStrictlyHigherSequences(t *testing.T) {
	root := newTestRootKey(t)
	roots := rootsOf(trustroot.ChannelDevelopment, root)
	stateDir := t.TempDir()
	store, err := OpenStore(stateDir, roots)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := store.Snapshot(); snapshot.Current != nil || snapshot.HighestSequence != 0 {
		t.Fatalf("empty store = %+v", snapshot)
	}
	first := signTestDocument(t, root, testDocument(t, 5, map[string]string{"k1": generatePublicKey(t)}))
	verified, outcome, err := store.Apply(first)
	if err != nil || outcome != Applied || verified.Document.Sequence != 5 {
		t.Fatalf("apply 5 = %v %v %v", verified.Document.Sequence, outcome, err)
	}
	same := signTestDocument(t, root, testDocument(t, 5, map[string]string{"k9": generatePublicKey(t)}))
	if _, outcome, err := store.Apply(same); err != nil || outcome != IgnoredNotNewer {
		t.Fatalf("equal sequence = %v %v", outcome, err)
	}
	lower := signTestDocument(t, root, testDocument(t, 4, map[string]string{"k8": generatePublicKey(t)}))
	if _, outcome, err := store.Apply(lower); err != nil || outcome != IgnoredNotNewer {
		t.Fatalf("lower sequence = %v %v", outcome, err)
	}
	snapshot := store.Snapshot()
	if snapshot.HighestSequence != 5 || snapshot.Current == nil || snapshot.Current.Document.Keys[0].KeyID != "k1" {
		t.Fatalf("after ignored sets = %+v", snapshot)
	}
	higher := signTestDocument(t, root, testDocument(t, 6, map[string]string{"k2": generatePublicKey(t)}))
	if _, outcome, err := store.Apply(higher); err != nil || outcome != Applied {
		t.Fatalf("higher sequence = %v %v", outcome, err)
	}
	if store.Snapshot().HighestSequence != 6 {
		t.Fatal("highest sequence did not advance")
	}
}

func TestStorePersistsHighestSetAcrossRestartsWithPrivateMode(t *testing.T) {
	root := newTestRootKey(t)
	roots := rootsOf(trustroot.ChannelDevelopment, root)
	stateDir := t.TempDir()
	store, err := OpenStore(stateDir, roots)
	if err != nil {
		t.Fatal(err)
	}
	current := signTestDocument(t, root, testDocument(t, 9, map[string]string{"k1": generatePublicKey(t)}))
	if _, _, err := store.Apply(current); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(stateDir, StateDirectory, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, StateDirectory))
	if err != nil || len(entries) != 1 {
		t.Fatalf("state directory entries = %v, %v", entries, err)
	}
	restarted, err := OpenStore(stateDir, roots)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := restarted.Snapshot()
	if snapshot.HighestSequence != 9 || snapshot.Current == nil || snapshot.Current.Signed != current {
		t.Fatalf("restarted store = %+v", snapshot)
	}
	older := signTestDocument(t, root, testDocument(t, 3, map[string]string{"k0": generatePublicKey(t)}))
	if _, outcome, err := restarted.Apply(older); err != nil || outcome != IgnoredNotNewer {
		t.Fatalf("older cached set after restart = %v %v", outcome, err)
	}
	if restarted.Snapshot().Current.Signed != current {
		t.Fatal("restart with an older cached set replaced the persisted highest set")
	}
}

func TestStoreRefusesUnverifiableSetsWithoutChangingState(t *testing.T) {
	trusted, foreign := newTestRootKey(t), newTestRootKey(t)
	store, err := OpenStore(t.TempDir(), rootsOf(trustroot.ChannelProduction, trusted))
	if err != nil {
		t.Fatal(err)
	}
	set := signTestDocument(t, foreign, testDocument(t, 50, map[string]string{"k1": generatePublicKey(t)}))
	if _, _, err := store.Apply(set); CodeOf(err) != CodeUnknownRoot {
		t.Fatalf("foreign set = %v", err)
	}
	if snapshot := store.Snapshot(); snapshot.HighestSequence != 0 || snapshot.Current != nil {
		t.Fatalf("store changed after an unverifiable set: %+v", snapshot)
	}
}

func TestStoreKeepsTheSequenceFloorWhenThePersistedSetNoLongerVerifies(t *testing.T) {
	oldRoot, newRoot := newTestRootKey(t), newTestRootKey(t)
	stateDir := t.TempDir()
	store, err := OpenStore(stateDir, rootsOf(trustroot.ChannelDevelopment, oldRoot))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(signTestDocument(t, oldRoot, testDocument(t, 7, map[string]string{"k1": generatePublicKey(t)}))); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenStore(stateDir, rootsOf(trustroot.ChannelDevelopment, newRoot))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := upgraded.Snapshot()
	if snapshot.Current != nil || snapshot.HighestSequence != 7 || !snapshot.PersistedSetUnverifiable {
		t.Fatalf("store under rotated roots = %+v", snapshot)
	}
	replay := signTestDocument(t, newRoot, testDocument(t, 7, map[string]string{"k2": generatePublicKey(t)}))
	if _, outcome, err := upgraded.Apply(replay); err != nil || outcome != IgnoredNotNewer {
		t.Fatalf("replayed sequence under new root = %v %v", outcome, err)
	}
	next := signTestDocument(t, newRoot, testDocument(t, 8, map[string]string{"k2": generatePublicKey(t)}))
	if _, outcome, err := upgraded.Apply(next); err != nil || outcome != Applied {
		t.Fatalf("next sequence under new root = %v %v", outcome, err)
	}
	if snapshot := upgraded.Snapshot(); snapshot.Current == nil || snapshot.PersistedSetUnverifiable {
		t.Fatalf("store after recovery = %+v", snapshot)
	}
}

func TestStoreRefusesCorruptOrExposedStateFiles(t *testing.T) {
	root := newTestRootKey(t)
	roots := rootsOf(trustroot.ChannelDevelopment, root)
	write := func(t *testing.T, raw string, mode os.FileMode) string {
		t.Helper()
		stateDir := t.TempDir()
		directory := filepath.Join(stateDir, StateDirectory)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, StateFileName)
		if err := os.WriteFile(path, []byte(raw), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return stateDir
	}
	valid := signTestDocument(t, root, testDocument(t, 2, map[string]string{"k1": generatePublicKey(t)}))
	validState := `{"format":1,"highestSequence":2,"set":{"document_base64":"` + valid.DocumentBase64 + `","root_key_id":"` + valid.RootKeyID + `","signature_base64":"` + valid.SignatureBase64 + `"}}`
	if _, err := OpenStore(write(t, validState, 0o600), roots); err != nil {
		t.Fatalf("valid state = %v", err)
	}
	cases := map[string]string{
		"not json":           "{",
		"unknown field":      `{"format":1,"highestSequence":2,"extra":1}`,
		"unsupported format": `{"format":2,"highestSequence":2}`,
		"negative sequence":  `{"format":1,"highestSequence":-1}`,
		"sequence mismatch":  `{"format":1,"highestSequence":3,"set":{"document_base64":"` + valid.DocumentBase64 + `","root_key_id":"` + valid.RootKeyID + `","signature_base64":"` + valid.SignatureBase64 + `"}}`,
		"set without floor":  `{"format":1,"highestSequence":0,"set":{"document_base64":"` + valid.DocumentBase64 + `","root_key_id":"` + valid.RootKeyID + `","signature_base64":"` + valid.SignatureBase64 + `"}}`,
	}
	for name, raw := range cases {
		if _, err := OpenStore(write(t, raw, 0o600), roots); err == nil {
			t.Fatalf("%s: corrupt state was accepted", name)
		}
	}
	if _, err := OpenStore(write(t, validState, 0o644), roots); err == nil {
		t.Fatal("group-readable state file was accepted")
	}
}

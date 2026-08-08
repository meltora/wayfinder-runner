package collector

import (
	"testing"
)

func TestNextRevisionConcurrency(t *testing.T) {
	s := NewPlanStore(t.TempDir())
	rev, err := s.NextRevision("j1", 0, "d1")
	if err != nil || rev != 1 {
		t.Fatalf("rev = %d err = %v", rev, err)
	}
	if _, err := s.NextRevision("j1", 0, "d2"); err == nil {
		t.Fatal("stale base must be rejected")
	}
	rev, err = s.NextRevision("j1", 1, "d2")
	if err != nil || rev != 2 {
		t.Fatalf("rev = %d err = %v", rev, err)
	}
	rev, err = s.NextRevision("j1", 1, "d2")
	if err != nil || rev != 2 {
		t.Fatalf("idempotent revision retry = %d err = %v", rev, err)
	}
	st, err := s.Get("j1")
	if err != nil || st.LatestRevision != 2 || st.LatestDigest != "d2" {
		t.Fatalf("state = %+v err = %v", st, err)
	}
}

func TestJobsAreIndependent(t *testing.T) {
	s := NewPlanStore(t.TempDir())
	if _, err := s.NextRevision("j1", 0, "d1"); err != nil {
		t.Fatal(err)
	}
	rev, err := s.NextRevision("j2", 0, "d1")
	if err != nil || rev != 1 {
		t.Fatalf("j2 rev = %d err = %v", rev, err)
	}
}

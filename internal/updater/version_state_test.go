package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type captureCatalog struct {
	query   CatalogQuery
	release Release
}

func (c *captureCatalog) Latest(_ context.Context, q CatalogQuery) (Release, error) {
	c.query = q
	return c.release, nil
}

func TestCurrentRunnerVersionSlotFormsAndCatalogPolicy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 5, "5.0.0")
	catalog := &captureCatalog{release: rel}
	clock := &fakeClock{now: time.Now()}
	core := newTestCoordinator(t, dir, catalog, fakeArtifacts{body: archive}, &fakeService{}, clock)
	if got, err := core.currentRunnerVersion(); err != nil || got != "0.1.0" {
		t.Fatalf("automatic version=%q err=%v", got, err)
	}
	manual := createTestSlot(t, core, "2.3.4")
	replaceTestLink(t, core.cfg.CurrentLink, manual)
	clock.now = clock.now.Add(2 * time.Minute)
	if _, err := core.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	if catalog.query.RunnerVersion != "2.3.4" || catalog.query.UpdaterVersion != "0.1.0" {
		t.Fatalf("query=%+v", catalog.query)
	}
}

func TestCurrentRunnerVersionInitialFallbackAndMalformedFailClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 1, "1.0.0")
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
	initial := createTestSlot(t, core, "initial")
	replaceTestLink(t, core.cfg.CurrentLink, initial)
	if got, err := core.currentRunnerVersion(); err != nil || got != "0.1.0" {
		t.Fatalf("initial=%q err=%v", got, err)
	}
	bad := createTestSlot(t, core, "0000000000000000000x-1.2.3")
	replaceTestLink(t, core.cfg.CurrentLink, bad)
	if _, err := core.currentRunnerVersion(); err == nil {
		t.Fatal("malformed slot accepted")
	}
	_ = os.Remove(core.cfg.CurrentLink)
	if _, err := core.currentRunnerVersion(); err == nil {
		t.Fatal("missing current accepted")
	}
}

func TestBoundedErrorsAndStrictPhaseState(t *testing.T) {
	t.Parallel()
	invalid := string([]byte{0xff, 'x'})
	got := boundedError(errors.New(invalid + strings.Repeat("z", 5000)))
	if len(got) > maxPersistedErrorBytes || !strings.Contains(got, "�") {
		t.Fatalf("bounded error len=%d value=%q", len(got), got[:minInt(len(got), 10)])
	}
	if boundedError(nil) != "" {
		t.Fatal("nil error persisted")
	}
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 1, "1.0.0")
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
	state := durableState{Schema: 1, Phase: PhaseIdle, LastError: strings.Repeat("x", maxPersistedErrorBytes+1)}
	if err := core.validateState(state); err == nil {
		t.Fatal("oversized state error accepted")
	}
	state = durableState{Schema: 1, Phase: PhasePrepared}
	if err := core.validateState(state); err == nil {
		t.Fatal("incomplete prepared state accepted")
	}
	state = durableState{Schema: 1, Phase: PhaseIdle, FailedSequence: 1}
	if err := core.validateState(state); err == nil {
		t.Fatal("incomplete failure tuple accepted")
	}
}

func TestRecoverInterruptedPreparationAndActivation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 2, "2.0.0")
	svc := &fakeService{}
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, svc, &fakeClock{now: time.Now()})
	preparing := durableState{Schema: 1, Phase: PhasePreparing, UpdateID: rel.ReleaseID, PendingSequence: rel.Sequence, PendingDigest: rel.PayloadDigest, PendingVersion: rel.Version, ObservedSequence: rel.Sequence, ObservedReleaseID: rel.ReleaseID, ObservedDigest: rel.PayloadDigest}
	if err := core.saveState(preparing); err != nil {
		t.Fatal(err)
	}
	if err := core.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := core.loadState()
	if err != nil || state.Phase != PhaseIdle {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err := core.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	state, err = core.loadState()
	if err != nil {
		t.Fatal(err)
	}
	old := mustReadlink(t, core.cfg.CurrentLink)
	state.PreviousTarget = old
	state.Phase = PhaseStarting
	if err := core.saveState(state); err != nil {
		t.Fatal(err)
	}
	if err := core.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = core.loadState()
	if err != nil || state.Phase != PhaseIdle || state.FailedReleaseID != rel.ReleaseID {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestStageQuarantinesInactiveSlotButNeverActiveUpdater(t *testing.T) {
	t.Parallel()
	for name, active := range map[string]bool{"inactive": false, "active updater": true} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := makeArchive(t, map[string][]byte{"our-runner": []byte("new"), "our-runner-updater": []byte("new")})
			rel := releaseFor(archive, 4, "4.0.0")
			core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
			slot := createTestSlot(t, core, "00000000000000000004-4.0.0")
			if active {
				replaceTestLink(t, core.cfg.UpdaterCurrentLink, filepath.Join(slot, "our-runner-updater"))
			}
			_, err := core.Prepare(context.Background(), rel.ReleaseID)
			if active && err == nil {
				t.Fatal("active updater slot quarantined")
			}
			if !active && err != nil {
				t.Fatal(err)
			}
			if !active {
				raw, readErr := os.ReadFile(filepath.Join(slot, "our-runner"))
				if readErr != nil || string(raw) != "new" {
					t.Fatalf("slot=%q err=%v", raw, readErr)
				}
				entries, _ := os.ReadDir(core.cfg.VersionsDir)
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".quarantine-") {
						t.Fatalf("quarantine retained: %s", entry.Name())
					}
				}
			}
		})
	}
}

func TestActivationStopFailureRestoresRunnerWithoutSwitch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 3, "3.0.0")
	svc := &fakeService{stopErr: errors.New("stop")}
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, svc, &fakeClock{now: time.Now()})
	old := mustReadlink(t, core.cfg.CurrentLink)
	if _, err := core.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Activate(context.Background(), rel.ReleaseID); err == nil {
		t.Fatal("stop failure ignored")
	}
	if got := mustReadlink(t, core.cfg.CurrentLink); got != old {
		t.Fatalf("current=%q", got)
	}
}

func TestFilesystemAndStateBoundariesFailClosed(t *testing.T) {
	t.Parallel()
	if err := validateRootDirectory("/", false); err != nil {
		t.Fatal(err)
	}
	if err := validateRootDirectory("/", true); err == nil {
		t.Fatal("public root accepted as private")
	}
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 1, "1.0.0")
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
	statePath := core.statePath()
	for name, raw := range map[string][]byte{"schema": []byte(`{}`), "trailing": []byte(`{"schema":1,"phase":"idle"} {}`), "oversize": []byte(strings.Repeat("x", (64<<10)+1))} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(statePath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := core.loadState(); err == nil {
				t.Fatal("invalid state accepted")
			}
		})
	}
	_ = os.Remove(statePath)
	lock := filepath.Join(core.cfg.StateDir, "transaction.lock")
	if err := os.WriteFile(lock, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Status(context.Background()); err == nil {
		t.Fatal("unsafe lock accepted")
	}
}

func TestAbortRestoreFailureRemainsRecoverable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 8, "8.0.0")
	svc := &fakeService{stopErr: errors.New("stop"), startErr: errors.New("start")}
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, svc, &fakeClock{now: time.Now()})
	if _, err := core.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Activate(context.Background(), rel.ReleaseID); err == nil {
		t.Fatal("dual service failure ignored")
	}
	state, err := core.loadState()
	if err != nil || state.Phase != PhaseStopping {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestStatusPerformsExpiredHealthRollback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 9, "9.0.0")
	clock := &fakeClock{now: time.Now()}
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, clock)
	if _, err := core.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Activate(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	status, err := core.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseIdle || status.FailedReleaseID != rel.ReleaseID {
		t.Fatalf("status=%+v", status)
	}
}

func TestHealthRetryFinishesDurableUpdaterCommit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 10, "10.0.0")
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
	if _, err := core.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Activate(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	core.failpoint = func(point string) error {
		if point == "after_health_commit" {
			return errors.New("crash")
		}
		return nil
	}
	if err := core.Health(context.Background(), rel.ReleaseID, rel.Version); err == nil {
		t.Fatal("commit failpoint ignored")
	}
	core.failpoint = nil
	if err := core.Health(context.Background(), rel.ReleaseID, rel.Version); err != nil {
		t.Fatal(err)
	}
	state, err := core.loadState()
	if err != nil || state.Phase != PhaseIdle || state.ReleaseID != rel.ReleaseID {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestReleaseValidationReplayEquivocationAndSuppression(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	release := releaseFor(archive, 5, "5.0.0")
	core := newTestCoordinator(t, dir, fakeCatalog{release: release}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
	invalid := release
	invalid.SHA256 = "BAD"
	if err := core.validateRelease(invalid, durableState{}); err == nil {
		t.Fatal("invalid digest accepted")
	}
	if err := core.validateRelease(release, durableState{ObservedSequence: 6}); err == nil {
		t.Fatal("sequence rollback accepted")
	}
	if err := core.validateRelease(release, durableState{ObservedSequence: 5, ObservedReleaseID: "other", ObservedDigest: release.PayloadDigest}); !errors.Is(err, ErrEquivocation) {
		t.Fatalf("equivocation=%v", err)
	}
	installed := durableState{ObservedSequence: 5, ObservedReleaseID: release.ReleaseID, ObservedDigest: release.PayloadDigest, Sequence: 5, ReleaseID: release.ReleaseID, PayloadDigest: release.PayloadDigest}
	if err := core.validateRelease(release, installed); !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("installed=%v", err)
	}
	failed := durableState{ObservedSequence: 5, ObservedReleaseID: release.ReleaseID, ObservedDigest: release.PayloadDigest, FailedSequence: 5, FailedDigest: release.PayloadDigest}
	if err := core.validateRelease(release, failed); !errors.Is(err, ErrSuppressed) {
		t.Fatalf("suppressed=%v", err)
	}
	newer := releaseFor(archive, 6, "6.0.0")
	if err := core.validateRelease(newer, failed); err != nil {
		t.Fatal(err)
	}
}

func TestAllDurablePhasesAndComponentValidation(t *testing.T) {
	t.Parallel()
	for _, phase := range []Phase{PhaseIdle, PhasePreparing, PhasePrepared, PhaseStopping, PhaseSwitching, PhaseStarting, PhaseAwaitingHealth, PhaseRollingBack, PhaseCommittingUpdater} {
		if !validPhase(phase) {
			t.Fatalf("valid phase rejected: %s", phase)
		}
	}
	if validPhase("unknown") {
		t.Fatal("unknown phase accepted")
	}
	for _, value := range []string{"", ".", "..", "bad/path", strings.Repeat("x", 129)} {
		if safeComponent(value) {
			t.Fatalf("unsafe component accepted: %q", value)
		}
	}
}

func createTestSlot(t *testing.T, c *Coordinator, name string) string {
	t.Helper()
	path := filepath.Join(c.cfg.VersionsDir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, binary := range requiredBinaries {
		if err := os.WriteFile(filepath.Join(path, binary), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return path
}
func replaceTestLink(t *testing.T, link, target string) {
	t.Helper()
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

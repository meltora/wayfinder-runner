package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testRootlessMapping = UserMapping{Mode: UserMappingRootless, ContainerUID: 10001, ContainerGID: 10001, HostUID: 110000, HostGID: 110000}

const testOwnershipImage = "registry.example/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type ownershipLayout struct {
	state, workspace, checks, home, checkHome, socket, intake, doctor string
}

func newOwnershipLayout(t *testing.T) ownershipLayout {
	t.Helper()
	state := t.TempDir()
	layout := ownershipLayout{
		state:     state,
		workspace: filepath.Join(state, "jobs", "job-123"),
		checks:    filepath.Join(state, "job-results", "job-123.checks"),
		home:      filepath.Join(state, "job-homes", "home-456"),
		checkHome: filepath.Join(state, "check-homes", "home-789"),
		socket:    filepath.Join(state, "sockets", "job-321"),
		intake:    filepath.Join(state, "work-intake-proposals", "execution-654"),
		doctor:    filepath.Join(state, "doctor", "sandbox-987", "workspace"),
	}
	for _, dir := range []string{layout.workspace, layout.checks, layout.home, layout.checkHome, layout.socket, layout.intake, layout.doctor,
		filepath.Join(state, "job-results", "job-123", "repo-1"), filepath.Join(state, "repos", "mirror")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return layout
}

type recordedHelper struct {
	name string
	args []string
}

func recordingOwnership(layout ownershipLayout, calls *[]recordedHelper) DockerOwnership {
	return DockerOwnership{
		Mapping: testRootlessMapping, Image: testOwnershipImage, StateDir: layout.state,
		EffectiveUID: func() int { return 1000 },
		Run: func(_ context.Context, name string, args []string) error {
			*calls = append(*calls, recordedHelper{name: name, args: append([]string(nil), args...)})
			return nil
		},
	}
}

func TestOwnershipHandOffArgvIsExactlyTheContract(t *testing.T) {
	layout := newOwnershipLayout(t)
	var calls []recordedHelper
	ownership := recordingOwnership(layout, &calls)
	request := OwnershipRequest{
		RunnerID: "runner-1", AttemptID: "attempt-1", Purpose: "job",
		Workspace: []string{layout.workspace}, Private: []string{layout.home, layout.socket},
	}
	if err := ownership.HandOff(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	name := ContainerName("runner-1", "attempt-1-own-job")
	want := []string{
		"run", "--rm", "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "CHOWN",
		"--security-opt", "no-new-privileges", "--network", "none", "--read-only", "--log-driver", "none",
		"--pids-limit", "64", "--memory", "128m", "--name", name,
		"--label", "com.ourplatform.runner-id=runner-1", "--label", "com.ourplatform.attempt-id=attempt-1",
		"-v", layout.workspace + ":" + layout.workspace + ":rw",
		"-v", layout.home + ":" + layout.home + ":rw",
		"-v", layout.socket + ":" + layout.socket + ":rw",
		testOwnershipImage, "sh", "-c", ownershipHandOffScript, "our-runner-ownership", "1", "--",
		layout.workspace, layout.home, layout.socket,
	}
	if len(calls) != 1 || calls[0].name != name || strings.Join(calls[0].args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("hand-off argv = %q\nwant %q", calls, want)
	}
	if !strings.Contains(ownershipHandOffScript, `chmod -R g+rX -- "$root"`) ||
		!strings.Contains(ownershipHandOffScript, `chown -hR 10001 -- "$root"`) ||
		!strings.Contains(ownershipHandOffScript, `chown -hR 10001:10001 -- "$root"`) {
		t.Fatalf("hand-off script = %s", ownershipHandOffScript)
	}
}

func TestOwnershipReclaimArgvAddsOnlyDACReadSearch(t *testing.T) {
	layout := newOwnershipLayout(t)
	var calls []recordedHelper
	ownership := recordingOwnership(layout, &calls)
	request := OwnershipRequest{
		RunnerID: "runner-1", AttemptID: "attempt-1", Purpose: "task-abc",
		Workspace: []string{layout.checks}, Private: []string{layout.checkHome},
	}
	if err := ownership.Reclaim(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	name := ContainerName("runner-1", "attempt-1-own-task-abc")
	want := []string{
		"run", "--rm", "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_READ_SEARCH",
		"--security-opt", "no-new-privileges", "--network", "none", "--read-only", "--log-driver", "none",
		"--pids-limit", "64", "--memory", "128m", "--name", name,
		"--label", "com.ourplatform.runner-id=runner-1", "--label", "com.ourplatform.attempt-id=attempt-1",
		"-v", layout.checks + ":" + layout.checks + ":rw",
		"-v", layout.checkHome + ":" + layout.checkHome + ":rw",
		testOwnershipImage, "chown", "-hR", "0:0", "--", layout.checks, layout.checkHome,
	}
	if len(calls) != 1 || strings.Join(calls[0].args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("reclaim argv = %q\nwant %q", calls, want)
	}
}

func TestOwnershipAcceptsEveryPerJobParent(t *testing.T) {
	layout := newOwnershipLayout(t)
	var calls []recordedHelper
	ownership := recordingOwnership(layout, &calls)
	request := OwnershipRequest{
		RunnerID: "runner-1", AttemptID: "attempt-1", Purpose: "recover-1",
		Private: []string{layout.workspace, layout.checks, layout.home, layout.checkHome, layout.socket, layout.intake, layout.doctor},
	}
	if err := ownership.Reclaim(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func TestOwnershipRefusesUnsafeModesAndRoots(t *testing.T) {
	layout := newOwnershipLayout(t)
	link := filepath.Join(layout.state, "jobs", "job-999")
	if err := os.Symlink(layout.workspace, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(layout.state, "jobs", "job-777")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(layout.state, "identity.json")
	if err := os.WriteFile(identity, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	many := []string{}
	for index := 0; index < 9; index++ {
		dir := filepath.Join(layout.state, "jobs", fmt.Sprintf("job-%d", 1000+index))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		many = append(many, dir)
	}
	colon := filepath.Join(layout.state, "jobs", "job-1:2")
	comma := filepath.Join(layout.state, "jobs", "job-1,2")
	newline := filepath.Join(layout.state, "jobs", "job-1\n2")
	for _, dir := range []string{colon, comma, newline} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	base := func() OwnershipRequest {
		return OwnershipRequest{RunnerID: "runner-1", AttemptID: "attempt-1", Purpose: "job", Workspace: []string{layout.workspace}}
	}
	cases := map[string]struct {
		mutate    func(*DockerOwnership, *OwnershipRequest)
		wantError string
	}{
		"identity mode": {func(o *DockerOwnership, _ *OwnershipRequest) {
			o.Mapping = UserMapping{Mode: UserMappingIdentity, ContainerUID: 1000, ContainerGID: 1000, HostUID: 1000, HostGID: 1000}
		}, "rootless"},
		"unresolved mode": {func(o *DockerOwnership, _ *OwnershipRequest) { o.Mapping = UserMapping{} }, "rootless"},
		"host root":       {func(o *DockerOwnership, _ *OwnershipRequest) { o.EffectiveUID = func() int { return 0 } }, "host root"},
		"zero roots":      {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = nil }, "between 1 and 8"},
		"nine roots":      {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = many }, "between 1 and 8"},
		"relative":        {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{"jobs/job-123"} }, "absolute"},
		"unclean": {func(_ *DockerOwnership, r *OwnershipRequest) {
			r.Workspace = []string{layout.workspace + "/../job-123"}
		}, "absolute"},
		"symlink":         {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{link} }, "real directory"},
		"not directory":   {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{file} }, "real directory"},
		"colon":           {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{colon} }, "unsupported character"},
		"comma":           {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{comma} }, "unsupported character"},
		"newline":         {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{newline} }, "unsupported character"},
		"nul":             {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{layout.workspace + "\x00"} }, "unsupported character"},
		"carriage return": {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{layout.workspace + "\r"} }, "unsupported character"},
		"state dir":       {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{layout.state} }, "per-job"},
		"identity file":   {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{identity} }, "per-job"},
		"jobs parent": {func(_ *DockerOwnership, r *OwnershipRequest) {
			r.Workspace = []string{filepath.Join(layout.state, "jobs")}
		}, "per-job"},
		"repos": {func(_ *DockerOwnership, r *OwnershipRequest) {
			r.Workspace = []string{filepath.Join(layout.state, "repos", "mirror")}
		}, "per-job"},
		"trusted repository": {func(_ *DockerOwnership, r *OwnershipRequest) {
			r.Workspace = []string{filepath.Join(layout.state, "job-results", "job-123", "repo-1")}
		}, "per-job"},
		"outside state": {func(_ *DockerOwnership, r *OwnershipRequest) { r.Workspace = []string{t.TempDir()} }, "per-job"},
		"duplicate": {func(_ *DockerOwnership, r *OwnershipRequest) {
			r.Workspace = []string{layout.workspace}
			r.Private = []string{layout.workspace}
		}, "duplicate"},
		"bad purpose":    {func(_ *DockerOwnership, r *OwnershipRequest) { r.Purpose = "Job Purpose" }, "purpose"},
		"bad attempt":    {func(_ *DockerOwnership, r *OwnershipRequest) { r.AttemptID = "" }, "identity"},
		"bad runner":     {func(_ *DockerOwnership, r *OwnershipRequest) { r.RunnerID = "runner 1" }, "identity"},
		"bad image":      {func(o *DockerOwnership, _ *OwnershipRequest) { o.Image = "-v" }, "image"},
		"relative state": {func(o *DockerOwnership, _ *OwnershipRequest) { o.StateDir = "state" }, "state directory"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			var calls []recordedHelper
			ownership := recordingOwnership(layout, &calls)
			request := base()
			test.mutate(&ownership, &request)
			for phase, operation := range map[string]func(context.Context, OwnershipRequest) error{
				"hand-off": ownership.HandOff, "reclaim": ownership.Reclaim,
			} {
				err := operation(context.Background(), request)
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("%s err = %v, want %q", phase, err, test.wantError)
				}
			}
			if len(calls) != 0 {
				t.Fatalf("refused request started %d helpers", len(calls))
			}
		})
	}
}

func TestOwnershipReportsHelperFailure(t *testing.T) {
	layout := newOwnershipLayout(t)
	ownership := recordingOwnership(layout, &[]recordedHelper{})
	ownership.Run = func(context.Context, string, []string) error { return errors.New("helper exited 1") }
	err := ownership.HandOff(context.Background(), OwnershipRequest{
		RunnerID: "runner-1", AttemptID: "attempt-1", Purpose: "job", Workspace: []string{layout.workspace},
	})
	if err == nil || !strings.Contains(err.Error(), "helper exited 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestReclaimInBatchesUsesOneHelperPerEightRoots(t *testing.T) {
	layout := newOwnershipLayout(t)
	var roots []string
	for index := 0; index < 17; index++ {
		dir := filepath.Join(layout.state, "jobs", fmt.Sprintf("job-%d", 2000+index))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, dir)
	}
	var calls []recordedHelper
	ownership := recordingOwnership(layout, &calls)
	if err := ReclaimInBatches(context.Background(), ownership, "runner-1", "recovery", roots); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("helpers = %d", len(calls))
	}
	mounts := 0
	for index, call := range calls {
		if call.name != ContainerName("runner-1", fmt.Sprintf("recovery-own-recover-%d", index+1)) {
			t.Fatalf("helper %d name = %s", index, call.name)
		}
		for _, argument := range call.args {
			if argument == "-v" {
				mounts++
			}
		}
	}
	if mounts != 17 {
		t.Fatalf("mounted roots = %d", mounts)
	}
	if err := ReclaimInBatches(context.Background(), ownership, "runner-1", "recovery", nil); err != nil || len(calls) != 3 {
		t.Fatalf("empty batch err=%v helpers=%d", err, len(calls))
	}
}

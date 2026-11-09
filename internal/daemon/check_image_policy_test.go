package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/isolation"
)

const (
	policyJobImage   = "registry.example.test/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	policyCheckImage = "our-runner-check-go:rdbc@sha256:3333333333333333333333333333333333333333333333333333333333333333"
	argvSeparator    = "\x1f"
)

var checkHomePattern = regexp.MustCompile(`home-[0-9]+`)

type exactDockerArgv struct{ path string }

func installArgvRecordingDocker(t *testing.T) exactDockerArgv {
	t.Helper()
	dir := t.TempDir()
	log := exactDockerArgv{path: filepath.Join(dir, "argv")}
	script := "#!/bin/sh\nfor arg in \"$@\"; do printf '%s\\037' \"$arg\" >> '" + log.path + "'; done\nprintf '\\n' >> '" + log.path + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func (l exactDockerArgv) runs(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var runs [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		argv := strings.Split(strings.TrimSuffix(line, argvSeparator), argvSeparator)
		if len(argv) > 0 && argv[0] == "run" {
			runs = append(runs, argv)
		}
	}
	return runs
}

func dockerCheckRunner(t *testing.T, jr *jobRunner) {
	t.Helper()
	jr.d.runnerID = "runner-1"
	jr.d.cfg.Isolation = "docker"
	jr.d.cfg.JobNetwork = "none"
	jr.d.cfg.JobImage = policyJobImage
	jr.d.cfg.JobMemoryMB, jr.d.cfg.JobCPUs, jr.d.cfg.JobPIDs = 2048, 2, 256
	jr.d.userMapping = testRootlessMapping
	jr.d.ownership = &recordingOwnership{log: &orderLog{}}
	jr.d.removeJobContainer = func(context.Context, string, string) error { return nil }
}

func containerImage(t *testing.T, argv []string, images ...string) string {
	t.Helper()
	var found []string
	for _, arg := range argv {
		if slices.Contains(images, arg) {
			found = append(found, arg)
		}
	}
	if len(found) != 1 {
		t.Fatalf("argv names images %v, want exactly one of %v: %q", found, images, argv)
	}
	return found[0]
}

func normalizedArgv(argv []string, image string) []string {
	out := make([]string, len(argv))
	for index, arg := range argv {
		arg = strings.ReplaceAll(arg, image, "<image>")
		out[index] = checkHomePattern.ReplaceAllString(arg, "home-N")
	}
	return out
}

func TestCheckImageChangesOnlyTheContainerImageOfAWorkspaceTask(t *testing.T) {
	jr, _ := testJobRunner(t)
	dockerCheckRunner(t, jr)
	log := installArgvRecordingDocker(t)
	root := checkWorkspaceRoot(jr.d.cfg.StateDir, jr.workDir)
	dir := filepath.Join(root, "primary")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"", policyCheckImage} {
		if _, _, err := jr.runWorkspaceTask(context.Background(), workspaceTask{
			purpose: "check-go-test", ownershipPurpose: "check", root: root, dir: dir,
			command: "go", args: []string{"test", "./..."}, configuredNetwork: true, outputLimit: 1024, image: image,
		}); err != nil {
			t.Fatal(err)
		}
	}
	runs := log.runs(t)
	if len(runs) != 2 {
		t.Fatalf("docker runs = %d", len(runs))
	}
	if got := containerImage(t, runs[0], policyJobImage, policyCheckImage); got != policyJobImage {
		t.Fatalf("a check without an image ran in %s", got)
	}
	if got := containerImage(t, runs[1], policyJobImage, policyCheckImage); got != policyCheckImage {
		t.Fatalf("a check with an image ran in %s", got)
	}
	if without, with := normalizedArgv(runs[0], policyJobImage), normalizedArgv(runs[1], policyCheckImage); !slices.Equal(without, with) {
		t.Fatalf("the check image changed more than the image:\nwithout %q\nwith    %q", without, with)
	}
	for _, want := range []string{"--read-only", "--network", "--user", "--cap-drop", "--security-opt", "--memory", "--cpus", "--pids-limit", "--tmpfs", "--label"} {
		if !slices.Contains(runs[1], want) {
			t.Fatalf("check image argv lost %s: %q", want, runs[1])
		}
	}
}

func TestACheckWithoutAnImageKeepsTheExactJobImageArgv(t *testing.T) {
	jr, _ := testJobRunner(t)
	dockerCheckRunner(t, jr)
	log := installArgvRecordingDocker(t)
	root := checkWorkspaceRoot(jr.d.cfg.StateDir, jr.workDir)
	dir := filepath.Join(root, "primary")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outcome := jr.executeConfiguredCheck(context.Background(), "go-test", root, dir, config.CheckConfig{Command: "go", Args: []string{"test", "./..."}})
	if outcome.Result != "passed" {
		t.Fatalf("outcome = %+v", outcome)
	}
	runs := log.runs(t)
	if len(runs) != 1 {
		t.Fatalf("docker runs = %d", len(runs))
	}
	iso, err := isolation.Select("docker", false)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(jr.d.cfg.StateDir, "check-homes", "home-0")
	attempt := jr.job.AttemptID + "-task-" + shortDigest("check-go-test")
	_, want, err := iso.Wrap("go", []string{"test", "./..."}, jr.commandPolicy(dir, root, home, attempt, true))
	if err != nil {
		t.Fatal(err)
	}
	if got := normalizedArgv(runs[0], policyJobImage); !slices.Equal(got, normalizedArgv(want, policyJobImage)) {
		t.Fatalf("argv without a check image changed:\ngot  %q\nwant %q", got, want)
	}
}

func TestInstallAndCommandOfACheckRunInItsImage(t *testing.T) {
	jr, _ := testJobRunner(t)
	dockerCheckRunner(t, jr)
	log := installArgvRecordingDocker(t)
	root := checkWorkspaceRoot(jr.d.cfg.StateDir, jr.workDir)
	dir := filepath.Join(root, "primary")
	cache := filepath.Join(t.TempDir(), "npm-cache")
	for _, path := range []string{dir, cache} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		check config.CheckConfig
		want  string
	}{
		{config.CheckConfig{Command: "npm", Args: []string{"test"}, Image: policyCheckImage}, policyCheckImage},
		{config.CheckConfig{Command: "npm", Args: []string{"test"}}, policyJobImage},
	} {
		request := checkRequest{checkID: "web-unit", check: test.check}
		if _, _, err := jr.runInstallCommand(context.Background(), request, dir, []string{"ci"}, cache, 1024); err != nil {
			t.Fatal(err)
		}
		if outcome := jr.executeConfiguredCheck(context.Background(), "web-unit", root, dir, test.check); outcome.Result != "passed" {
			t.Fatalf("outcome = %+v", outcome)
		}
		runs := log.runs(t)
		for _, argv := range runs[len(runs)-2:] {
			if got := containerImage(t, argv, policyJobImage, policyCheckImage); got != test.want {
				t.Fatalf("ran in %s, want %s: %q", got, test.want, argv)
			}
		}
	}
}

func TestIterativeAndTerminalRunsOfACheckUseItsImage(t *testing.T) {
	jr, _ := runCheckFixture(t)
	dockerCheckRunner(t, jr)
	log := installArgvRecordingDocker(t)
	jr.d.cfg.Checks = map[string]config.CheckConfig{
		"go-test": {Command: "go", Args: []string{"test", "./..."}, Image: policyCheckImage},
		"lint":    {Command: "golangci-lint", Args: []string{"run"}},
	}
	jr.job.RequiredChecks = []string{"go-test", "lint"}
	for _, id := range []string{"go-test", "lint"} {
		if _, err := call(t, jr, "platform_run_check", `{"check_id":"`+id+`","idempotency_key":"rc-image-`+id+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call(t, jr, "platform_run_check", `{"check_id":"lint","idempotency_key":"rc-image-override","image":"`+policyCheckImage+`"}`); err == nil {
		t.Fatal("platform_run_check accepted an image")
	}
	commitSet := map[string]string{"primary": gitHead(t, jr.repositories[0].WorkDir)}
	if !jr.runRequiredChecks(context.Background(), commitSet, nil) {
		t.Fatal("terminal checks failed")
	}
	var images []string
	for _, argv := range log.runs(t) {
		images = append(images, containerImage(t, argv, policyJobImage, policyCheckImage))
	}
	want := []string{policyCheckImage, policyJobImage, policyCheckImage, policyJobImage}
	if !slices.Equal(images, want) {
		t.Fatalf("images = %v, want %v", images, want)
	}
	if jr.d.cfg.JobImage != policyJobImage || jr.d.cfg.Checks["lint"].Image != "" {
		t.Fatal("running a check changed the configured images")
	}
	provenance := map[string]string{}
	for _, artifact := range uploadedArtifacts(t, jr) {
		var document map[string]any
		if err := json.Unmarshal([]byte(artifact.content), &document); err != nil {
			t.Fatal(err)
		}
		provenance[artifact.name], _ = document["check_image"].(string)
	}
	wantProvenance := map[string]string{
		"runner-check-install-go-test.json": isolation.ImageDigest(policyCheckImage),
		"runner-check-install-lint.json":    isolation.ImageDigest(policyJobImage),
	}
	if !reflect.DeepEqual(provenance, wantProvenance) {
		t.Fatalf("provenance check images = %v, want %v", provenance, wantProvenance)
	}
}

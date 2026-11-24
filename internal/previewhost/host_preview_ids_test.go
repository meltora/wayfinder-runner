package previewhost

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/journal"
)

func TestDockerCLIHostPreviewIDsFiltersOnlyThisRunner(t *testing.T) {
	log := installFakeDocker(t, `
case "$1 $2" in
  "ps -a") printf 'preview-1\npreview-2\n\n' ;;
  "network ls") printf 'preview-2\npreview-3\n' ;;
  "volume ls") printf 'preview-1\n' ;;
esac`)
	cli := DockerCLI{TempDir: privateDir(t)}
	ids, err := cli.HostPreviewIDs(context.Background(), testRunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"preview-1", "preview-2", "preview-3"}) {
		t.Fatalf("ids = %v", ids)
	}
	calls := readCalls(t, log)
	filter := "--filter label=com.ourplatform.preview-host=runner-1"
	format := `--format {{.Label "com.ourplatform.preview-id"}}`
	for _, prefix := range []string{"ps -a ", "network ls ", "volume ls "} {
		found := false
		for _, call := range calls {
			if strings.HasPrefix(call, prefix) && strings.Contains(call, filter) && strings.Contains(call, format) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no %q call with %q and %q in %v", prefix, filter, format, calls)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %v", calls)
	}
	if _, err := cli.HostPreviewIDs(context.Background(), ""); err == nil {
		t.Fatal("empty runner id was accepted")
	}
	if len(readCalls(t, log)) != 3 {
		t.Fatal("empty runner id reached docker")
	}
}

func TestDockerCLIHostPreviewIDsPropagatesDockerFailure(t *testing.T) {
	installFakeDocker(t, `exit 1`)
	cli := DockerCLI{TempDir: privateDir(t)}
	if _, err := cli.HostPreviewIDs(context.Background(), testRunnerID); err == nil {
		t.Fatal("docker failure was ignored")
	}
}

func TestHostedPreviewIDsReportsValidIDsAndRejectsForeignLabels(t *testing.T) {
	h := newHarness(t)
	controller, engine := h.build(), h.engine
	engine.containers["a"] = ContainerSpec{Name: "a", Labels: PreviewLabels(testRunnerID, "preview-live")}
	engine.containers["b"] = ContainerSpec{Name: "b", Labels: PreviewLabels(testRunnerID, "preview-orphan")}
	engine.containers["c"] = ContainerSpec{Name: "c", Labels: PreviewLabels("runner-other", "preview-foreign")}
	ids, err := controller.HostedPreviewIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"preview-live", "preview-orphan"}) {
		t.Fatalf("ids = %v", ids)
	}
	engine.containers["d"] = ContainerSpec{Name: "d", Labels: PreviewLabels(testRunnerID, "../escape")}
	ids, err = controller.HostedPreviewIDs(context.Background())
	if err == nil {
		t.Fatal("invalid preview id label accepted")
	}
	if !slices.Equal(ids, []string{"preview-live", "preview-orphan"}) {
		t.Fatalf("valid ids not kept beside the invalid one: %v", ids)
	}
}

func TestRecoverRemovesAnOrphanPreviewWithoutAHostRow(t *testing.T) {
	h := newHarness(t)
	controller, engine, stateDir := h.build(), h.engine, h.cfg.StateDir
	engine.containers["orphan"] = ContainerSpec{Name: "orphan", Labels: PreviewLabels(testRunnerID, "preview-orphan")}
	engine.containers["keep"] = ContainerSpec{Name: "keep", Labels: PreviewLabels(testRunnerID, "preview-live")}
	dir := filepath.Join(stateDir, "previews", ResourcePrefix(testRunnerID, "preview-orphan")[len("our-preview-"):])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := controller.Recover(context.Background(), journal.PreviewHost{PreviewID: "preview-orphan"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.containers["orphan"]; ok {
		t.Fatal("orphan container survived")
	}
	if _, ok := engine.containers["keep"]; !ok {
		t.Fatal("live preview container removed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("orphan preview dir survived: %v", err)
	}
}

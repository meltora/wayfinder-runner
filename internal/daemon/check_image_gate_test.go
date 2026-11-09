package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/isolation"
)

const (
	gateGoImage   = "our-runner-check-go:rdbc@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	gateNodeImage = "our-runner-check-node:rdbc@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func withCheckImageInspection(t *testing.T, missing map[string]bool, failure error) *[]string {
	t.Helper()
	var calls []string
	original := inspectCheckImage
	inspectCheckImage = func(_ context.Context, ref string) (string, error) {
		calls = append(calls, ref)
		if failure != nil {
			return "", failure
		}
		if missing[ref] {
			return "", isolation.ErrImageNotPresent
		}
		return "sha256:" + strings.Repeat("9", 64), nil
	}
	t.Cleanup(func() { inspectCheckImage = original })
	return &calls
}

func gateChecks() map[string]config.CheckConfig {
	return map[string]config.CheckConfig{
		"go-vet":   {Command: "go", Args: []string{"vet", "./..."}, Image: gateGoImage},
		"go-test":  {Command: "go", Args: []string{"test", "./..."}, Image: gateGoImage},
		"web-unit": {Command: "npm", Args: []string{"test"}, Image: gateNodeImage},
		"lint":     {Command: "golangci-lint"},
	}
}

func TestCheckImageGateInspectsEachDistinctImageOnce(t *testing.T) {
	for name, test := range map[string]struct {
		checks map[string]config.CheckConfig
		want   []string
	}{
		"no checks":             {nil, nil},
		"no check declares one": {map[string]config.CheckConfig{"lint": {Command: "golangci-lint"}}, nil},
		"one image":             {map[string]config.CheckConfig{"go-test": gateChecks()["go-test"]}, []string{gateGoImage}},
		"several images":        {gateChecks(), []string{gateGoImage, gateNodeImage}},
	} {
		t.Run(name, func(t *testing.T) {
			calls := withCheckImageInspection(t, nil, nil)
			if err := verifyDockerCheckImages(context.Background(), test.checks); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(*calls, test.want) {
				t.Fatalf("inspected = %v, want %v", *calls, test.want)
			}
		})
	}
}

func TestCheckImageGateNamesTheCheckAndImageThatAreMissing(t *testing.T) {
	withCheckImageInspection(t, map[string]bool{gateGoImage: true}, nil)
	err := verifyDockerCheckImages(context.Background(), gateChecks())
	want := "check go-test: image " + gateGoImage + " is not present on the runner's Docker host; pull or build it before starting"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %q", err, want)
	}
	withCheckImageInspection(t, map[string]bool{gateNodeImage: true}, nil)
	err = verifyDockerCheckImages(context.Background(), gateChecks())
	want = "check web-unit: image " + gateNodeImage + " is not present on the runner's Docker host; pull or build it before starting"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %q", err, want)
	}
}

func TestCheckImageGateWrapsAnInspectionFailure(t *testing.T) {
	withCheckImageInspection(t, nil, errors.New("inspect image: Cannot connect to the Docker daemon"))
	err := verifyDockerCheckImages(context.Background(), map[string]config.CheckConfig{"go-test": gateChecks()["go-test"]})
	if err == nil || !strings.HasPrefix(err.Error(), "check go-test: verify image "+gateGoImage+": ") ||
		!strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRefusesToStartWhenACheckImageIsMissing(t *testing.T) {
	cfg := claudeDockerConfig(t)
	cfg.Checks = map[string]config.CheckConfig{"go-test": gateChecks()["go-test"]}
	withProviderCLIProbe(t, compatibleClaudeProbe(t), nil)
	calls := withCheckImageInspection(t, map[string]bool{gateGoImage: true}, nil)
	d, err := New(cfg, nil)
	if err == nil {
		closeConstructedDaemon(d)
		t.Fatal("daemon started without its check image")
	}
	want := "check go-test: image " + gateGoImage + " is not present on the runner's Docker host; pull or build it before starting"
	if err.Error() != want {
		t.Fatalf("err = %q\nwant %q", err, want)
	}
	if !slices.Equal(*calls, []string{gateGoImage}) {
		t.Fatalf("inspected = %v", *calls)
	}
	assertStartRefusalReleasedTheLock(t, cfg)
}

func TestNewStartsWhenEveryCheckImageIsPresent(t *testing.T) {
	cfg := claudeDockerConfig(t)
	cfg.Checks = gateChecks()
	withProviderCLIProbe(t, compatibleClaudeProbe(t), nil)
	calls := withCheckImageInspection(t, nil, nil)
	d, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	closeConstructedDaemon(d)
	if !slices.Equal(*calls, []string{gateGoImage, gateNodeImage}) {
		t.Fatalf("inspected = %v", *calls)
	}
}

func TestNewUnderDirectIsolationWarnsOncePerUnusedCheckImage(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	cfg.Checks = gateChecks()
	calls := withCheckImageInspection(t, nil, nil)
	var logs bytes.Buffer
	d, err := New(cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	closeConstructedDaemon(d)
	if len(*calls) != 0 {
		t.Fatalf("direct isolation inspected images: %v", *calls)
	}
	output := logs.String()
	if count := strings.Count(output, "check image is unused under direct isolation"); count != 3 {
		t.Fatalf("warnings = %d\n%s", count, output)
	}
	for _, id := range []string{"go-test", "go-vet", "web-unit"} {
		if !strings.Contains(output, "check="+id) {
			t.Fatalf("no warning names %s:\n%s", id, output)
		}
	}
	if strings.Contains(output, "check=lint") || strings.Contains(output, "rdbc@sha256") {
		t.Fatalf("warning names a check without an image or prints the reference:\n%s", output)
	}
}

func TestCheckImageGateIgnoresDirectIsolationWithoutImages(t *testing.T) {
	var logs bytes.Buffer
	warnUnusedCheckImages(map[string]config.CheckConfig{"lint": {Command: "golangci-lint"}}, slog.New(slog.NewTextHandler(&logs, nil)))
	if logs.Len() != 0 {
		t.Fatalf("logs = %s", logs.String())
	}
	warnUnusedCheckImages(gateChecks(), nil)
}

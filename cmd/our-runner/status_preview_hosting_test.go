package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/remoteconfig"
)

func statusLinesWithLocal(t *testing.T, write func(stateDir string)) []string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := daemon.WriteRunnerID(stateDir, "runner-123"); err != nil {
		t.Fatal(err)
	}
	if write != nil {
		write(stateDir)
	}
	lines, err := statusReport(stateDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func saveStatusLocal(t *testing.T, stateDir string, hosting config.PreviewHostingConfig) {
	t.Helper()
	local := remoteconfig.Local{ControlPlaneURL: "wss://gateway.example.test/v1/runners/stream", StateDir: stateDir, PreviewHosting: hosting}
	if err := remoteconfig.SaveFiles(stateDir, local, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func TestStatusReportShowsPreviewHosting(t *testing.T) {
	for name, test := range map[string]struct {
		write func(t *testing.T, stateDir string)
		want  string
	}{
		"not configured": {
			want: "preview hosting: off (no runner.local.yaml; run our-runner configure --preview-hosting on, or set preview_hosting.enabled in the --config file)",
		},
		"disabled": {
			write: func(t *testing.T, stateDir string) { saveStatusLocal(t, stateDir, config.PreviewHostingConfig{}) },
			want:  "preview hosting: off (not enabled in runner.local.yaml; run our-runner configure --preview-hosting on)",
		},
		"enabled": {
			write: func(t *testing.T, stateDir string) {
				saveStatusLocal(t, stateDir, config.PreviewHostingConfig{Enabled: true, MaxConcurrent: 2})
			},
			want: "preview hosting: on (at most 2 previews at once)",
		},
		"enabled with defaults": {
			write: func(t *testing.T, stateDir string) {
				saveStatusLocal(t, stateDir, config.PreviewHostingConfig{Enabled: true})
			},
			want: "preview hosting: on (at most 3 previews at once)",
		},
		"unreadable": {
			write: func(t *testing.T, stateDir string) {
				if err := os.WriteFile(filepath.Join(stateDir, remoteconfig.LocalFileName), []byte("preview_hosting: ["), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "preview hosting: unknown (runner.local.yaml could not be read; the runner will not start until it is fixed)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			lines := statusLinesWithLocal(t, func(stateDir string) {
				if test.write != nil {
					test.write(t, stateDir)
				}
			})
			if !slices.Contains(lines, test.want) {
				t.Fatalf("lines = %q", lines)
			}
		})
	}
}

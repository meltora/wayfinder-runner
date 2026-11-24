package isolation_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/previewhost"
)

const (
	jobContainerID     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	previewContainerID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func labelLine(id string, labels map[string]string) string {
	pairs := make([]string, 0, len(labels))
	for key, value := range labels {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	return id + " ," + strings.Join(pairs, ",") + ","
}

func TestRunnerContainerCleanupNeverSelectsPreviewContainers(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "containers")
	removed := filepath.Join(dir, "removed")
	lines := []string{
		labelLine(jobContainerID, map[string]string{
			"com.ourplatform.runner-id": "runner-1", "com.ourplatform.attempt-id": "attempt-1",
		}),
		labelLine(previewContainerID, previewhost.PreviewLabels("runner-1", "preview-1")),
	}
	if err := os.WriteFile(fixture, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
if [ "$1" = ps ]; then
  shift
  filter=""
  while [ $# -gt 0 ]; do
    if [ "$1" = --filter ]; then filter=${2#label=}; shift; fi
    shift
  done
  [ -n "$filter" ] || exit 3
  while read -r id labels; do
    case "$labels" in *",$filter,"*) echo "$id" ;; esac
  done < ` + fixture + `
  exit 0
fi
if [ "$1" = rm ]; then
  shift 2
  for id do echo "$id" >> ` + removed + `; done
  exit 0
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	if err := isolation.RemoveRunnerContainers(context.Background(), "runner-1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(removed)
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Fields(string(raw))
	if len(ids) != 1 || ids[0] != jobContainerID {
		t.Fatalf("runner cleanup removed %v, want only the job container", ids)
	}
}

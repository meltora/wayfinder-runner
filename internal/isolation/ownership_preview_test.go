package isolation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnershipAcceptsPreviewDirectoriesOnly(t *testing.T) {
	layout := newOwnershipLayout(t)
	preview := filepath.Join(layout.state, "previews", "0123456789ab")
	workspace := filepath.Join(preview, "workspace")
	wrongShape := filepath.Join(layout.state, "previews", "preview-1")
	for _, dir := range []string{workspace, wrongShape} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var calls []recordedHelper
	ownership := recordingOwnership(layout, &calls)
	request := OwnershipRequest{RunnerID: "runner-1", AttemptID: "preview-0123456789ab", Purpose: "preview", Workspace: []string{workspace}}
	if err := ownership.HandOff(context.Background(), request); err != nil {
		t.Fatalf("preview workspace hand-off refused: %v", err)
	}
	if err := ownership.Reclaim(context.Background(), OwnershipRequest{
		RunnerID: "runner-1", AttemptID: "preview-0123456789ab", Purpose: "preview", Private: []string{preview},
	}); err != nil {
		t.Fatalf("preview dir reclaim refused: %v", err)
	}
	for _, root := range []string{wrongShape, filepath.Join(layout.state, "previews")} {
		if err := ownership.HandOff(context.Background(), OwnershipRequest{
			RunnerID: "runner-1", AttemptID: "preview-0123456789ab", Purpose: "preview", Workspace: []string{root},
		}); err == nil {
			t.Fatalf("ownership accepted %s", root)
		}
	}
}

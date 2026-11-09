package remoteconfig

import (
	"context"
	"strings"
	"testing"
)

const localCheckImage = "our-runner-check-go:rdbc@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func localImageChecksYAML(image string) string {
	return "checks:\n  go-test:\n    command: go\n    args: [test, ./...]\n    image: " + image + "\n"
}

func TestLoadLocalReadsACheckImageAndMergeKeepsIt(t *testing.T) {
	local := testLocal(t)
	writeLocalFile(t, local, localImageChecksYAML(localCheckImage))
	loaded, err := LoadLocal(local.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Checks["go-test"].Image; got != localCheckImage {
		t.Fatalf("loaded image = %q", got)
	}
	merged, err := Merge(loaded, parsed(t, deliveryBody(dockerConfig())))
	if err != nil {
		t.Fatal(err)
	}
	if got := merged.Config.Checks["go-test"].Image; got != localCheckImage {
		t.Fatalf("merged image = %q", got)
	}
	if merged.Config.JobImage == localCheckImage {
		t.Fatal("the check image replaced the job image")
	}
}

func TestMergeRefusesALocalCheckImageThatIsNotPinnedByDigest(t *testing.T) {
	for name, image := range map[string]string{
		"tag only":       "golang:1.26-bookworm",
		"uppercase hex":  "our-runner-check-go:rdbc@sha256:" + strings.Repeat("C", 64),
		"mixed case hex": "our-runner-check-go:rdbc@sha256:" + strings.Repeat("cC", 32),
	} {
		t.Run(name, func(t *testing.T) {
			local := testLocal(t)
			writeLocalFile(t, local, localImageChecksYAML(image))
			loaded, err := LoadLocal(local.StateDir)
			if err == nil {
				_, err = Merge(loaded, parsed(t, deliveryBody(dockerConfig())))
			}
			if err == nil || !strings.Contains(err.Error(), "check go-test: image must be pinned by digest (name@sha256:<64 lowercase hex>)") {
				t.Fatalf("load or merge err = %v", err)
			}
		})
	}
}

func TestTheRemoteDocumentCannotCarryACheckImage(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"check with an image": func(remote map[string]any) {
			remote["checks"] = map[string]any{"go-test": map[string]any{"command": "go", "image": localCheckImage}}
		},
		"top-level check image": func(remote map[string]any) { remote["check_image"] = localCheckImage },
		"check images map": func(remote map[string]any) {
			remote["check_images"] = map[string]any{"go-test": localCheckImage}
		},
	} {
		t.Run(name, func(t *testing.T) {
			remote := dockerConfig()
			mutate(remote)
			if _, err := ParseDelivery(encode(t, deliveryBody(remote)), testRunnerID); err == nil {
				t.Fatal("a remote check image was accepted")
			}
		})
	}
}

func TestReloadNeverChangesTheRunningCheckImages(t *testing.T) {
	local := testLocal(t)
	writeLocalFile(t, local, localImageChecksYAML(localCheckImage))
	if err := SaveCachedDelivery(local.StateDir, encode(t, deliveryBody(dockerConfig()))); err != nil {
		t.Fatal(err)
	}
	var gateways []string
	started, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(encode(t, deliveryBody(dockerConfig())), nil, &gateways), noSleepPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if got := started.Merged.Config.Checks["go-test"].Image; got != localCheckImage {
		t.Fatalf("start image = %q", got)
	}
	replacement := "our-runner-check-go:other@sha256:" + strings.Repeat("d", 64)
	writeLocalFile(t, local, localImageChecksYAML(replacement))
	next := dockerConfig()
	next["labels"] = map[string]any{"pool": "us-linux"}
	script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(next, strings.Repeat("e", 64)))}}}
	reloader := NewReloader(local.StateDir, testRunnerID, started.Local, started.Delivery, script.fetch)
	reload, err := reloader.Check(context.Background())
	if err != nil || reload.Outcome != ReloadApply {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	if got := reload.Merged.Config.Checks["go-test"].Image; got != localCheckImage {
		t.Fatalf("hot reload image = %q, want the start-time %q", got, localCheckImage)
	}
	restarted, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(encode(t, deliveryBody(dockerConfig())), nil, &gateways), noSleepPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Merged.Config.Checks["go-test"].Image; got != replacement {
		t.Fatalf("restart image = %q", got)
	}
}

package isolation

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

const inspectTestImage = "our-runner-check-go:rdbc@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

func withImageInspectRun(t *testing.T, output string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	original := runImageInspect
	runImageInspect = func(_ context.Context, args []string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		return []byte(output), err
	}
	t.Cleanup(func() { runImageInspect = original })
	return &calls
}

func TestInspectImageReadsTheLocalImageIDWithoutPulling(t *testing.T) {
	id := "sha256:" + strings.Repeat("f", 64)
	calls := withImageInspectRun(t, id+"\n", nil)
	got, err := InspectImage(context.Background(), inspectTestImage)
	if err != nil {
		t.Fatal(err)
	}
	if got != id {
		t.Fatalf("id = %q", got)
	}
	want := []string{"image", "inspect", "--format", "{{.Id}}", "--", inspectTestImage}
	if len(*calls) != 1 || !slices.Equal((*calls)[0], want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestInspectImageReportsAMissingImage(t *testing.T) {
	withImageInspectRun(t, "", errors.New("exit status 1: Error response from daemon: No such image: "+inspectTestImage))
	_, err := InspectImage(context.Background(), inspectTestImage)
	if !errors.Is(err, ErrImageNotPresent) {
		t.Fatalf("err = %v", err)
	}
}

func TestInspectImageWrapsOtherDockerFailures(t *testing.T) {
	withImageInspectRun(t, "", errors.New("exit status 1: Cannot connect to the Docker daemon"))
	_, err := InspectImage(context.Background(), inspectTestImage)
	if err == nil || errors.Is(err, ErrImageNotPresent) || !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Fatalf("err = %v", err)
	}
}

func TestInspectImageRefusesUnexpectedOutput(t *testing.T) {
	for name, output := range map[string]string{
		"empty":     "",
		"not an id": "hello\n",
		"two ids":   "sha256:" + strings.Repeat("f", 64) + "\nsha256:" + strings.Repeat("f", 64) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			withImageInspectRun(t, output, nil)
			if _, err := InspectImage(context.Background(), inspectTestImage); err == nil {
				t.Fatal("unexpected inspect output was accepted")
			}
		})
	}
}

func TestInspectImageRefusesAnInvalidReferenceBeforeRunningDocker(t *testing.T) {
	calls := withImageInspectRun(t, "", nil)
	for _, ref := range []string{"", "-x", "a b", "a\nb"} {
		if _, err := InspectImage(context.Background(), ref); err == nil {
			t.Fatalf("reference %q was accepted", ref)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("docker ran for an invalid reference: %v", *calls)
	}
}

func TestImageDigestReturnsOnlyThePinnedDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("e", 64)
	for ref, want := range map[string]string{
		inspectTestImage: digest,
		"registry.example.com:5000/tools/go@" + digest: digest,
		"golang:1.26":                 "",
		"":                            "",
		"golang@sha256:" + "short":    "",
		"golang@sha512:" + digest[7:]: "",
		"golang@sha256:" + strings.Repeat("A", 64): "sha256:" + strings.Repeat("A", 64),
	} {
		if got := ImageDigest(ref); got != want {
			t.Fatalf("ImageDigest(%q) = %q, want %q", ref, got, want)
		}
	}
}

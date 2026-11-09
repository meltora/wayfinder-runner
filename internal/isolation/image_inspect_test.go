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
		"golang@sha256:" + strings.Repeat("A", 64):               "sha256:" + strings.Repeat("a", 64),
		"golang@sha256:" + strings.Repeat("aBcDeF", 10) + "0F9e": "sha256:" + strings.Repeat("abcdef", 10) + "0f9e",
	} {
		if got := ImageDigest(ref); got != want {
			t.Fatalf("ImageDigest(%q) = %q, want %q", ref, got, want)
		}
	}
}

type inspectStep struct {
	output string
	err    error
}

func withImageInspectSequence(t *testing.T, steps ...inspectStep) *[][]string {
	t.Helper()
	var calls [][]string
	original := runImageInspect
	runImageInspect = func(_ context.Context, args []string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		if len(calls) > len(steps) {
			t.Fatalf("unexpected docker call %v", args)
		}
		step := steps[len(calls)-1]
		return []byte(step.output), step.err
	}
	t.Cleanup(func() { runImageInspect = original })
	return &calls
}

func inspectArgs(ref string) []string {
	return []string{"image", "inspect", "--format", "{{.Id}}", "--", ref}
}

func noSuchImage(ref string) error {
	return errors.New("exit status 1: Error response from daemon: No such image: " + ref)
}

const inspectTestDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

func TestInspectImageReportsAnImageThatResolvesOnlyByID(t *testing.T) {
	calls := withImageInspectSequence(t,
		inspectStep{err: noSuchImage(inspectTestImage)},
		inspectStep{output: inspectTestDigest + "\n"},
	)
	_, err := InspectImage(context.Background(), inspectTestImage)
	if !errors.Is(err, ErrImageResolvesOnlyByID) || errors.Is(err, ErrImageNotPresent) {
		t.Fatalf("err = %v", err)
	}
	want := [][]string{inspectArgs(inspectTestImage), inspectArgs(inspectTestDigest)}
	if !slices.EqualFunc(*calls, want, slices.Equal) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestInspectImageReportsAMissingImageWhenTheIDDoesNotMatch(t *testing.T) {
	for name, second := range map[string]inspectStep{
		"no image with that ID": {err: noSuchImage(inspectTestDigest)},
		"a different ID":        {output: "sha256:" + strings.Repeat("d", 64) + "\n"},
		"unexpected output":     {output: "hello\n"},
	} {
		t.Run(name, func(t *testing.T) {
			calls := withImageInspectSequence(t, inspectStep{err: noSuchImage(inspectTestImage)}, second)
			_, err := InspectImage(context.Background(), inspectTestImage)
			if !errors.Is(err, ErrImageNotPresent) || errors.Is(err, ErrImageResolvesOnlyByID) {
				t.Fatalf("err = %v", err)
			}
			if len(*calls) != 2 || !slices.Equal((*calls)[1], inspectArgs(inspectTestDigest)) {
				t.Fatalf("calls = %v", *calls)
			}
		})
	}
}

func TestInspectImageWrapsAFailureOfTheIDInspection(t *testing.T) {
	withImageInspectSequence(t,
		inspectStep{err: noSuchImage(inspectTestImage)},
		inspectStep{err: errors.New("exit status 1: Cannot connect to the Docker daemon")},
	)
	_, err := InspectImage(context.Background(), inspectTestImage)
	if err == nil || errors.Is(err, ErrImageNotPresent) || errors.Is(err, ErrImageResolvesOnlyByID) ||
		!strings.HasPrefix(err.Error(), "inspect image: ") || !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Fatalf("err = %v", err)
	}
}

func TestInspectImageInspectsByIDOnlyForALowercasePinnedReference(t *testing.T) {
	for _, ref := range []string{
		"golang:1.26",
		"golang",
		"golang@sha256:" + strings.Repeat("E", 64),
		"golang@sha256:" + strings.Repeat("e", 63),
		"sha256:" + strings.Repeat("e", 64),
	} {
		t.Run(ref, func(t *testing.T) {
			calls := withImageInspectSequence(t, inspectStep{err: noSuchImage(ref)})
			_, err := InspectImage(context.Background(), ref)
			if !errors.Is(err, ErrImageNotPresent) {
				t.Fatalf("err = %v", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("calls = %v", *calls)
			}
		})
	}
}

package doctor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/isolation"
)

const (
	doctorGoImage   = "our-runner-check-go:rdbc@sha256:1234567890ab1111111111111111111111111111111111111111111111111111"
	doctorNodeImage = "our-runner-check-node:rdbc@sha256:abcdef0123452222222222222222222222222222222222222222222222222222"
)

func withDoctorImageInspection(t *testing.T, missing, idOnly map[string]bool, failure error) *[]string {
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
		if idOnly[ref] {
			return "", isolation.ErrImageResolvesOnlyByID
		}
		return "sha256:" + strings.Repeat("9", 64), nil
	}
	t.Cleanup(func() { inspectCheckImage = original })
	return &calls
}

func dockerCheckImageOptions(images map[string]string) Options {
	opts := dockerClaudeOptions()
	opts.CheckImages = images
	return opts
}

func TestCheckImagesRowStatuses(t *testing.T) {
	several := map[string]string{"go-test": doctorGoImage, "go-vet": doctorGoImage, "web-unit": doctorNodeImage}
	for name, test := range map[string]struct {
		opts     Options
		missing  map[string]bool
		idOnly   map[string]bool
		failure  error
		status   Status
		detail   string
		inspects []string
	}{
		"direct isolation": {
			opts:   Options{Isolation: "direct", CheckImages: map[string]string{"go-test": doctorGoImage}},
			status: Skip, detail: "check images are unused under direct isolation",
		},
		"no configuration": {
			opts:   Options{CheckImages: map[string]string{"go-test": doctorGoImage}},
			status: Skip, detail: "check images are unused under direct isolation",
		},
		"no check declares an image": {
			opts: dockerCheckImageOptions(nil), status: Skip, detail: "no check declares an image",
		},
		"one image present": {
			opts:   dockerCheckImageOptions(map[string]string{"go-test": doctorGoImage}),
			status: OK, detail: "go-test sha256:1234567890ab", inspects: []string{doctorGoImage},
		},
		"several images present": {
			opts:   dockerCheckImageOptions(several),
			status: OK, detail: "go-test sha256:1234567890ab, go-vet sha256:1234567890ab, web-unit sha256:abcdef012345",
			inspects: []string{doctorGoImage, doctorNodeImage},
		},
		"one image missing": {
			opts: dockerCheckImageOptions(several), missing: map[string]bool{doctorNodeImage: true}, status: Fail,
			detail:   "check web-unit: image " + doctorNodeImage + " is not present on the runner's Docker host",
			inspects: []string{doctorGoImage, doctorNodeImage},
		},
		"every image missing": {
			opts: dockerCheckImageOptions(several), missing: map[string]bool{doctorGoImage: true, doctorNodeImage: true}, status: Fail,
			detail: "check go-test, go-vet: image " + doctorGoImage + " is not present on the runner's Docker host; " +
				"check web-unit: image " + doctorNodeImage + " is not present on the runner's Docker host",
			inspects: []string{doctorGoImage, doctorNodeImage},
		},
		"one image resolves only by ID": {
			opts: dockerCheckImageOptions(several), idOnly: map[string]bool{doctorNodeImage: true}, status: Fail,
			detail:   "check web-unit: image " + doctorNodeImage + " does not resolve by digest; a local image has that digest as its ID",
			inspects: []string{doctorGoImage, doctorNodeImage},
		},
		"several checks share an image that resolves only by ID": {
			opts: dockerCheckImageOptions(several), idOnly: map[string]bool{doctorGoImage: true}, missing: map[string]bool{doctorNodeImage: true}, status: Fail,
			detail: "check go-test, go-vet: image " + doctorGoImage + " does not resolve by digest; a local image has that digest as its ID; " +
				"check web-unit: image " + doctorNodeImage + " is not present on the runner's Docker host",
			inspects: []string{doctorGoImage, doctorNodeImage},
		},
		"uppercase digest shown in lowercase": {
			opts:   dockerCheckImageOptions(map[string]string{"go-test": "our-runner-check-go:rdbc@sha256:ABCDEF0123AB" + strings.Repeat("1", 52)}),
			status: OK, detail: "go-test sha256:abcdef0123ab", inspects: []string{"our-runner-check-go:rdbc@sha256:ABCDEF0123AB" + strings.Repeat("1", 52)},
		},
		"inspection failure": {
			opts: dockerCheckImageOptions(map[string]string{"go-test": doctorGoImage}), failure: errors.New("inspect image: Cannot connect to the Docker daemon"),
			status: Fail, detail: "check go-test: image " + doctorGoImage + ": inspect image: Cannot connect to the Docker daemon",
			inspects: []string{doctorGoImage},
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := withDoctorImageInspection(t, test.missing, test.idOnly, test.failure)
			got := checkCheckImages(context.Background(), test.opts)
			if got.Name != "check-images" || got.Status != test.status || got.Detail != test.detail {
				t.Fatalf("check = %+v\nwant status %d detail %q", got, test.status, test.detail)
			}
			if !slices.Equal(*calls, test.inspects) {
				t.Fatalf("inspected = %v, want %v", *calls, test.inspects)
			}
		})
	}
}

func TestSkipIsReportedWithoutFailingTheReport(t *testing.T) {
	r := Report{Checks: []Check{{Name: "check-images", Status: Skip, Detail: "no check declares an image"}}}
	if r.ExitCode() != 0 || !strings.Contains(Format(r), "[SKIP] check-images: no check declares an image") {
		t.Fatalf("report = %q exit = %d", Format(r), r.ExitCode())
	}
}

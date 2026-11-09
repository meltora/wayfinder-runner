package doctor

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/example/app-runner/internal/isolation"
)

var inspectCheckImage = isolation.InspectImage

const shortDigestLength = len("sha256:") + 12

func checkCheckImages(ctx context.Context, opts Options) Check {
	c := Check{Name: "check-images"}
	if opts.Isolation != "docker" {
		c.Status, c.Detail = Skip, "check images are unused under direct isolation"
		return c
	}
	ids := slices.Sorted(maps.Keys(opts.CheckImages))
	if len(ids) == 0 {
		c.Status, c.Detail = Skip, "no check declares an image"
		return c
	}
	byImage := map[string][]string{}
	var images []string
	for _, id := range ids {
		ref := opts.CheckImages[id]
		if _, seen := byImage[ref]; !seen {
			images = append(images, ref)
		}
		byImage[ref] = append(byImage[ref], id)
	}
	var failures []string
	for _, ref := range images {
		_, err := inspectCheckImage(ctx, ref)
		if errors.Is(err, isolation.ErrImageNotPresent) {
			failures = append(failures, "check "+strings.Join(byImage[ref], ", ")+": image "+ref+" is not present on the runner's Docker host")
		} else if err != nil {
			failures = append(failures, "check "+strings.Join(byImage[ref], ", ")+": image "+ref+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		c.Status, c.Detail = Fail, strings.Join(failures, "; ")
		return c
	}
	present := make([]string, 0, len(ids))
	for _, id := range ids {
		present = append(present, id+" "+shortDigest(opts.CheckImages[id]))
	}
	c.Status, c.Detail = OK, strings.Join(present, ", ")
	return c
}

func shortDigest(ref string) string {
	digest := isolation.ImageDigest(ref)
	if len(digest) < shortDigestLength {
		return digest
	}
	return digest[:shortDigestLength]
}

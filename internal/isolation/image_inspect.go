package isolation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrImageNotPresent = errors.New("image is not present on the Docker host")

var runImageInspect = runDockerProbe

var (
	inspectedImageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	pinnedDigestPattern     = regexp.MustCompile(`^[^\s@]+@(sha256:[a-fA-F0-9]{64})$`)
)

func InspectImage(ctx context.Context, ref string) (string, error) {
	if err := validateContainerImage(ref); err != nil {
		return "", err
	}
	output, err := runImageInspect(ctx, []string{"image", "inspect", "--format", "{{.Id}}", "--", ref})
	if err != nil {
		if strings.Contains(err.Error(), "No such image") {
			return "", ErrImageNotPresent
		}
		return "", fmt.Errorf("inspect image: %w", err)
	}
	id := strings.TrimSpace(string(output))
	if !inspectedImageIDPattern.MatchString(id) {
		return "", fmt.Errorf("inspect image: Docker returned an unexpected image ID")
	}
	return id, nil
}

func ImageDigest(ref string) string {
	match := pinnedDigestPattern.FindStringSubmatch(ref)
	if match == nil {
		return ""
	}
	return match[1]
}

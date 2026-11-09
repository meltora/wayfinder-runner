package isolation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrImageNotPresent       = errors.New("image is not present on the Docker host")
	ErrImageResolvesOnlyByID = errors.New("image does not resolve by digest, but a local image has that digest as its ID")
)

var runImageInspect = runDockerProbe

var (
	inspectedImageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	pinnedDigestPattern     = regexp.MustCompile(`^[^\s@]+@(sha256:[a-fA-F0-9]{64})$`)
	lowercasePinnedPattern  = regexp.MustCompile(`^[^\s@]+@(sha256:[a-f0-9]{64})$`)
)

func InspectImage(ctx context.Context, ref string) (string, error) {
	if err := validateContainerImage(ref); err != nil {
		return "", err
	}
	id, found, err := inspectImageID(ctx, ref)
	if err != nil {
		return "", err
	}
	if found {
		return id, nil
	}
	match := lowercasePinnedPattern.FindStringSubmatch(ref)
	if match == nil {
		return "", ErrImageNotPresent
	}
	id, found, err = inspectImageID(ctx, match[1])
	if err != nil && !errors.Is(err, errUnexpectedImageID) {
		return "", err
	}
	if found && id == match[1] {
		return "", ErrImageResolvesOnlyByID
	}
	return "", ErrImageNotPresent
}

var errUnexpectedImageID = errors.New("inspect image: Docker returned an unexpected image ID")

func inspectImageID(ctx context.Context, ref string) (string, bool, error) {
	output, err := runImageInspect(ctx, []string{"image", "inspect", "--format", "{{.Id}}", "--", ref})
	if err != nil {
		if strings.Contains(err.Error(), "No such image") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect image: %w", err)
	}
	id := strings.TrimSpace(string(output))
	if !inspectedImageIDPattern.MatchString(id) {
		return "", false, errUnexpectedImageID
	}
	return id, true, nil
}

func ImageDigest(ref string) string {
	match := pinnedDigestPattern.FindStringSubmatch(ref)
	if match == nil {
		return ""
	}
	return strings.ToLower(match[1])
}

package isolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxOwnershipRoots      = 8
	ownershipHandOffScript = `set -eu; workspace=$1; [ "$2" = -- ]; shift 2; index=0; for root do index=$((index + 1)); if [ "$index" -le "$workspace" ]; then chmod -R g+rX -- "$root"; chown -hR 10001 -- "$root"; else chown -hR 10001:10001 -- "$root"; fi; done`
)

var (
	ownershipPurposePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	ownershipParentPatterns = map[string]*regexp.Regexp{
		"jobs":                  regexp.MustCompile(`^job-[A-Za-z0-9]+$`),
		"job-homes":             regexp.MustCompile(`^home-[A-Za-z0-9]+$`),
		"check-homes":           regexp.MustCompile(`^home-[A-Za-z0-9]+$`),
		"sockets":               regexp.MustCompile(`^job-[A-Za-z0-9]+$`),
		"job-results":           regexp.MustCompile(`^job-[A-Za-z0-9]+\.(checks|npm-cache)$`),
		"work-intake-proposals": regexp.MustCompile(`^execution-[A-Za-z0-9]+$`),
		"doctor":                regexp.MustCompile(`^sandbox-[A-Za-z0-9]+$`),
		"previews":              regexp.MustCompile(`^[0-9a-f]{12}$`),
	}
)

type OwnershipRequest struct {
	RunnerID  string
	AttemptID string
	Purpose   string
	Workspace []string
	Private   []string
}

type Ownership interface {
	HandOff(context.Context, OwnershipRequest) error
	Reclaim(context.Context, OwnershipRequest) error
}

type DockerOwnership struct {
	Mapping      UserMapping
	Image        string
	StateDir     string
	EffectiveUID func() int
	Run          func(ctx context.Context, name string, args []string) error
}

func (o DockerOwnership) HandOff(ctx context.Context, request OwnershipRequest) error {
	name, args, err := o.helperArgs(request, false)
	if err != nil {
		return err
	}
	args = append(args, "sh", "-c", ownershipHandOffScript, "our-runner-ownership", strconv.Itoa(len(request.Workspace)), "--")
	args = append(args, request.roots()...)
	if err := o.run(ctx, name, args); err != nil {
		return fmt.Errorf("hand job trees to the container user: %w", err)
	}
	return nil
}

func (o DockerOwnership) Reclaim(ctx context.Context, request OwnershipRequest) error {
	name, args, err := o.helperArgs(request, true)
	if err != nil {
		return err
	}
	args = append(args, "chown", "-hR", "0:0", "--")
	args = append(args, request.roots()...)
	if err := o.run(ctx, name, args); err != nil {
		return fmt.Errorf("re-own job output for the runner: %w", err)
	}
	return nil
}

func ReclaimInBatches(ctx context.Context, ownership Ownership, runnerID, attemptID string, roots []string) error {
	for start, batch := 0, 1; start < len(roots); start, batch = start+maxOwnershipRoots, batch+1 {
		end := min(start+maxOwnershipRoots, len(roots))
		request := OwnershipRequest{
			RunnerID: runnerID, AttemptID: attemptID, Purpose: "recover-" + strconv.Itoa(batch),
			Private: append([]string(nil), roots[start:end]...),
		}
		if err := ownership.Reclaim(ctx, request); err != nil {
			return err
		}
	}
	return nil
}

func (r OwnershipRequest) roots() []string {
	return append(append([]string(nil), r.Workspace...), r.Private...)
}

func (o DockerOwnership) helperArgs(request OwnershipRequest, reclaim bool) (string, []string, error) {
	if err := o.Mapping.Validate(); err != nil || !o.Mapping.RequiresOwnershipHandOff() {
		return "", nil, fmt.Errorf("the ownership helper requires the rootless Docker mapping")
	}
	if hostRoot(o.EffectiveUID) {
		return "", nil, ErrHostRoot
	}
	if err := validateContainerImage(o.Image); err != nil {
		return "", nil, err
	}
	if !runnerLabelPattern.MatchString(request.RunnerID) || !runnerLabelPattern.MatchString(request.AttemptID) {
		return "", nil, fmt.Errorf("the ownership helper requires runner and attempt identity")
	}
	if !ownershipPurposePattern.MatchString(request.Purpose) {
		return "", nil, fmt.Errorf("the ownership helper purpose is invalid")
	}
	if o.StateDir == "" || !filepath.IsAbs(o.StateDir) || filepath.Clean(o.StateDir) != o.StateDir {
		return "", nil, fmt.Errorf("the ownership helper requires an absolute clean state directory")
	}
	roots := request.roots()
	if len(roots) == 0 || len(roots) > maxOwnershipRoots {
		return "", nil, fmt.Errorf("the ownership helper takes between 1 and %d roots", maxOwnershipRoots)
	}
	seen := map[string]bool{}
	for _, root := range roots {
		if seen[root] {
			return "", nil, fmt.Errorf("the ownership helper received a duplicate root")
		}
		seen[root] = true
		if err := validateOwnershipRoot(o.StateDir, root); err != nil {
			return "", nil, err
		}
	}
	name := ContainerName(request.RunnerID, request.AttemptID+"-own-"+request.Purpose)
	args := []string{"run", "--rm", "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "CHOWN"}
	if reclaim {
		args = append(args, "--cap-add", "DAC_READ_SEARCH")
	}
	args = append(args,
		"--security-opt", "no-new-privileges", "--network", "none", "--read-only", "--log-driver", "none",
		"--pids-limit", "64", "--memory", "128m", "--name", name,
		"--label", "com.ourplatform.runner-id="+request.RunnerID,
		"--label", "com.ourplatform.attempt-id="+request.AttemptID,
	)
	for _, root := range roots {
		args = append(args, "-v", root+":"+root+":rw")
	}
	return name, append(args, o.Image), nil
}

func validateOwnershipRoot(stateDir, root string) error {
	if strings.ContainsAny(root, ":,\x00\r\n") {
		return fmt.Errorf("ownership root contains an unsupported character")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("ownership root must be an absolute clean path")
	}
	rel, err := filepath.Rel(stateDir, root)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("ownership root %s is not inside a per-job directory", root)
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	pattern, ok := ownershipParentPatterns[parts[0]]
	if len(parts) < 2 || !ok || !pattern.MatchString(parts[1]) {
		return fmt.Errorf("ownership root %s is not inside a per-job directory", root)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("ownership root %s must be a real directory", root)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return fmt.Errorf("ownership root %s must be a real directory without symbolic link ancestors", root)
	}
	return nil
}

func (o DockerOwnership) run(ctx context.Context, name string, args []string) error {
	if o.Run != nil {
		return o.Run(ctx, name, args)
	}
	// #nosec G204 -- executable is Docker, flags and the helper command are fixed, and every root is a validated per-job directory passed as one argv item.
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnvironment()
	var output cappedProbeBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		removeErr := removeContainers(cleanupCtx, []string{name})
		cancel()
		if removeErr != nil {
			return fmt.Errorf("%w: %s (helper cleanup: %v)", err, strings.TrimSpace(output.String()), removeErr)
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

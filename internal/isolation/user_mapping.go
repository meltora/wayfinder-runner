package isolation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type UserMappingMode string

const (
	UserMappingIdentity UserMappingMode = "identity"
	UserMappingRootless UserMappingMode = "rootless"
)

const (
	JobUserID         = 10001
	JobGroupID        = 10001
	maxUserMapEntries = 340
	userMappingScript = "cat /proc/self/uid_map; echo --; cat /proc/self/gid_map; echo --; id -u runner; id -g runner"
)

var ErrHostRoot = errors.New("docker isolation must not run as host root")

var runnerLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type UserMapping struct {
	Mode         UserMappingMode
	ContainerUID int
	ContainerGID int
	HostUID      int
	HostGID      int
}

func (m UserMapping) User() string {
	return strconv.Itoa(m.ContainerUID) + ":" + strconv.Itoa(m.ContainerGID)
}

func (m UserMapping) RequiresOwnershipHandOff() bool {
	return m.Mode == UserMappingRootless
}

func (m UserMapping) Validate() error {
	switch m.Mode {
	case UserMappingIdentity:
	case UserMappingRootless:
		if m.ContainerUID != JobUserID || m.ContainerGID != JobGroupID {
			return fmt.Errorf("rootless Docker job user must be %d:%d", JobUserID, JobGroupID)
		}
	default:
		return fmt.Errorf("Docker user mapping has not been resolved")
	}
	if m.ContainerUID <= 0 || m.ContainerGID <= 0 {
		return fmt.Errorf("Docker job user must not be container root")
	}
	return nil
}

func (m UserMapping) Describe() string {
	switch m.Mode {
	case UserMappingRootless:
		return fmt.Sprintf("rootless: job user %s (host %d:%d); job output is re-owned after each container", m.User(), m.HostUID, m.HostGID)
	case UserMappingIdentity:
		return fmt.Sprintf("identity mapping: job user %s is the runner's own host user", m.User())
	}
	return "unresolved"
}

type userMapExtent struct {
	inside, outside, length uint64
}

func ParseUserMapping(hostUID, hostGID int, output string) (UserMapping, error) {
	if hostUID == 0 {
		return UserMapping{}, ErrHostRoot
	}
	if hostUID < 0 || hostGID < 0 {
		return UserMapping{}, fmt.Errorf("the runner's host ids are invalid")
	}
	uidText, gidText, imageUser, err := splitUserMappingOutput(output)
	if err != nil {
		return UserMapping{}, err
	}
	uidMap, err := parseUserMap(uidText)
	if err != nil {
		return UserMapping{}, err
	}
	gidMap, err := parseUserMap(gidText)
	if err != nil {
		return UserMapping{}, err
	}
	hostU, hostG := uint64(hostUID), uint64(hostGID)
	containerUID, mapped := toContainerID(uidMap, hostU)
	if !mapped {
		for _, extent := range uidMap {
			if extent.inside == 0 && extent.length == 1 {
				return UserMapping{}, fmt.Errorf("the Docker daemon is rootless under another host user (uid %d)", extent.outside)
			}
		}
		return UserMapping{}, fmt.Errorf("the runner's host uid %d is not mapped into the Docker daemon's user namespace; userns-remap daemons are not supported yet", hostUID)
	}
	gidMismatch := fmt.Errorf("the daemon's gid map does not match its uid map")
	if containerUID != 0 {
		containerGID, ok := toContainerID(gidMap, hostG)
		if !ok || containerGID == 0 {
			return UserMapping{}, gidMismatch
		}
		uid, uidErr := mappedID(containerUID)
		gid, gidErr := mappedID(containerGID)
		if err := errors.Join(uidErr, gidErr); err != nil {
			return UserMapping{}, err
		}
		return UserMapping{
			Mode: UserMappingIdentity, ContainerUID: uid, ContainerGID: gid,
			HostUID: hostUID, HostGID: hostGID,
		}, nil
	}
	if !hasExactExtent(uidMap, hostU) {
		return UserMapping{}, fmt.Errorf("the runner's host uid maps to container root in an unexpected range")
	}
	if !hasExactExtent(gidMap, hostG) {
		return UserMapping{}, gidMismatch
	}
	if imageUser[0] != strconv.Itoa(JobUserID) || imageUser[1] != strconv.Itoa(JobGroupID) {
		return UserMapping{}, fmt.Errorf("the job image user runner is %s:%s, expected %d:%d", imageUser[0], imageUser[1], JobUserID, JobGroupID)
	}
	jobHostUID, uidOK := toHostID(uidMap, JobUserID)
	jobHostGID, gidOK := toHostID(gidMap, JobGroupID)
	if !uidOK || !gidOK {
		return UserMapping{}, fmt.Errorf("the daemon's subordinate id range does not map the job user %d", JobUserID)
	}
	for _, id := range []uint64{jobHostUID, jobHostGID} {
		if id == 0 || id == hostU || id == hostG {
			return UserMapping{}, fmt.Errorf("the job user maps to host root or the runner's own host ids")
		}
	}
	uid, uidErr := mappedID(jobHostUID)
	gid, gidErr := mappedID(jobHostGID)
	if err := errors.Join(uidErr, gidErr); err != nil {
		return UserMapping{}, err
	}
	return UserMapping{
		Mode: UserMappingRootless, ContainerUID: JobUserID, ContainerGID: JobGroupID,
		HostUID: uid, HostGID: gid,
	}, nil
}

func mappedID(value uint64) (int, error) {
	if value >= math.MaxUint32 || value > math.MaxInt {
		return 0, fmt.Errorf("mapped id %d is not a valid host id", value)
	}
	return int(value), nil
}

func splitUserMappingOutput(output string) (string, string, [2]string, error) {
	malformed := fmt.Errorf("the user namespace map probe output is malformed")
	sections := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n--\n")
	if len(sections) != 3 {
		return "", "", [2]string{}, malformed
	}
	ids := strings.Fields(sections[2])
	if len(ids) != 2 {
		return "", "", [2]string{}, malformed
	}
	return sections[0], sections[1], [2]string{ids[0], ids[1]}, nil
}

func parseUserMap(text string) ([]userMapExtent, error) {
	var extents []userMapExtent
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(extents) == maxUserMapEntries {
			return nil, fmt.Errorf("the user namespace map has more than %d entries", maxUserMapEntries)
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("the user namespace map is malformed")
		}
		var values [3]uint64
		for index, field := range fields {
			value, err := strconv.ParseUint(field, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("the user namespace map is malformed")
			}
			values[index] = value
		}
		if values[2] == 0 || values[0]+values[2] > 1<<32 || values[1]+values[2] > 1<<32 {
			return nil, fmt.Errorf("the user namespace map is malformed")
		}
		extents = append(extents, userMapExtent{inside: values[0], outside: values[1], length: values[2]})
	}
	if len(extents) == 0 {
		return nil, fmt.Errorf("the user namespace map is empty")
	}
	return extents, nil
}

func toContainerID(extents []userMapExtent, host uint64) (uint64, bool) {
	for _, extent := range extents {
		if host >= extent.outside && host < extent.outside+extent.length {
			return extent.inside + host - extent.outside, true
		}
	}
	return 0, false
}

func toHostID(extents []userMapExtent, container uint64) (uint64, bool) {
	for _, extent := range extents {
		if container >= extent.inside && container < extent.inside+extent.length {
			return extent.outside + container - extent.inside, true
		}
	}
	return 0, false
}

func hasExactExtent(extents []userMapExtent, host uint64) bool {
	for _, extent := range extents {
		if extent.inside == 0 && extent.outside == host && extent.length == 1 {
			return true
		}
	}
	return false
}

type UserMappingResolver struct {
	EffectiveUID func() int
	HostUID      func() int
	HostGID      func() int
	Run          func(context.Context, []string) ([]byte, error)
}

func ResolveUserMapping(ctx context.Context, image, runnerID string) (UserMapping, error) {
	return UserMappingResolver{}.Resolve(ctx, image, runnerID)
}

func (r UserMappingResolver) Resolve(ctx context.Context, image, runnerID string) (UserMapping, error) {
	if hostRoot(r.EffectiveUID) {
		return UserMapping{}, ErrHostRoot
	}
	if err := validateContainerImage(image); err != nil {
		return UserMapping{}, err
	}
	if !runnerLabelPattern.MatchString(runnerID) {
		return UserMapping{}, fmt.Errorf("Docker user mapping probe requires a valid runner label")
	}
	args := []string{
		"run", "--rm", "--user", "0:0", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--network", "none", "--read-only", "--log-driver", "none", "--pids-limit", "16", "--memory", "64m",
		"--label", "com.ourplatform.runner-id=" + runnerID, image,
		"sh", "-c", userMappingScript,
	}
	run := r.Run
	if run == nil {
		run = runDockerProbe
	}
	output, err := run(ctx, args)
	if err != nil {
		return UserMapping{}, fmt.Errorf("read the Docker daemon's user namespace map: %w", err)
	}
	hostUID, hostGID := os.Getuid, os.Getgid
	if r.HostUID != nil {
		hostUID = r.HostUID
	}
	if r.HostGID != nil {
		hostGID = r.HostGID
	}
	return ParseUserMapping(hostUID(), hostGID(), string(output))
}

func hostRoot(effectiveUID func() int) bool {
	if effectiveUID == nil {
		effectiveUID = os.Geteuid
	}
	return effectiveUID() == 0
}

func validateContainerImage(image string) error {
	if image == "" || strings.HasPrefix(image, "-") || strings.IndexFunc(image, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return fmt.Errorf("Docker isolation requires a valid job image reference")
	}
	return nil
}

func runDockerProbe(ctx context.Context, args []string) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// #nosec G204 -- executable is Docker, flags are fixed, and the image and runner label are validated argv items.
	cmd := exec.CommandContext(probeCtx, "docker", args...)
	cmd.Env = dockerEnvironment()
	var stderr cappedProbeBuffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if len(output) > 64<<10 {
		return nil, fmt.Errorf("Docker user mapping probe output exceeds 64 KiB")
	}
	return output, nil
}

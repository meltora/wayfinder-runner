package isolation

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func mappingOutput(uidMap, gidMap, runnerUID, runnerGID string) string {
	return uidMap + "\n--\n" + gidMap + "\n--\n" + runnerUID + "\n" + runnerGID + "\n"
}

const rootlessMap = "         0       1000          1\n         1     100000      65536"

func TestParseUserMappingDecidesTheModeFromTheObservedMap(t *testing.T) {
	for name, test := range map[string]struct {
		hostUID, hostGID int
		output           string
		want             UserMapping
	}{
		"rootless": {
			hostUID: 1000, hostGID: 1000,
			output: mappingOutput(rootlessMap, rootlessMap, "10001", "10001"),
			want:   UserMapping{Mode: UserMappingRootless, ContainerUID: 10001, ContainerGID: 10001, HostUID: 110000, HostGID: 110000},
		},
		"identity linux": {
			hostUID: 1000, hostGID: 1000,
			output: mappingOutput("0 0 4294967295", "0 0 4294967295", "10001", "10001"),
			want:   UserMapping{Mode: UserMappingIdentity, ContainerUID: 1000, ContainerGID: 1000, HostUID: 1000, HostGID: 1000},
		},
		"identity desktop": {
			hostUID: 501, hostGID: 20,
			output: mappingOutput("0 0 4294967295", "0 0 4294967295", "10001", "10001"),
			want:   UserMapping{Mode: UserMappingIdentity, ContainerUID: 501, ContainerGID: 20, HostUID: 501, HostGID: 20},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseUserMapping(test.hostUID, test.hostGID, test.output)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("mapping = %+v, want %+v", got, test.want)
			}
			if got.RequiresOwnershipHandOff() != (test.want.Mode == UserMappingRootless) {
				t.Fatalf("hand-off required = %v for %s", got.RequiresOwnershipHandOff(), got.Mode)
			}
			if got.User() != strconv.Itoa(test.want.ContainerUID)+":"+strconv.Itoa(test.want.ContainerGID) {
				t.Fatalf("user = %q", got.User())
			}
		})
	}
}

func TestParseUserMappingRefusesEveryUnsupportedShapeWithADistinctReason(t *testing.T) {
	tooMany := strings.Repeat("0 0 1\n", 341)
	cases := map[string]struct {
		output string
		want   string
	}{
		"userns remap":           {mappingOutput("0 100000 65536", "0 100000 65536", "10001", "10001"), "userns-remap"},
		"rootless of other user": {mappingOutput("0 1001 1\n1 100000 65536", "0 1001 1\n1 100000 65536", "10001", "10001"), "another host user"},
		"range too small":        {mappingOutput("0 1000 1\n1 100000 9000", "0 1000 1\n1 100000 9000", "10001", "10001"), "does not map the job user"},
		"image runner differs":   {mappingOutput(rootlessMap, rootlessMap, "1000", "1000"), "expected 10001:10001"},
		"gid map disagrees":      {mappingOutput(rootlessMap, "0 0 4294967295", "10001", "10001"), "gid map does not match"},
		"empty":                  {mappingOutput("", "", "10001", "10001"), "map is empty"},
		"malformed":              {mappingOutput("0 x 1", "0 x 1", "10001", "10001"), "malformed"},
		"too many lines":         {mappingOutput(tooMany, tooMany, "10001", "10001"), "more than 340"},
		"missing sections":       {"0 1000 1", "malformed"},
		"job user is the overflow id": {
			mappingOutput("0 1000 1\n1 4294957295 10001", "0 1000 1\n1 4294957295 10001", "10001", "10001"),
			"not a valid host id",
		},
	}
	seen := map[string]string{}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseUserMapping(1000, 1000, test.output)
			if err == nil {
				t.Fatal("unsupported mapping accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
		if _, err := ParseUserMapping(1000, 1000, test.output); err != nil && name != "missing sections" {
			if previous, ok := seen[err.Error()]; ok {
				t.Fatalf("%s and %s share the reason %q", name, previous, err.Error())
			}
			seen[err.Error()] = name
		}
	}
}

func TestParseUserMappingRefusesHostRootWhateverTheMapSays(t *testing.T) {
	for _, output := range []string{
		mappingOutput("0 0 4294967295", "0 0 4294967295", "10001", "10001"),
		mappingOutput("0 0 1\n1 100000 65536", "0 0 1\n1 100000 65536", "10001", "10001"),
	} {
		_, err := ParseUserMapping(0, 0, output)
		if !errors.Is(err, ErrHostRoot) || err.Error() != "docker isolation must not run as host root" {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestResolveUserMappingRunsOneFixedProbeContainer(t *testing.T) {
	var calls [][]string
	resolver := UserMappingResolver{
		EffectiveUID: func() int { return 1000 },
		HostUID:      func() int { return 1000 },
		HostGID:      func() int { return 1000 },
		Run: func(_ context.Context, args []string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			return []byte(mappingOutput(rootlessMap, rootlessMap, "10001", "10001")), nil
		},
	}
	image := "registry.example/job@sha256:" + strings.Repeat("a", 64)
	mapping, err := resolver.Resolve(context.Background(), image, "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if mapping.User() != "10001:10001" || len(calls) != 1 {
		t.Fatalf("mapping=%+v calls=%d", mapping, len(calls))
	}
	want := []string{
		"run", "--rm", "--user", "0:0", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--network", "none", "--read-only", "--log-driver", "none", "--pids-limit", "16", "--memory", "64m",
		"--label", "com.ourplatform.runner-id=runner-1", image,
		"sh", "-c", "cat /proc/self/uid_map; echo --; cat /proc/self/gid_map; echo --; id -u runner; id -g runner",
	}
	if strings.Join(calls[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q\nwant %q", calls[0], want)
	}
}

func TestResolveUserMappingRefusesHostRootBeforeRunningDocker(t *testing.T) {
	ran := false
	resolver := UserMappingResolver{
		EffectiveUID: func() int { return 0 },
		HostUID:      func() int { return 0 },
		HostGID:      func() int { return 0 },
		Run: func(context.Context, []string) ([]byte, error) {
			ran = true
			return []byte(mappingOutput("0 0 4294967295", "0 0 4294967295", "10001", "10001")), nil
		},
	}
	if _, err := resolver.Resolve(context.Background(), "image@sha256:"+strings.Repeat("a", 64), "runner-1"); !errors.Is(err, ErrHostRoot) || ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
}

func TestResolveUserMappingRejectsUnsafeInputsAndProbeFailures(t *testing.T) {
	resolver := UserMappingResolver{
		EffectiveUID: func() int { return 1000 }, HostUID: func() int { return 1000 }, HostGID: func() int { return 1000 },
		Run: func(context.Context, []string) ([]byte, error) { return nil, errors.New("daemon down") },
	}
	for _, image := range []string{"", "-v", "image name"} {
		if _, err := resolver.Resolve(context.Background(), image, "runner-1"); err == nil {
			t.Fatalf("image %q accepted", image)
		}
	}
	if _, err := resolver.Resolve(context.Background(), "image", "bad id"); err == nil {
		t.Fatal("invalid runner label accepted")
	}
	if _, err := resolver.Resolve(context.Background(), "image", "runner-1"); err == nil || !strings.Contains(err.Error(), "daemon down") {
		t.Fatalf("probe failure err = %v", err)
	}
}

func TestUserMappingDescribeNamesTheModeAndHostIDs(t *testing.T) {
	rootless := UserMapping{Mode: UserMappingRootless, ContainerUID: 10001, ContainerGID: 10001, HostUID: 110000, HostGID: 110000}
	if got := rootless.Describe(); got != "rootless: job user 10001:10001 (host 110000:110000); job output is re-owned after each container" {
		t.Fatalf("rootless description = %q", got)
	}
	identity := UserMapping{Mode: UserMappingIdentity, ContainerUID: 1000, ContainerGID: 1000, HostUID: 1000, HostGID: 1000}
	if got := identity.Describe(); got != "identity mapping: job user 1000:1000 is the runner's own host user" {
		t.Fatalf("identity description = %q", got)
	}
	if got := (UserMapping{}).Describe(); got != "unresolved" {
		t.Fatalf("zero description = %q", got)
	}
}

func TestUserMappingValidateRefusesZeroOrUnknownModes(t *testing.T) {
	for _, mapping := range []UserMapping{
		{},
		{Mode: UserMappingRootless, ContainerUID: 0, ContainerGID: 10001},
		{Mode: "remap", ContainerUID: 10001, ContainerGID: 10001},
	} {
		if err := mapping.Validate(); err == nil {
			t.Fatalf("mapping %+v accepted", mapping)
		}
	}
}

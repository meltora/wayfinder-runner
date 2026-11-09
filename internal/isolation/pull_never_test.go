package isolation

import (
	"slices"
	"strings"
	"testing"
)

const pullNeverImage = "our-runner-check-go:v1@sha256:3333333333333333333333333333333333333333333333333333333333333333"

func pullNeverPolicies() map[string]Policy {
	return map[string]Policy{
		"check": {
			User: testJobUser, NetworkNone: true, ReadOnlyFS: true, WorkDir: "/w/repo", WorkspaceRoot: "/w",
			JobHome: "/job-home", RunnerID: "runner-1", AttemptID: "attempt-1", Image: pullNeverImage,
		},
		"provider": {
			User: testJobUser, NetworkNone: true, ReadOnlyFS: true, WorkDir: "/w/repo", WorkspaceRoot: "/w",
			JobHome: "/job-home", MCPSock: "/run/job/mcp.sock", MCPTokenFile: "/run/job/token",
			RunnerID: "runner-1", AttemptID: "attempt-1", NestedSandbox: true, UnprivilegedBwrap: true, Image: pullNeverImage,
		},
		"consultation": {
			User: testJobUser, NetworkName: "consult-net", ReadOnlyFS: true, WorkDir: "/w",
			RunnerID: "runner-1", AttemptID: "attempt-1", DisableLogs: true,
		},
	}
}

func TestPullNeverAddsOnlyThePullFlagBeforeTheImage(t *testing.T) {
	for name, policy := range pullNeverPolicies() {
		t.Run(name, func(t *testing.T) {
			_, without, err := (Docker{}).Wrap("go", []string{"test", "./..."}, policy)
			if err != nil {
				t.Fatal(err)
			}
			policy.PullNever = true
			_, with, err := (Docker{}).Wrap("go", []string{"test", "./..."}, policy)
			if err != nil {
				t.Fatal(err)
			}
			image := slices.Index(without, policy.Image)
			if policy.Image == "" {
				image = slices.Index(without, DefaultJobImage)
			}
			if image < 0 {
				t.Fatalf("image missing from %q", without)
			}
			want := slices.Concat(without[:image], []string{"--pull=never"}, without[image:])
			if !slices.Equal(with, want) {
				t.Fatalf("pull never argv:\ngot  %q\nwant %q", with, want)
			}
		})
	}
}

func TestWrapWithoutPullNeverNeverNamesAPullPolicy(t *testing.T) {
	for name, policy := range pullNeverPolicies() {
		t.Run(name, func(t *testing.T) {
			_, args, err := (Docker{}).Wrap("go", []string{"test", "./..."}, policy)
			if err != nil {
				t.Fatal(err)
			}
			for _, arg := range args {
				if strings.HasPrefix(arg, "--pull") {
					t.Fatalf("argv without pull never carries %q: %q", arg, args)
				}
			}
		})
	}
}

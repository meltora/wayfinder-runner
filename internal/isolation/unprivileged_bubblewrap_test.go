package isolation

import (
	"strings"
	"testing"
)

func unprivilegedBubblewrapPolicy(enabled, nested bool) Policy {
	return Policy{
		NetworkNone: true, ReadOnlyFS: true, WorkDir: "/workspace", RunnerID: "runner-1", AttemptID: "attempt-1",
		User: testJobUser, NestedSandbox: nested, UnprivilegedBwrap: enabled,
	}
}

func pathArguments(args []string) []string {
	var paths []string
	for index, argument := range args {
		if argument == "-e" && index+1 < len(args) && strings.HasPrefix(args[index+1], "PATH=") {
			paths = append(paths, args[index+1])
		}
	}
	return paths
}

func TestDockerWrapPrependsTheUnprivilegedBubblewrapDirectory(t *testing.T) {
	d := Docker{LookPath: func(string) (string, error) { return "/usr/bin/docker", nil }}
	_, args, err := d.Wrap("claude", []string{"-p", "hi"}, unprivilegedBubblewrapPolicy(true, true))
	if err != nil {
		t.Fatal(err)
	}
	want := "PATH=/usr/local/lib/our-runner/unprivileged-bwrap:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	if paths := pathArguments(args); len(paths) != 1 || paths[0] != want {
		t.Fatalf("PATH arguments = %q", paths)
	}
	image := -1
	for index, argument := range args {
		if argument == DefaultJobImage {
			image = index
		}
	}
	for index, argument := range args {
		if argument == want && (image < 0 || index > image) {
			t.Fatalf("PATH is not a docker run option: %q", args)
		}
	}
}

func TestDockerWrapKeepsTheImagePathByDefault(t *testing.T) {
	d := Docker{LookPath: func(string) (string, error) { return "/usr/bin/docker", nil }}
	for _, nested := range []bool{true, false} {
		_, args, err := d.Wrap("codex", nil, unprivilegedBubblewrapPolicy(false, nested))
		if err != nil {
			t.Fatal(err)
		}
		if paths := pathArguments(args); len(paths) != 0 {
			t.Fatalf("nested=%v PATH arguments = %q", nested, paths)
		}
	}
}

func TestDockerWrapRefusesUnprivilegedBubblewrapWithoutTheNestedSandbox(t *testing.T) {
	d := Docker{LookPath: func(string) (string, error) { return "/usr/bin/docker", nil }}
	if _, _, err := d.Wrap("claude", nil, unprivilegedBubblewrapPolicy(true, false)); err == nil {
		t.Fatal("unprivileged bubblewrap without the nested sandbox was accepted")
	}
}

func TestDirectWrapIgnoresUnprivilegedBubblewrap(t *testing.T) {
	command, args, err := (Direct{}).Wrap("claude", []string{"-p"}, unprivilegedBubblewrapPolicy(true, true))
	if err != nil || command != "claude" || len(args) != 1 {
		t.Fatalf("direct wrap = %q %q %v", command, args, err)
	}
}

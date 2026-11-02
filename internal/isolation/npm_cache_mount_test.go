package isolation

import (
	"slices"
	"strings"
	"testing"
)

func npmCachePolicy(cache string) Policy {
	return Policy{
		RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser, NetworkNone: true, ReadOnlyFS: true,
		WorkDir: "/state/job-results/job-1.checks/repo", WorkspaceRoot: "/state/job-results/job-1.checks",
		JobHome: "/state/check-homes/home-1", NPMCacheDir: cache,
	}
}

func TestDockerMountsTheNPMCacheReadWriteAtItsHostPath(t *testing.T) {
	docker := Docker{}
	_, args, err := docker.Wrap("npm", []string{"ci"}, npmCachePolicy("/state/job-results/job-1.npm-cache"))
	if err != nil {
		t.Fatal(err)
	}
	mount := slices.Index(args, "/state/job-results/job-1.npm-cache:/state/job-results/job-1.npm-cache:rw")
	if mount < 1 || args[mount-1] != "-v" {
		t.Fatalf("args %q lack the read-write cache mount", args)
	}
	env := slices.Index(args, "npm_config_cache=/state/job-results/job-1.npm-cache")
	if env < 1 || args[env-1] != "-e" {
		t.Fatalf("args %q lack the cache environment", args)
	}
	image := slices.Index(args, DefaultJobImage)
	if mount > image || env > image {
		t.Fatalf("cache options follow the image: %q", args)
	}
}

func TestDockerWithoutNPMCacheHasNoCacheMount(t *testing.T) {
	_, args, err := Docker{}.Wrap("npm", []string{"test"}, npmCachePolicy(""))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "npm-cache") || strings.Contains(joined, "npm_config_cache") {
		t.Fatalf("check container mounts the npm cache: %q", joined)
	}
}

func TestDockerRefusesAnUnsafeNPMCachePath(t *testing.T) {
	for _, cache := range []string{"relative/cache", "./cache", "/state/../cache", "/state/cache/", "/", "/state/ca:che", "/state/cache\n", " "} {
		if _, _, err := (Docker{}).Wrap("npm", nil, npmCachePolicy(cache)); err == nil {
			t.Fatalf("npm cache %q accepted", cache)
		}
	}
}

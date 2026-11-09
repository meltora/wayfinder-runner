package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
)

const goFixturePolicyTest = `package fixture

import (
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRunsUnderTheCheckPolicy(t *testing.T) {
	if uid, gid := os.Getuid(), os.Getgid(); uid != 10001 || gid != 10001 {
		t.Fatalf("uid:gid = %d:%d", uid, gid)
	}
	if err := os.WriteFile("/rootfs-probe", []byte("x"), 0o600); err == nil {
		t.Fatal("the root filesystem is writable")
	}
	if err := os.WriteFile("/tmp/exec-probe", []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("/tmp/exec-probe").Run(); err == nil {
		t.Fatal("/tmp allows execution")
	}
	if conn, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("the check reached the network")
	}
	if Add(2, 3) != 5 {
		t.Fatal("Add(2, 3) != 5")
	}
}
`

func seedGoFixtureModule(failing bool) func(t *testing.T, origin string) {
	return func(t *testing.T, origin string) {
		t.Helper()
		writeAgentFile(t, origin, "go.mod", "module example.test/fixture\n\ngo 1.25\n")
		sum := "a + b"
		if failing {
			sum = "a - b"
		}
		writeAgentFile(t, origin, "fixture.go", "package fixture\n\nfunc Add(a, b int) int { return "+sum+" }\n")
		writeAgentFile(t, origin, "fixture_test.go", goFixturePolicyTest)
	}
}

func imageGoTestCheck(image string) config.CheckConfig {
	return config.CheckConfig{Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 300, Image: image}
}

func TestGoCheckImageRunsGoTestUnderTheProductionPolicy(t *testing.T) {
	if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
		t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
	}
	checkImage := os.Getenv("CHECK_IMAGE")
	if checkImage == "" {
		t.Fatal("CHECK_IMAGE is required")
	}
	okLine := imageGoTestCheck(checkImage)
	okLine.Command, okLine.Args = "/bin/sh", []string{"-c", "go test ./... && exit 3"}
	for _, tc := range []struct {
		name    string
		failing bool
		check   config.CheckConfig
		result  string
		class   string
		output  string
	}{
		{"a passing module passes in the check image", false, imageGoTestCheck(checkImage), "passed", "", ""},
		{"go test prints its ok line in the check image", false, okLine, "failed", "exit_nonzero", "ok  \texample.test/fixture"},
		{"a failing test fails in the check image", true, imageGoTestCheck(checkImage), "failed", "exit_nonzero", "Add(2, 3) != 5"},
		{"the job image still has no Go toolchain", false, imageGoTestCheck(""), "failed", "exit_nonzero", "Cannot find module"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			jr, events := testJobRunner(t)
			jr.workDir = filepath.Join(jr.d.cfg.StateDir, "jobs", "job-rdbc")
			if err := os.MkdirAll(jr.workDir, 0o700); err != nil {
				t.Fatal(err)
			}
			seed := seedGoFixtureModule(tc.failing)
			f := newAgentResultTestOn(t, jr, events, func(origin string) { seed(t, origin) })
			useDockerChecks(t, f)

			result := f.runCheck(t, tc.check)

			output, _ := result["output"].(string)
			class, _ := result["error_classification"].(string)
			if result["result"] != tc.result || class != tc.class || !strings.Contains(output, tc.output) {
				t.Fatalf("result = %v, want %s (%q) with %q", result, tc.result, tc.class, tc.output)
			}
		})
	}
}

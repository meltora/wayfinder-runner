package plancommand

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

var hostileShells = map[string][]string{
	"bash": {"-c", "shopt -s extglob globstar dotglob nocaseglob\n" + "eval \"$0\""},
	"zsh":  {"-f", "-o", "extendedglob", "-o", "rcquotes", "-o", "magicequalsubst", "-o", "globdots", "-o", "braceccl", "-c", "eval \"$0\""},
}

func TestGuardWordsMatchRealShellArguments(t *testing.T) {
	fixture := newGuardFixture(t)
	for shell, args := range hostileShells {
		path, err := exec.LookPath(shell)
		if err != nil {
			t.Logf("%s is not installed; skipping its parity check", shell)
			continue
		}
		for _, command := range acceptedCommands {
			stages, err := splitPipeline(command)
			if err != nil {
				t.Fatalf("accepted command %q does not tokenize: %v", command, err)
			}
			sources := strings.Split(command, "|")
			if len(sources) != len(stages) {
				t.Fatalf("parity test needs unquoted pipes only: %q", command)
			}
			for i, source := range sources {
				want := stages[i][1:]
				if len(want) == 0 {
					continue
				}
				rest := strings.TrimLeft(source, " \t")
				rest = rest[strings.IndexAny(rest, " \t"):]
				got := shellArguments(t, path, args, fixture.workspace, "printf '%s\\0'"+rest)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s argv for %q = %q, guard saw %q", shell, command, got, want)
				}
			}
		}
	}
}

func TestHostileShellsDisagreeWhereTheGuardDenies(t *testing.T) {
	fixture := newGuardFixture(t)
	want := map[string][]string{
		"bash": {"sx", "--file==cat"},
		"zsh":  {"s'x", "--file=/usr/bin/cat"},
	}
	for shell, args := range hostileShells {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		got := shellArguments(t, path, args, fixture.workspace, "printf '%s\\0' 's''x' --file==cat")
		if shell == "zsh" && len(got) == 2 && got[1] == "--file=/bin/cat" {
			got[1] = "--file=/usr/bin/cat"
		}
		if !reflect.DeepEqual(got, want[shell]) {
			t.Fatalf("%s argv = %q, want %q", shell, got, want[shell])
		}
		for _, command := range []string{"cat 's''x'", "grep --file==cat x"} {
			if err := fixture.guard.CheckCommand(fixture.workspace, command); err == nil {
				t.Fatalf("guard accepted %q", command)
			}
		}
	}
}

func shellArguments(t *testing.T, shell string, args []string, dir, script string) []string {
	t.Helper()
	cmd := exec.Command(shell, append(append([]string(nil), args...), script)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %q: %v", shell, script, err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

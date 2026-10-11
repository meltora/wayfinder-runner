package plancommand

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type guardFixture struct {
	workspace string
	pack      string
	outside   string
	guard     *Guard
}

func newGuardFixture(t testing.TB) guardFixture {
	t.Helper()
	workspace := t.TempDir()
	pack := t.TempDir()
	outside := t.TempDir()
	for name, content := range map[string]string{
		"README.md":       "readme\n",
		"a.go":            "package a\n",
		"src/a.go":        "package src\n",
		"src/b.go":        "package src\n",
		"docs/notes.txt":  "notes\n",
		"globlink/ok.txt": "ok\n",
	} {
		path := filepath.Join(workspace, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pack, "WORKFLOW.md"), []byte("workflow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(workspace, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(workspace, "globlink", "z-secret.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(workspace, "README.md"), filepath.Join(workspace, "inside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing.txt"), filepath.Join(workspace, "dangling-link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"-delete", "-exec", "-R"} {
		if err := os.WriteFile(filepath.Join(workspace, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{
		"s":       filepath.Join(outside, "secret.txt"),
		"s'x":     filepath.Join(outside, "secret.txt"),
		"deref/l": outside,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(workspace, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(workspace, name)); err != nil {
			t.Fatal(err)
		}
	}
	guard, err := New([]string{workspace, pack})
	if err != nil {
		t.Fatal(err)
	}
	return guardFixture{workspace: workspace, pack: pack, outside: outside, guard: guard}
}

var acceptedCommands = []string{
	"git log --oneline -20",
	"git show HEAD:src/a.go",
	"git diff 'HEAD~1' -- src",
	"git status --porcelain",
	"git ls-files",
	"ls -la src",
	"cat README.md",
	"head -n 40 a.go",
	"tail -n 5 a.go",
	"wc -l a.go",
	"find . -name '*.go' -type f",
	`rg -n "func main" src`,
	"grep -rn TODO src",
	"git log --oneline | head -5",
	`grep -rn "hello world" src`,
	`rg -n 'two words' "src"`,
	"cat inside-link",
	"ls src",
	"git log --format='%h %s' -5",
	"git diff --stat 'HEAD~2..HEAD'",
	"rg -n -g '*.go' package",
	"grep -rn --include='*.go' package src",
	"find src -type f -newer README.md",
	"cat src/a.go | wc -l",
	"git log -p -- src | grep -n func | head -20",
	"wc -l\tREADME.md",
	"git show 'HEAD^'",
	"git diff --name-only 'HEAD~1'",
	"head -20 a.go",
	"tail -n +2 a.go",
	"head -n -1 a.go",
	"cat -n a.go",
	"wc -lw a.go",
	"ls -1 src",
	"ls --color=never -la",
	"grep -rn -e package -- src",
	"grep -c package a.go",
	"grep -A 3 -B2 package a.go",
	"grep -rn --exclude-dir=docs package .",
	`grep -rn --include="*.go" package src`,
	"rg --files src",
	"rg -t go -n package",
	"rg --glob='*.go' package",
	"rg -uu -l package",
	"rg -n -e package -- src",
	"find . -maxdepth 2 -type d",
	"find src -mtime -7 -name '*.go'",
	"find . '(' -name '*.go' -o -name '*.md' ')' -print",
	"find . -type f -size -10k -not -path './docs/*'",
	"git log -n 5 --stat",
	"git log -n5 --oneline",
	"git log -S package --oneline",
	"git log --since='2 weeks ago' --author=omar --format='%h %an'",
	"git show --stat HEAD",
	"git status -sb",
	"git status --porcelain=v1 -uno",
	"git ls-files -co --exclude-standard",
	"git ls-files --others --exclude-standard",
}

var deniedCommands = []string{
	"git log; touch f",
	"git log && rm -rf .",
	"git log || rm f",
	"git log & rm f",
	"git log |& head",
	"git log\nrm f",
	"git log\rrm f",
	"cat $(echo f)",
	"cat `echo f`",
	"cat ${HOME}/x",
	"cat $HOME",
	`cat "$HOME"`,
	"cat <(ls)",
	"diff >(cat)",
	"echo x > f",
	"cat a.go >> f",
	"cat < a.go",
	"cat a.go 2> f",
	"cat <<EOF",
	"cat <<< hello",
	"(cat a.go)",
	"{ cat a.go; }",
	`cat a\.go`,
	`cat "a\.go"`,
	"FOO=bar cat a.go",
	"git log | sh",
	"rm a.go",
	"sed -n 1p a.go",
	"python -c 'print(1)'",
	"bash -c 'ls'",
	"sh",
	"env",
	"xargs cat",
	"cd src",
	"git",
	"/bin/cat a.go",
	"~/bin/cat a.go",
	"./cat a.go",
	"git push",
	"git checkout main",
	"git -C /tmp log",
	"git -c core.pager=sh log",
	"git --git-dir=/tmp/x log",
	"git --work-tree=/tmp log",
	"git --exec-path=/tmp log",
	"git diff --output=f",
	"git diff --output f",
	"git log -o f",
	"git diff --ext-diff",
	"git log --textconv",
	"git show -Oorder",
	"git diff --open-files-in-pager",
	"find . -exec cat {} +",
	"find . -execdir ls ';'",
	"find . -ok rm '{}' ';'",
	"find . -okdir rm '{}' ';'",
	"find . -delete",
	"find . -fprint f",
	"find . -fprint0 f",
	"find . -fprintf f '%p'",
	"find . -fls f",
	"find -L .",
	"find . -follow",
	"rg --pre cat x",
	"rg --pre=cat x",
	"rg --pre-glob '*.go' x",
	"rg -z x",
	"rg --search-zip x",
	"rg -L x",
	"rg --follow x",
	"rg -nL x",
	"rg -nz x",
	"rg --hostname-bin=cat x",
	"grep -R x .",
	"grep -nR x .",
	"grep --dereference-recursive x .",
	"cat /etc/passwd",
	"cat ~/.claude/.credentials.json",
	"cat ~",
	"cat '~/x'",
	"cat ../secret.txt",
	"cat src/../../secret.txt",
	"git show HEAD:../x",
	"grep -rn --file=/etc/passwd x",
	"cat outside-link",
	"cat dangling-link",
	"cat globlink/*",
	"cat **/*.go",
	"cat a.go | | head",
	"| head",
	"cat a.go |",
	"git log | head | wc | cat | head",
	"cat 'unterminated",
	`cat "unterminated`,
	"cat a.go # comment",
	"! cat a.go",
	"=cat a.go",
	"cat =ls",
	"cat x=~/y",
	"cat HEAD:~/x",
	"",
	"   ",
	"cat\x00a.go",
	"grep -f/etc/passwd x",
	"cat [[:alpha:]]",
	"cat [[:lower:]]",
	"cat [[=s=]]",
	"cat src/[[:lower:]]*",
	"cat [s]",
	"cat s]",
	"cat s*~x",
	"cat s~x",
	"cat src/^x",
	"cat ^x",
	"cat ?",
	"cat a.g?",
	"ls src/*.go",
	"git diff HEAD~1",
	"git show HEAD^",
	"find . -delet?",
	"find . -delet[e]",
	"find . -exe? cat ';'",
	"grep -r x -[R]",
	"find . '-delete'",
	`find . "-exec" cat ';'`,
	"grep --derefer -r x",
	"grep --dereference-r -r x",
	"grep --dereference-recursive=yes x",
	"grep --color=always --dereference-recursive x",
	"grep -fs x .",
	"grep -f s x",
	"grep -rf s x",
	"grep --file=s x",
	"grep --file s x",
	"grep --regexp x a.go",
	"grep --exclude-from=s x .",
	"rg -fs x",
	"rg -f s x",
	"rg -nf s x",
	"rg --ignore-file=s x",
	"rg --pre-gl='*' x",
	"rg --hostname-bin cat",
	"rg --glob '*.go' x",
	"wc --files0-from=a.go",
	"wc --files0=a.go",
	"wc --files0-from a.go",
	"find . -files0-from a.go",
	"find -files0-from a.go",
	"find . -fprin f",
	"find -H .",
	"find -D tree .",
	"find . -newer s",
	"find . -name -delete",
	"ls -L",
	"ls --dereference",
	"ls --derefe",
	"ls -H s",
	"ls deref/l",
	"head --byt=5 a.go",
	"head -n",
	"head -c s",
	"tail -f a.go",
	"tail -F a.go",
	"tail --follow a.go",
	"tail --pid=1 a.go",
	"cat --show-a s",
	"cat -v s",
	"cat -- s",
	"git diff --outp=f",
	"git diff --out=f",
	"git log --output-indicator-new=x",
	"git log --show-signature",
	"git log -pO f",
	"git log -p -Of",
	"git diff --no-index a.go s",
	"git ls-files --exclude-f=s",
	"git ls-files --exclude-from=s",
	"git ls-files -X s",
	"git ls-files -Xs",
	"git status --porc",
	"git status -uno --porcelain s",
	"git log -n",
	"git log --grep foo",
	"cat 's''x'",
	"cat 's'''",
	"grep --file==cat x",
	"grep --exclude-from=a:=cat x .",
	"cat x:=ls",
	`cat "a!b"`,
	"git log --format='%G?'",
	"git log --pretty='format:%h %GS'",
	"git show --format=%GK",
	"cat ''=cat",
	`cat ""=cat`,
	"head -c 64 ''=git",
	`wc -c ""=ls`,
	"grep -c x ''=cat",
	"ls -la ''=cat",
	"find ''=cat",
	"grep --file=''=cat x .",
	"cat 'a'=cat",
	"git log --format='%+G?' -1",
	"git log --pretty=%+GS -1",
	"git log --format='%-GS' -1",
	"git log --format='% GK' -1",
	"git show --format='%+GG' -s",
	"git log --format='%<(10)%+G?' -1",
	"git log --format='%>>(4)%GK' -1",
	"cat x:''=cat",
	"cat a=''=cat",
	"git log --format='%>(5)%GS' -1",
	"git log --format='tformat:%+GK' -1",
}

func TestGuardAcceptsReadOnlyCommands(t *testing.T) {
	fixture := newGuardFixture(t)
	for _, command := range acceptedCommands {
		t.Run(command, func(t *testing.T) {
			if err := fixture.guard.CheckCommand(fixture.workspace, command); err != nil {
				t.Fatalf("accepted command denied: %v", err)
			}
		})
	}
	if err := fixture.guard.CheckCommand(fixture.pack, "cat WORKFLOW.md"); err != nil {
		t.Fatalf("pack root command denied: %v", err)
	}
	if err := fixture.guard.CheckCommand(filepath.Join(fixture.workspace, "src"), "cat a.go"); err != nil {
		t.Fatalf("nested cwd denied: %v", err)
	}
}

func TestGuardDeniesUnsafeCommands(t *testing.T) {
	fixture := newGuardFixture(t)
	for _, command := range deniedCommands {
		t.Run(strings.ReplaceAll(command, "/", "_"), func(t *testing.T) {
			err := fixture.guard.CheckCommand(fixture.workspace, command)
			if err == nil {
				t.Fatalf("unsafe command accepted: %q", command)
			}
			if strings.TrimSpace(err.Error()) == "" {
				t.Fatal("denial has no reason")
			}
		})
	}
}

func TestGuardDeniesWorkingDirectoryOutsideRoots(t *testing.T) {
	fixture := newGuardFixture(t)
	for _, cwd := range []string{fixture.outside, "", "relative/dir", filepath.Join(fixture.workspace, "missing")} {
		if err := fixture.guard.CheckCommand(cwd, "ls"); err == nil {
			t.Fatalf("cwd %q accepted", cwd)
		}
	}
	link := filepath.Join(fixture.workspace, "outside-dir")
	if err := os.Symlink(fixture.outside, link); err != nil {
		t.Fatal(err)
	}
	if err := fixture.guard.CheckCommand(link, "ls"); err == nil {
		t.Fatal("cwd symlinked outside the roots accepted")
	}
	if err := fixture.guard.CheckCommand(fixture.workspace, "ls outside-dir"); err == nil {
		t.Fatal("directory symlinked outside the roots accepted")
	}
}

func TestGuardRejectsOversizedCommands(t *testing.T) {
	fixture := newGuardFixture(t)
	command := "cat " + strings.Repeat("a", MaxCommandBytes)
	if err := fixture.guard.CheckCommand(fixture.workspace, command); err == nil {
		t.Fatal("oversized command accepted")
	}
}

func hookInputJSON(t *testing.T, cwd string, fields map[string]any) []byte {
	t.Helper()
	input := map[string]any{
		"session_id": "s1", "hook_event_name": "PreToolUse", "cwd": cwd,
		"tool_name": "Bash", "tool_input": map[string]any{"command": "git status --porcelain", "description": "status"},
	}
	for key, value := range fields {
		if value == nil {
			delete(input, key)
			continue
		}
		input[key] = value
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGuardHookInputValidation(t *testing.T) {
	fixture := newGuardFixture(t)
	if err := fixture.guard.CheckHookInput(hookInputJSON(t, fixture.workspace, nil)); err != nil {
		t.Fatalf("valid hook input denied: %v", err)
	}
	denied := map[string][]byte{
		"empty":              nil,
		"malformed":          []byte(`{"tool_name":`),
		"array":              []byte(`[]`),
		"trailing":           append(hookInputJSON(t, fixture.workspace, nil), []byte(` {}`)...),
		"tool name":          hookInputJSON(t, fixture.workspace, map[string]any{"tool_name": "Write"}),
		"missing tool name":  hookInputJSON(t, fixture.workspace, map[string]any{"tool_name": nil}),
		"missing tool input": hookInputJSON(t, fixture.workspace, map[string]any{"tool_input": nil}),
		"missing command":    hookInputJSON(t, fixture.workspace, map[string]any{"tool_input": map[string]any{"description": "x"}}),
		"numeric command":    hookInputJSON(t, fixture.workspace, map[string]any{"tool_input": map[string]any{"command": 7}}),
		"empty command":      hookInputJSON(t, fixture.workspace, map[string]any{"tool_input": map[string]any{"command": ""}}),
		"oversized command": hookInputJSON(t, fixture.workspace, map[string]any{"tool_input": map[string]any{
			"command": "cat " + strings.Repeat("a", MaxCommandBytes),
		}}),
		"case-folded command key": []byte(`{"cwd":"` + fixture.workspace + `","tool_name":"Bash","tool_input":{"command":"rm -rf src","Command":"ls"}}`),
		"case-folded only key":    []byte(`{"cwd":"` + fixture.workspace + `","tool_name":"Bash","tool_input":{"Command":"ls"}}`),
		"case-folded tool name":   []byte(`{"cwd":"` + fixture.workspace + `","tool_name":"Write","Tool_Name":"Bash","tool_input":{"command":"ls"}}`),
		"case-folded cwd":         []byte(`{"cwd":"` + fixture.workspace + `","CWD":"` + fixture.outside + `","tool_name":"Bash","tool_input":{"command":"ls"}}`),
		"case-folded tool input":  []byte(`{"cwd":"` + fixture.workspace + `","tool_name":"Bash","Tool_Input":{"command":"rm -rf src"},"tool_input":{"command":"ls"}}`),
		"unsafe command":          hookInputJSON(t, fixture.workspace, map[string]any{"tool_input": map[string]any{"command": "rm -rf ."}}),
		"missing cwd":             hookInputJSON(t, fixture.workspace, map[string]any{"cwd": nil}),
		"outside cwd":             hookInputJSON(t, fixture.outside, nil),
		"oversized input":         []byte(`{"pad":"` + strings.Repeat("x", MaxHookInputBytes) + `"}`),
	}
	for name, raw := range denied {
		t.Run(name, func(t *testing.T) {
			if err := fixture.guard.CheckHookInput(raw); err == nil {
				t.Fatalf("hook input accepted: %s", name)
			}
		})
	}
}

func TestNewRejectsInvalidRoots(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, roots := range map[string][]string{
		"none":     nil,
		"relative": {"relative"},
		"missing":  {filepath.Join(t.TempDir(), "missing")},
		"file":     {file},
		"control":  {t.TempDir() + "\n"},
	} {
		if _, err := New(roots); err == nil {
			t.Fatalf("%s roots accepted", name)
		}
	}
}

func TestGuardTableRunsWellUnderFiftyMillisecondsAtP99(t *testing.T) {
	fixture := newGuardFixture(t)
	commands := append(append([]string(nil), acceptedCommands...), deniedCommands...)
	var durations []time.Duration
	for range 20 {
		for _, command := range commands {
			start := time.Now()
			_ = fixture.guard.CheckCommand(fixture.workspace, command)
			durations = append(durations, time.Since(start))
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[len(durations)*99/100]
	if p99 >= 50*time.Millisecond {
		t.Fatalf("p99 guard decision = %s", p99)
	}
	t.Logf("p99 guard decision = %s over %d decisions", p99, len(durations))
}

func BenchmarkGuardTable(b *testing.B) {
	fixture := newGuardFixture(b)
	commands := append(append([]string(nil), acceptedCommands...), deniedCommands...)
	b.ResetTimer()
	for b.Loop() {
		for _, command := range commands {
			_ = fixture.guard.CheckCommand(fixture.workspace, command)
		}
	}
}

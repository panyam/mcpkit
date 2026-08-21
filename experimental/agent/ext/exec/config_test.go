package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workspace makes a root and returns its symlink-resolved absolute path, which
// is what the source compares against. On darwin t.TempDir is under /var,
// itself a symlink to /private/var.
func workspace(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// echoSpec is a minimal valid command for tests about everything except what
// the command does.
func echoSpec() CommandSpec {
	return CommandSpec{Name: "echo", Argv: []string{"echo", "hi"}, Description: "Say hi."}
}

func baseConfig(t *testing.T, cmds ...CommandSpec) Config {
	t.Helper()
	return Config{Roots: []string{workspace(t)}, Commands: cmds, Sandbox: Unconfined()}
}

func TestRootsAreRequired(t *testing.T) {
	_, err := NewSource(Config{Commands: []CommandSpec{echoSpec()}, Sandbox: Unconfined()})
	if err == nil {
		t.Fatal("a source with no root would run commands anywhere")
	}
}

func TestAnEmptyAllowlistIsRefused(t *testing.T) {
	_, err := NewSource(Config{Roots: []string{workspace(t)}, Sandbox: Unconfined()})
	if err == nil {
		t.Fatal("an exec source with nothing to run is a misconfiguration")
	}
}

func TestCommandNameMustBeUsableAsAToolName(t *testing.T) {
	for _, name := range []string{"", "Test", "run test", "test-suite", "2fast"} {
		spec := echoSpec()
		spec.Name = name
		if _, err := NewSource(baseConfig(t, spec)); err == nil {
			t.Errorf("name %q became a tool name unchallenged", name)
		}
	}
}

func TestDuplicateCommandNamesAreRefused(t *testing.T) {
	a, b := echoSpec(), echoSpec()
	b.Argv = []string{"echo", "different"}
	if _, err := NewSource(baseConfig(t, a, b)); err == nil {
		t.Fatal("two commands sharing a name leave one of them unreachable")
	}
}

func TestACommandNotOnPathFailsAtConstruction(t *testing.T) {
	spec := echoSpec()
	spec.Argv = []string{"mcpkit-no-such-binary-anywhere"}
	if _, err := NewSource(baseConfig(t, spec)); err == nil {
		t.Fatal("a command that cannot resolve should fail at startup, not an hour into a session")
	}
}

func TestACommandWithoutADescriptionIsRefused(t *testing.T) {
	spec := echoSpec()
	spec.Description = ""
	if _, err := NewSource(baseConfig(t, spec)); err == nil {
		t.Fatal("the description is the only thing the model has to choose between two commands")
	}
}

func TestCommandDirCannotEscapeTheRoots(t *testing.T) {
	spec := echoSpec()
	spec.Dir = "../.."
	if _, err := NewSource(baseConfig(t, spec)); err == nil {
		t.Fatal("a Dir outside every root confines nothing")
	}
}

func TestCommandDirResolvesRelativeToTheFirstRoot(t *testing.T) {
	root := workspace(t)
	sub := filepath.Join(root, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := echoSpec()
	spec.Dir = "pkg"
	src, err := NewSource(Config{Roots: []string{root}, Commands: []CommandSpec{spec}, Sandbox: Unconfined()})
	if err != nil {
		t.Fatal(err)
	}
	if got := src.cmds["run_echo"].dir; got != sub {
		t.Errorf("dir = %q, want %q", got, sub)
	}
}

func TestASymlinkOutOfTheRootIsNotADir(t *testing.T) {
	root := workspace(t)
	outside := workspace(t)
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	spec := echoSpec()
	spec.Dir = "escape"
	if _, err := NewSource(Config{Roots: []string{root}, Commands: []CommandSpec{spec}, Sandbox: Unconfined()}); err == nil {
		t.Fatal("a symlink inside a root pointing out of it is still outside it")
	}
}

func TestArgPolicyMustActuallyConstrain(t *testing.T) {
	cases := map[string]ArgPolicy{
		"no maximum":     {Match: `.*`},
		"no expression":  {Max: 2},
		"broken pattern": {Max: 2, Match: `([`},
	}
	for name, p := range cases {
		spec := echoSpec()
		policy := p
		spec.Args = &policy
		if _, err := NewSource(baseConfig(t, spec)); err == nil {
			t.Errorf("%s: accepted an argument policy that permits everything", name)
		}
	}
}

func TestDefaultDenyReadCoversCredentialLocations(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	got := strings.Join(expandPaths(DefaultDenyRead), " ")
	for _, want := range []string{"/home/u/.ssh", "/home/u/.aws"} {
		if !strings.Contains(got, want) {
			t.Errorf("default read denials are missing %s: %s", want, got)
		}
	}
}

func TestExpandPathsDropsEntriesWhoseVariableIsUnset(t *testing.T) {
	t.Setenv("HOME", "")
	got := expandPaths([]string{"$HOME/.ssh", "/tmp"})
	for _, p := range got {
		if strings.HasSuffix(p, "/.ssh") {
			t.Errorf("an unset variable must drop the entry rather than expand to /.ssh, got %v", got)
		}
	}
	if len(got) == 0 {
		t.Errorf("the entries that do expand must survive, got %v", got)
	}
}

// TestProfilePathsAreSymlinkResolved is what the live suite caught. A rule
// naming /tmp covers nothing on darwin, where the kernel matches the resolved
// path and /tmp is a link to /private/tmp, so a read denial reads as in force
// while permitting the read.
func TestProfilePathsAreSymlinkResolved(t *testing.T) {
	real, err := filepath.EvalSymlinks("/tmp")
	if err != nil || real == "/tmp" {
		t.Skip("/tmp is not a symlink on this platform")
	}
	got := expandPaths([]string{"/tmp"})
	var sawReal bool
	for _, p := range got {
		if p == real {
			sawReal = true
		}
	}
	if !sawReal {
		t.Errorf("want the resolved path %s among %v, or the rule matches nothing", real, got)
	}
}

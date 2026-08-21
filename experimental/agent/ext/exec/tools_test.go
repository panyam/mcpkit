package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustSource(t *testing.T, cfg Config) *Source {
	t.Helper()
	src, err := NewSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func call(t *testing.T, src *Source, name string, args map[string]any) string {
	t.Helper()
	res, err := src.Call(context.Background(), name, args)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	return res.Content[0].Text
}

func TestAnUnlistedCommandIsADispatchFailure(t *testing.T) {
	src := mustSource(t, baseConfig(t, echoSpec()))
	if _, err := src.Call(context.Background(), "run_anything", nil); err == nil {
		t.Fatal("a name outside the allowlist must not resolve")
	}
}

func TestTheAllowlistIsTheOnlyWayIn(t *testing.T) {
	src := mustSource(t, baseConfig(t, echoSpec()))
	defs, err := src.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "run_echo" {
		t.Fatalf("one tool per command, got %+v", defs)
	}
	schema := defs[0].InputSchema.(map[string]any)
	props := schema["properties"].(map[string]any)
	if _, ok := props["args"]; ok {
		t.Error("a command with no ArgPolicy must expose no argument at all")
	}
	if _, ok := props["command"]; ok {
		t.Error("nothing in the schema may let the model name a command")
	}
}

func TestArgumentsAreRefusedWithoutAPolicy(t *testing.T) {
	src := mustSource(t, baseConfig(t, echoSpec()))
	got := call(t, src, "run_echo", map[string]any{"args": []any{"--anything"}})
	if !strings.Contains(got, "takes no arguments") {
		t.Errorf("arguments must be refused when no policy permits them, got %q", got)
	}
}

// argSpec runs the helper's echo mode with a permissive-looking policy, so a
// test can watch what actually reaches the process.
func argSpec(t *testing.T, p *ArgPolicy) CommandSpec {
	return CommandSpec{
		Name:        "echo",
		Argv:        helperArgv(t, "echo"),
		Description: "Echo the arguments.",
		Args:        p,
	}
}

func TestArgumentsBeyondTheMaximumAreRefused(t *testing.T) {
	src := mustSource(t, baseConfig(t, argSpec(t, &ArgPolicy{Max: 2, Match: `\w+`})))
	got := call(t, src, "run_echo", map[string]any{"args": []any{"a", "b", "c"}})
	if !strings.Contains(got, "at most 2") {
		t.Errorf("want a refusal naming the limit, got %q", got)
	}
}

func TestAnArgumentMustMatchThePatternEndToEnd(t *testing.T) {
	src := mustSource(t, baseConfig(t, argSpec(t, &ArgPolicy{Max: 2, Match: `\./\w+`})))
	// Anchoring is the point: ./pkg;rm contains a match for ./\w+ and must
	// still be refused, or every policy is a substring policy.
	for _, bad := range []string{"./pkg;rm", "x./pkg", "--flag"} {
		got := call(t, src, "run_echo", map[string]any{"args": []any{bad}})
		if !strings.Contains(got, "not permitted") {
			t.Errorf("argument %q was accepted: %q", bad, got)
		}
	}
	if got := call(t, src, "run_echo", map[string]any{"args": []any{"./pkg"}}); !strings.Contains(got, "exited 0") {
		t.Errorf("a matching argument should run: %q", got)
	}
}

func TestPathArgumentsAreConfinedToTheRoots(t *testing.T) {
	root := workspace(t)
	spec := argSpec(t, &ArgPolicy{Max: 2, Match: `[\w./-]+`, Paths: true})
	src := mustSource(t, Config{Roots: []string{root}, Commands: []CommandSpec{spec}, Sandbox: Unconfined()})

	got := call(t, src, "run_echo", map[string]any{"args": []any{"../../etc/passwd"}})
	if !strings.Contains(got, "outside every workspace root") {
		t.Errorf("a path argument climbing out of the root must be refused, got %q", got)
	}
	if got := call(t, src, "run_echo", map[string]any{"args": []any{"./pkg/..."}}); !strings.Contains(got, "exited 0") {
		t.Errorf("a build pattern naming no existing file should still pass: %q", got)
	}
}

func TestArgumentsReachTheProcessLiterally(t *testing.T) {
	root := workspace(t)
	spec := argSpec(t, &ArgPolicy{Max: 2, Match: `[^\n]+`})
	src := mustSource(t, Config{Roots: []string{root}, Commands: []CommandSpec{spec}, Sandbox: Unconfined()})

	marker := filepath.Join(root, "pwned")
	got := call(t, src, "run_echo", map[string]any{"args": []any{"; touch " + marker}})
	if !strings.Contains(got, "; touch "+marker) {
		t.Errorf("the argument should arrive as one literal string, got %q", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a shell metacharacter ran as a command; nothing here may reach a shell")
	}
}

func TestANonZeroExitIsAResultRatherThanAToolError(t *testing.T) {
	spec := CommandSpec{Name: "build", Argv: helperArgv(t, "fail"), Description: "Build."}
	src := mustSource(t, baseConfig(t, spec))

	res, err := src.Call(context.Background(), "run_build", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Error("a command that ran and failed did its job; flagging the tool as broken hides the failure from the model")
	}
	if !strings.Contains(res.Content[0].Text, "exited 3") {
		t.Errorf("the exit code must be visible, got %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "undefined: x") {
		t.Errorf("stderr is the failure; it must reach the model, got %q", res.Content[0].Text)
	}
	if code := res.StructuredContent.(map[string]any)["exit_code"]; code != 3 {
		t.Errorf("exit_code = %v, want 3", code)
	}
}

func TestACommandRunsInItsConfiguredDirectory(t *testing.T) {
	root := workspace(t)
	sub := filepath.Join(root, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := CommandSpec{Name: "where", Argv: helperArgv(t, "cwd"), Dir: "pkg", Description: "Print the directory."}
	src := mustSource(t, Config{Roots: []string{root}, Commands: []CommandSpec{spec}, Sandbox: Unconfined()})
	if got := call(t, src, "run_where", nil); !strings.Contains(got, sub) {
		t.Errorf("want the command to run in %s, got %q", sub, got)
	}
}

func TestOutputIsCappedAndSaysSo(t *testing.T) {
	spec := CommandSpec{Name: "spam", Argv: helperArgv(t, "spam", "4000"), Description: "Print a lot."}
	cfg := baseConfig(t, spec)
	cfg.MaxOutput = 2048
	src := mustSource(t, cfg)

	res, err := src.Call(context.Background(), "run_spam", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "FIRST LINE") {
		t.Error("the head says why a run failed to start and must survive truncation")
	}
	if !strings.Contains(text, "LAST LINE") {
		t.Error("the tail carries the summary and must survive truncation")
	}
	if !strings.Contains(text, "dropped from the middle") {
		t.Error("a truncated result that does not say so reads as a complete one")
	}
	if truncated := res.StructuredContent.(map[string]any)["truncated"]; truncated != true {
		t.Errorf("truncated = %v, want true", truncated)
	}
}

func TestATimeoutIsAToolErrorWithWhateverItPrinted(t *testing.T) {
	spec := CommandSpec{
		Name:        "hang",
		Argv:        helperArgv(t, "sleep", "60s"),
		Description: "Hang.",
		Timeout:     300 * time.Millisecond,
	}
	src := mustSource(t, baseConfig(t, spec))

	start := time.Now()
	res, err := src.Call(context.Background(), "run_hang", nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the timeout did not fire: took %s", elapsed)
	}
	if !res.IsError {
		t.Error("a command that never finished produced no verdict, so the result is an error")
	}
	if !strings.Contains(res.Content[0].Text, "timed out") {
		t.Errorf("want a timeout explanation, got %q", res.Content[0].Text)
	}
}

func TestAnnotationsCarryWhatTheOperatorDeclared(t *testing.T) {
	specs := []CommandSpec{
		{Name: "lint", Argv: helperArgv(t, "echo"), Description: "Lint.", ReadOnly: true},
		{Name: "fmt", Argv: helperArgv(t, "echo"), Description: "Format.", Reversible: true},
		{Name: "deploy", Argv: helperArgv(t, "echo"), Description: "Deploy."},
	}
	src := mustSource(t, baseConfig(t, specs...))
	defs, err := src.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, d := range defs {
		byName[d.Name] = d.Annotations
	}

	if ro, _ := byName["run_lint"]["readOnlyHint"].(bool); !ro {
		t.Error("a read-only command must declare it, or it prompts under every mode")
	}
	if d, ok := byName["run_fmt"]["destructiveHint"].(bool); !ok || d {
		t.Error("a reversible command must clear destructiveHint, or ModeReversibleAuto still asks")
	}
	if _, present := byName["run_deploy"]["destructiveHint"]; present {
		t.Error("an unclassified command must declare nothing, because an absent destructiveHint reads as destructive")
	}
}

func TestTheDescriptionNamesWhatWillActuallyRun(t *testing.T) {
	src := mustSource(t, baseConfig(t, echoSpec()))
	defs, _ := src.Tools(context.Background())
	if !strings.Contains(defs[0].Description, "echo hi") {
		t.Errorf("the model should see the command it is invoking, got %q", defs[0].Description)
	}
}

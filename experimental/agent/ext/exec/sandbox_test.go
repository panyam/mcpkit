package exec

import (
	osexec "os/exec"
	"runtime"
	"strings"
	"testing"
)

type stubSandbox struct {
	name  string
	avail error
	seen  Policy
}

func (s *stubSandbox) Name() string     { return s.name }
func (s *stubSandbox) Available() error { return s.avail }
func (s *stubSandbox) Confine(_ *osexec.Cmd, p Policy) error {
	s.seen = p
	return nil
}

func noBackend() Sandbox { return nil }

func TestNoBackendRefusesRatherThanRunningUnconfined(t *testing.T) {
	_, err := resolveSandbox(nil, noBackend)
	if err == nil {
		t.Fatal("a platform with no backend must refuse, not fall back to running unconfined")
	}
	if !strings.Contains(err.Error(), "Unconfined") {
		t.Errorf("the refusal must name the opt-out, got %q", err)
	}
}

func TestUnconfinedMustBeNamedExplicitly(t *testing.T) {
	got, err := resolveSandbox(Unconfined(), noBackend)
	if err != nil {
		t.Fatalf("an explicit Unconfined is a valid choice: %v", err)
	}
	if got.Name() != "unconfined" {
		t.Errorf("Name() = %q, want unconfined", got.Name())
	}
}

func TestUnavailableBackendFailsAtConstruction(t *testing.T) {
	stub := &stubSandbox{name: "stub", avail: errStub}
	if _, err := resolveSandbox(stub, noBackend); err == nil {
		t.Fatal("a backend that reports itself unavailable must fail construction")
	}
}

func TestPlatformBackendMatchesWhatShipped(t *testing.T) {
	got := defaultSandbox()
	if runtime.GOOS == "darwin" {
		if got == nil {
			t.Fatal("darwin ships sandbox-exec; defaultSandbox returned nil")
		}
		if got.Name() != "sandbox-exec" {
			t.Errorf("Name() = %q, want sandbox-exec", got.Name())
		}
		return
	}
	if got != nil {
		t.Errorf("no backend has landed for %s yet, got %q", runtime.GOOS, got.Name())
	}
}

func TestSBPLConfinesWritesAndDeniesEgress(t *testing.T) {
	p := Policy{
		Write:    []string{"/ws/two", "/ws/one"},
		DenyRead: []string{"/home/u/.ssh"},
		Dir:      "/ws/one",
	}
	got := buildSBPL(p)

	for _, want := range []string{
		"(deny default)",
		`(subpath "/ws/one")`,
		`(subpath "/ws/two")`,
		`(deny file-read* (subpath "/home/u/.ssh"))`,
		`(allow network-outbound (remote ip "localhost:*"))`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("profile is missing %s\n%s", want, got)
		}
	}
	if strings.Contains(got, "(allow network*)") {
		t.Errorf("network is off by default; profile allows it\n%s", got)
	}

	// The read denial has to come after the blanket read allow, because
	// Seatbelt takes the last matching rule. Ordered the other way the denial
	// is present and does nothing, which is the failure that looks fine.
	if strings.Index(got, "(allow file-read*)") > strings.Index(got, `(deny file-read* (subpath "/home/u/.ssh"))`) {
		t.Error("the read denial must follow the read allow, or Seatbelt's last-match rule discards it")
	}
}

func TestSBPLAllowsNetworkOnlyWhenTheCommandAsked(t *testing.T) {
	got := buildSBPL(Policy{AllowNetwork: true})
	if !strings.Contains(got, "(allow network*)") {
		t.Errorf("AllowNetwork must open the network\n%s", got)
	}
}

func TestSBPLIsStableAcrossConfigurationOrder(t *testing.T) {
	a := buildSBPL(Policy{Write: []string{"/b", "/a", "/b"}})
	b := buildSBPL(Policy{Write: []string{"/a", "/b"}})
	if a != b {
		t.Errorf("the profile must not depend on the order roots were configured in\n%s\n---\n%s", a, b)
	}
}

func TestSBPLQuotesPathsThatWouldBreakTheProfile(t *testing.T) {
	got := buildSBPL(Policy{Write: []string{`/ws/a"b\c`}})
	if !strings.Contains(got, `(subpath "/ws/a\"b\\c")`) {
		t.Errorf("a quote or backslash in a path must be escaped, or it ends the profile string early\n%s", got)
	}
}

package exec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultTimeout bounds a command that does not set its own.
const DefaultTimeout = 2 * time.Minute

// DefaultMaxOutput caps the bytes of combined output returned to the model.
// Output beyond it is dropped from the middle, keeping the head and the tail,
// because a failing build states the problem at one end or the other and
// almost never in the middle.
const DefaultMaxOutput = 32 << 10

// toolPrefix namespaces the generated tool names so a command called "test"
// registers as run_test and cannot collide with an unrelated source's "test".
const toolPrefix = "run_"

// nameRE constrains a command name, because the name becomes a tool name and
// a tool name is matched literally by approval rules and per-tool config.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Config configures the command source.
//
// There is no mode in which the model supplies a command. Commands come from
// here, and the model chooses among them, for the reason files.Config.Roots
// and lsp.ServerSpec.Command already give: a model's instructions can come
// from content it read rather than from the user, so anything the model can
// name is something an injected instruction can name.
type Config struct {
	// Roots are the directories a command may run in and write to. At least
	// one is required, and the first is primary: a relative Dir or path
	// argument resolves against it.
	//
	// A set rather than one directory, matching files.Config.Roots, because a
	// session that stays inside one repository is the exception (issue 1314).
	Roots []string

	// Commands is the allowlist. Each entry becomes one tool, so the model
	// picks a tool rather than composing a command line. Empty is an error:
	// an exec extension with nothing to run is a misconfiguration, not a
	// degenerate case worth supporting.
	Commands []CommandSpec

	// Sandbox confines every command. Nil selects the platform's backend and
	// fails construction when there is none, rather than running unconfined.
	//
	// Unconfined() is the deliberate opt-out, and has to be written here. It
	// is the right setting for a process an operator already isolated, which
	// is the ordinary deployed case: a container the operator built is the
	// boundary, and a second fence inside it protects nothing.
	Sandbox Sandbox

	// Timeout bounds any command whose spec does not set its own. Zero means
	// DefaultTimeout.
	Timeout time.Duration

	// MaxOutput caps the combined output bytes returned. Zero means
	// DefaultMaxOutput.
	MaxOutput int

	// WritePaths are directories outside Roots that commands may still write
	// to. Nil means DefaultWritePaths, which covers the build and module
	// caches a toolchain needs; an explicitly empty non-nil slice means none.
	//
	// A toolchain that needs a path not listed here fails with a sandbox
	// denial rather than silently writing outside confinement, which is the
	// failure direction to prefer.
	WritePaths []string

	// DenyRead are paths a command may not read even though reads are
	// otherwise unrestricted. Nil means DefaultDenyRead; an explicitly empty
	// non-nil slice means no read is denied.
	//
	// Reads are broadly allowed on purpose. Confining them would break every
	// real toolchain, which reads compilers, SDKs, module caches and system
	// libraries scattered across the filesystem, and the resulting failures
	// look like bugs rather than policy. The exposure that leaves is a
	// command reading credentials, so the default denies the well-known
	// credential locations rather than pretending to a confinement the
	// profile does not have.
	DenyRead []string
}

// DefaultWritePaths lists the caches a build or test command writes to outside
// the workspace. Without them a sandboxed `go test` fails on its build cache
// before it compiles anything.
var DefaultWritePaths = []string{
	"$TMPDIR",
	"/tmp",
	"/private/tmp",
	"/private/var/tmp",
	"$HOME/Library/Caches",
	"$HOME/.cache",
	"$HOME/go/pkg/mod",
	"$HOME/.cargo/registry",
	"$HOME/.npm",
}

// DefaultDenyRead lists credential locations a build or test command has no
// business reading. It is not exhaustive and cannot be: it is the difference
// between a profile that stops the obvious exfiltration and one that stops
// none.
var DefaultDenyRead = []string{
	"$HOME/.ssh",
	"$HOME/.aws",
	"$HOME/.gnupg",
	"$HOME/.kube",
	"$HOME/.docker",
	"$HOME/.config/gcloud",
	"$HOME/.netrc",
	"$HOME/.npmrc",
	"$HOME/.pypirc",
}

// CommandSpec is one entry in the allowlist.
type CommandSpec struct {
	// Name identifies the command to the model. It becomes the tool name
	// with a run_ prefix, so it must match [a-z][a-z0-9_]*.
	Name string

	// Argv is the command and its fixed arguments. Argv[0] is resolved
	// through PATH once, at construction, so the tool runs the binary that
	// was on PATH when the operator configured it.
	//
	// It is an argv and never a shell string. Nothing here is passed to a
	// shell, so an argument containing ; or | or $( is an argument.
	Argv []string

	// Dir is the working directory, relative to the first root or absolute
	// inside one of them. Empty means the first root.
	Dir string

	// Description tells the model when to reach for this command. It becomes
	// the tool description, which is the only thing the model has to choose
	// between two entries.
	Description string

	// Args permits trailing arguments the model supplies. Nil means the
	// command takes none and the tool exposes no argument at all, which is
	// the default because an allowlist that accepts free-form arguments is a
	// weaker statement than it appears to be.
	Args *ArgPolicy

	// AllowNetwork lets this command reach the network. Off by default: a
	// build that fetches at test time is a build that can also post.
	AllowNetwork bool

	// ReadOnly marks a command with no side effects worth reviewing, and
	// becomes the tool's readOnlyHint. It makes the command auto-allowed
	// under ModeReadOnlyAuto and above.
	ReadOnly bool

	// Reversible marks a command whose effects can be undone, and clears the
	// tool's destructiveHint. It makes the command auto-allowed under
	// ModeReversibleAuto.
	//
	// The zero value declares the command destructive, so a command nobody
	// classified is asked about. The operator who wrote the allowlist is the
	// one who knows which entries are safe to run unattended, and a default
	// that guessed on their behalf would guess in the permissive direction.
	Reversible bool

	// Timeout overrides Config.Timeout for this command.
	Timeout time.Duration
}

// ArgPolicy constrains the trailing arguments a model may supply.
//
// It exists so the test loop can run a subset rather than only whole suites,
// which is the difference between a useful feedback loop and one that reruns
// everything to learn one thing.
type ArgPolicy struct {
	// Max is the most arguments accepted, and must be positive.
	Max int

	// Match is a regular expression every argument must match end to end. It
	// is required: a policy that permits arguments without constraining them
	// is the free-form command line this package exists to avoid.
	Match string

	// Paths additionally requires every argument to name a path inside a
	// root, resolved through symlinks. It applies to every argument, so a
	// command taking a mix of flags and paths cannot use it.
	Paths bool

	re *regexp.Regexp
}

func (p *ArgPolicy) validate() error {
	if p.Max <= 0 {
		return fmt.Errorf("ArgPolicy.Max must be positive")
	}
	if p.Match == "" {
		return fmt.Errorf("ArgPolicy.Match is required; a policy that constrains nothing permits everything")
	}
	re, err := regexp.Compile(`\A(?:` + p.Match + `)\z`)
	if err != nil {
		return fmt.Errorf("ArgPolicy.Match: %w", err)
	}
	p.re = re
	return nil
}

// expandPaths resolves $HOME and $TMPDIR in a path list and drops entries that
// expand to nothing, so a default list stays usable on a machine where one of
// the variables is unset.
func expandPaths(in []string) []string {
	var out []string
	for _, p := range in {
		missing := false
		e := os.Expand(p, func(k string) string {
			var v string
			switch k {
			case "HOME":
				v = os.Getenv("HOME")
			case "TMPDIR":
				v = strings.TrimSuffix(os.Getenv("TMPDIR"), "/")
			}
			// An unset variable drops the whole entry. Expanding it to nothing
			// would turn $HOME/.ssh into /.ssh, which is a different path that
			// happens to exist, so the rule would silently apply somewhere the
			// operator never named.
			if v == "" {
				missing = true
			}
			return v
		})
		if missing || e == "" || !strings.HasPrefix(e, "/") {
			continue
		}
		out = append(out, resolveBoth(filepath.Clean(e))...)
	}
	return out
}

// resolveBoth returns a path and, when it differs, the same path with symlinks
// resolved.
//
// Both, because it is not knowable from here which spelling a rule will be
// matched against. Seatbelt matches the resolved path, so a rule naming /tmp
// silently covers nothing on darwin where /tmp is a link to /private/tmp. That
// failure is invisible in the profile, which lists the rule the operator asked
// for: the write denial appears to be in force and is not, and a read denial
// that appears to be in force and is not is worse than an absent one.
func resolveBoth(p string) []string {
	real, err := filepath.EvalSymlinks(p)
	if err != nil || real == p {
		return []string{p}
	}
	return []string{p, real}
}

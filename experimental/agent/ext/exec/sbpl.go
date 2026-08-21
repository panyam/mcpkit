package exec

import (
	"sort"
	"strings"
)

// buildSBPL renders a Policy as a Seatbelt profile for sandbox-exec.
//
// It lives outside the darwin build tag so the profile can be tested
// anywhere. The golden test pins what we generate; it cannot pin that Apple
// keeps honoring it, and sandbox-exec has been deprecated for years with an
// undocumented profile language, so the live test is the only thing that
// checks the other half.
//
// Rule order matters and is not the order a reader expects: Seatbelt takes the
// LAST matching rule, so the read denials come after the blanket read allow
// and override it.
func buildSBPL(p Policy) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s); b.WriteByte('\n') }

	w("(version 1)")
	w("(deny default)")
	w("")
	w(";; A command is a process tree: it forks, execs, waits, and signals its")
	w(";; own children. None of that is the capability being confined.")
	w("(allow process-fork)")
	w("(allow process-exec*)")
	w("(allow signal (target same-sandbox))")
	w("(allow sysctl-read)")
	w("(allow mach-lookup)")
	w("(allow ipc-posix-shm)")
	w("(allow file-ioctl)")
	w("")
	w(";; Reads are open, then carved back. Confining reads to the workspace")
	w(";; breaks every toolchain, which reads compilers, SDKs, module caches and")
	w(";; system libraries from all over the disk, and the breakage reads as a")
	w(";; bug rather than as policy. What is left is credential theft, which the")
	w(";; denials below address directly.")
	w("(allow file-read*)")
	for _, path := range dedupe(p.DenyRead) {
		w("(deny file-read* (subpath " + sbplString(path) + "))")
	}
	w("")
	w(";; Writes are closed, then opened onto the workspace and the caches a")
	w(";; build needs. A path missing from here fails loudly, which is the")
	w(";; direction to fail in.")
	if writes := dedupe(p.Write); len(writes) > 0 {
		w("(allow file-write*")
		for _, path := range writes {
			w("    (subpath " + sbplString(path) + ")")
		}
		w(")")
	}
	w("(allow file-write-data")
	w(`    (literal "/dev/null")`)
	w(`    (literal "/dev/zero")`)
	w(`    (literal "/dev/random")`)
	w(`    (literal "/dev/urandom")`)
	w(`    (literal "/dev/tty")`)
	w(`    (literal "/dev/dtracehelper")`)
	w(")")
	w("")
	if p.AllowNetwork {
		w(";; The command declared it needs the network.")
		w("(allow network*)")
	} else {
		w(";; Loopback stays open because a test suite that starts a server on")
		w(";; 127.0.0.1 is the ordinary case, not an escape. Everything that")
		w(";; leaves the machine is denied, which is what stops a build script")
		w(";; posting the workspace somewhere.")
		w("(allow network-bind (local ip \"localhost:*\"))")
		w("(allow network-outbound (remote ip \"localhost:*\"))")
	}
	return b.String()
}

// sbplString quotes a path for the profile language, which is close enough to
// C string syntax for these two escapes to cover it.
func sbplString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// dedupe sorts and removes duplicates so the profile is stable regardless of
// the order roots and cache paths were configured in, which is what makes a
// golden test worth having.
func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

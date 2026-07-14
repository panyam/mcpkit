// Command check-auth-markers keeps the ext/auth spec-coverage matrix
// self-verifying (issue 504).
//
// conformance/AUTH_SPEC_COVERAGE.md maps each spec clause (MUST / SHOULD /
// MAY) to the ext/auth site that implements it. That file:line link drifts
// as code moves. This tool asserts the inverse, drift-proof invariant: for
// every matrix row whose implementation cell cites an ext/auth non-test
// file, that file carries an inline `// <clause>` marker citing the same
// clause. The markers travel with the code, so `grep -rE "// (RFC|MCP-Auth|
// SEP-)"` over ext/auth reconstructs the matrix's clause→site mapping.
//
// Matching is line-independent (file + clause, not file:line) and tolerant:
// an RFC clause matches when the file has a comment naming that RFC number
// and section; a SEP clause matches on the SEP number. Run via
// `make check-auth-markers`.
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const matrixPath = "conformance/AUTH_SPEC_COVERAGE.md"

// clause is a normalized spec citation extracted from the matrix.
type clause struct {
	raw  string // human-readable, e.g. "RFC 8707 §2"
	kind string // "RFC" | "SEP" | "MCP-Auth"
	num  string // RFC/SEP number, or "" for MCP-Auth
	sec  string // section, e.g. "2", "3.1", "C6", "2.3"
}

var (
	reHeading    = regexp.MustCompile(`^#{2,3}\s+(.+)`)
	reSectionRFC = regexp.MustCompile(`RFC (\d+)`)
	reSectionSEP = regexp.MustCompile(`SEP-(\d+)`)
	reFullRFC    = regexp.MustCompile(`RFC (\d+) §([0-9.]+)`)
	reFullSEP    = regexp.MustCompile(`SEP-(\d+)`)
	reScenSEP    = regexp.MustCompile(`sep-(\d+)-`)
	reFullMCP    = regexp.MustCompile(`MCP-Auth §([0-9.A-Za-z]+)`)
	reLeadingSec = regexp.MustCompile(`^§([0-9.A-Za-z]+)`)
	reExtAuth    = regexp.MustCompile(`ext/auth/(\w+)\.go`)
)

// parseClause resolves the row's clause cell (which may be section-relative)
// against the current section spec into a full clause. Returns ok=false when
// the cell carries no recognizable citation.
func parseClause(cell, sectionKind, sectionNum string) (clause, bool) {
	if m := reFullRFC.FindStringSubmatch(cell); m != nil {
		return clause{raw: m[0], kind: "RFC", num: m[1], sec: m[2]}, true
	}
	if m := reFullMCP.FindStringSubmatch(cell); m != nil {
		return clause{raw: m[0], kind: "MCP-Auth", sec: m[1]}, true
	}
	if m := reScenSEP.FindStringSubmatch(cell); m != nil {
		return clause{raw: "SEP-" + m[1], kind: "SEP", num: m[1]}, true
	}
	if m := reFullSEP.FindStringSubmatch(cell); m != nil {
		return clause{raw: "SEP-" + m[1], kind: "SEP", num: m[1]}, true
	}
	// Section-relative §X — combine with the current section heading.
	if m := reLeadingSec.FindStringSubmatch(cell); m != nil && sectionKind != "" {
		switch sectionKind {
		case "RFC":
			return clause{raw: fmt.Sprintf("RFC %s §%s", sectionNum, m[1]), kind: "RFC", num: sectionNum, sec: m[1]}, true
		case "SEP":
			return clause{raw: "SEP-" + sectionNum, kind: "SEP", num: sectionNum}, true
		case "MCP-Auth":
			return clause{raw: "MCP-Auth §" + m[1], kind: "MCP-Auth", sec: m[1]}, true
		}
	}
	return clause{}, false
}

// requirement pairs a clause with the ext/auth file that must carry its marker.
type requirement struct {
	file   string // e.g. "token_source.go"
	clause clause
}

func parseMatrix() ([]requirement, error) {
	f, err := os.Open(matrixPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var reqs []requirement
	seen := map[string]bool{}
	var sectionKind, sectionNum string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if h := reHeading.FindStringSubmatch(line); h != nil {
			t := h[1]
			switch {
			case reSectionRFC.MatchString(t):
				sectionKind, sectionNum = "RFC", reSectionRFC.FindStringSubmatch(t)[1]
			case reSectionSEP.MatchString(t):
				sectionKind, sectionNum = "SEP", reSectionSEP.FindStringSubmatch(t)[1]
			case strings.Contains(t, "MCP Authorization"):
				sectionKind, sectionNum = "MCP-Auth", ""
			}
			continue
		}
		if !strings.HasPrefix(line, "| ") || strings.Contains(line, "---") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) < 2 {
			continue
		}
		clauseCell := strings.TrimSpace(cells[0])
		implCell := strings.TrimSpace(cells[1])
		if clauseCell == "" || clauseCell == "Clause" {
			continue
		}
		cl, ok := parseClause(clauseCell, sectionKind, sectionNum)
		if !ok {
			continue
		}
		for _, m := range reExtAuth.FindAllStringSubmatch(implCell, -1) {
			file := m[1] + ".go"
			if strings.HasSuffix(m[1], "_test") {
				continue
			}
			key := file + "|" + cl.raw
			if seen[key] {
				continue
			}
			seen[key] = true
			reqs = append(reqs, requirement{file: file, clause: cl})
		}
	}
	return reqs, sc.Err()
}

// fileHasClause reports whether an inline // comment in ext/auth/<file>
// cites the clause. RFC: names the RFC number and the section; SEP: names
// the SEP number; MCP-Auth: names the section (e.g. §C6, §2.3).
func fileHasClause(file string, cl clause) (bool, error) {
	data, err := os.ReadFile("ext/auth/" + file)
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		i := strings.Index(line, "//")
		if i < 0 {
			continue
		}
		c := line[i:]
		switch cl.kind {
		case "RFC":
			if strings.Contains(c, "RFC "+cl.num) && strings.Contains(c, "§"+cl.sec) {
				return true, nil
			}
		case "SEP":
			if strings.Contains(c, "SEP-"+cl.num) {
				return true, nil
			}
		case "MCP-Auth":
			if strings.Contains(c, "§"+cl.sec) {
				return true, nil
			}
		}
	}
	return false, nil
}

func main() {
	reqs, err := parseMatrix()
	if err != nil {
		fmt.Fprintf(os.Stderr, "check-auth-markers: %v\n", err)
		os.Exit(2)
	}
	var missing []requirement
	for _, r := range reqs {
		ok, err := fileHasClause(r.file, r.clause)
		if err != nil {
			fmt.Fprintf(os.Stderr, "check-auth-markers: %v\n", err)
			os.Exit(2)
		}
		if !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		fmt.Printf("MISSING inline spec markers for %d matrix clause(s):\n", len(missing))
		for _, r := range missing {
			fmt.Printf("  ext/auth/%s  needs a // comment citing  %s\n", r.file, r.clause.raw)
		}
		fmt.Fprintf(os.Stderr, "\nAUTH_SPEC_COVERAGE.md cites these ext/auth sites, but the code carries no inline\n"+
			"marker for the clause. Add a `// %s` comment at the impl site (issue 504).\n", "<clause>")
		os.Exit(1)
	}
	fmt.Printf("check-auth-markers: all %d matrix-cited ext/auth clauses carry inline markers.\n", len(reqs))
}

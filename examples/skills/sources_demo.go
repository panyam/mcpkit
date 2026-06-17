// Source-loading helpers for examples/skills demo. Lives next to
// main.go so the --source flag dispatch stays readable; not intended
// as adopter-facing API. See ext/skills/sources.go for the production
// helpers.

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/ext/skills/fsutil"
)

// buildSourceOption returns the ProviderOption matching the
// --source flag, a human-readable label for the serve log, and an
// optional cleanup function the caller must defer.
//
// Supported modes:
//   - "dir" (default): skills.WithDirectory(skillsDir)
//   - "archive": skills.OpenArchive(sourcePath) → WithFS
//   - "archives-dir": skills.OpenArchivesDir(sourcePath) → WithFS
//   - "github": skills.FetchGitHubArchive(...) → WithFS, spec format
//     "owner/repo[@ref][:subdir]" (ref defaults to "main")
//   - "multi": local at FS root + archive + github sub-mounts via
//     fsutil.NewMountFS. Operators add ad-hoc sub-mounts via the
//     --extra prefix:./path[,prefix:./path...] flag.
func buildSourceOption(mode, skillsDir, sourcePath, githubSpec string, extras []extraMount) (skills.ProviderOption, string, func(), error) {
	switch strings.ToLower(mode) {
	case "", "dir":
		return skills.WithDirectory(skillsDir), fmt.Sprintf("dir=%s", skillsDir), nil, nil
	case "archive":
		if sourcePath == "" {
			return nil, "", nil, fmt.Errorf("--source=archive requires --source-path=<archive file>")
		}
		src, err := skills.OpenArchive(sourcePath)
		if err != nil {
			return nil, "", nil, err
		}
		return skills.WithFS(src), fmt.Sprintf("archive=%s", sourcePath), func() { src.Close() }, nil
	case "archives-dir":
		if sourcePath == "" {
			return nil, "", nil, fmt.Errorf("--source=archives-dir requires --source-path=<directory of archives>")
		}
		src, err := skills.OpenArchivesDir(sourcePath)
		if err != nil {
			return nil, "", nil, err
		}
		return skills.WithFS(src), fmt.Sprintf("archives-dir=%s", sourcePath), func() { src.Close() }, nil
	case "github":
		if githubSpec == "" {
			return nil, "", nil, fmt.Errorf("--source=github requires --source-github=owner/repo[@ref][:subdir]")
		}
		owner, repo, ref, subdir, err := parseGitHubSpec(githubSpec)
		if err != nil {
			return nil, "", nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var opts []skills.GitHubOption
		if subdir != "" {
			opts = append(opts, skills.WithGitHubSubdir(subdir))
		}
		src, err := skills.FetchGitHubArchive(ctx, owner, repo, ref, opts...)
		if err != nil {
			return nil, "", nil, err
		}
		return skills.WithFS(src), fmt.Sprintf("github=%s/%s@%s subdir=%s", owner, repo, ref, subdir), func() { src.Close() }, nil
	case "multi":
		return buildMultiSource(skillsDir, extras)
	}
	return nil, "", nil, fmt.Errorf("invalid --source: %q (want dir|archive|archives-dir|github|multi)", mode)
}

// buildMultiSource composes three demo sources into one Provider via
// fsutil.NewMountFS:
//   - bundled local skills mounted at the FS root (no Path) so they
//     resolve at skill://<skill-name>/... (matches the walkthrough's
//     hardcoded URIs)
//   - "archived/" sub-mount: a single archive packed from one bundled
//     skill, exercising the archive-file source
//   - "github/" sub-mount: fetched from anthropics/skills (best-effort;
//     soft-fail on network errors so the demo still runs offline)
//
// Per-source layers (--extra prefix:path) are appended as additional
// sub-mounts under their declared prefixes.
//
// Demonstrates that MountFS composes any fs.FS — local + archive +
// remote — into one catalog the Provider walks unchanged.
func buildMultiSource(skillsDir string, extras []extraMount) (skills.ProviderOption, string, func(), error) {
	var mounts []fsutil.Mount
	var closers []io.Closer
	cleanup := func() {
		for _, c := range closers {
			c.Close()
		}
	}

	// Root mount: bundled local skills. Empty Path = mounted at FS
	// root so URIs surface as skill://<skill-name>/SKILL.md.
	mounts = append(mounts, fsutil.Mount{FSys: os.DirFS(skillsDir)})
	labels := []string{"local"}

	// Sub-mount: archived/. Best-effort.
	archivePath, err := stageDemoArchive(skillsDir, "git-workflow")
	if err != nil {
		log.Printf("[skills-demo] multi: skipping archive sub-mount: %v", err)
	} else {
		closers = append(closers, removeOnClose{path: archivePath})
		arc, err := skills.OpenArchive(archivePath)
		if err != nil {
			log.Printf("[skills-demo] multi: open archive sub-mount: %v", err)
		} else {
			mounts = append(mounts, fsutil.Mount{Path: "archived", FSys: arc, Closer: arc})
			labels = append(labels, "archived")
		}
	}

	// Sub-mount: github/. Best-effort.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gh, err := skills.FetchGitHubArchive(ctx, "anthropics", "skills", "main",
		skills.WithGitHubSubdir("skills"))
	if err != nil {
		log.Printf("[skills-demo] multi: skipping github sub-mount: %v", err)
	} else {
		mounts = append(mounts, fsutil.Mount{Path: "github", FSys: gh, Closer: gh})
		labels = append(labels, "github")
	}

	// Operator-declared extras (--extra prefix:./path).
	for _, ex := range extras {
		mounts = append(mounts, fsutil.Mount{Path: ex.prefix, FSys: os.DirFS(ex.path)})
		labels = append(labels, ex.prefix+"="+ex.path)
	}

	composed, err := fsutil.NewMountFS(mounts...)
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	// Compose every mount's closer through the MountFS lifecycle.
	closers = []io.Closer{composed}

	label := fmt.Sprintf("multi=[%s]", strings.Join(labels, ","))
	return skills.WithFS(composed), label, cleanup, nil
}

// extraMount is one --extra prefix:./path declaration.
type extraMount struct {
	prefix string
	path   string
}

// parseExtraMounts accepts comma-separated prefix:path pairs (e.g.
// "science:./scienceskills,math:./mathskills") and returns the parsed
// list. Empty input returns nil. Whitespace around tokens is trimmed.
func parseExtraMounts(spec string) ([]extraMount, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []extraMount
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		i := strings.Index(tok, ":")
		if i < 1 || i == len(tok)-1 {
			return nil, fmt.Errorf("invalid --extra token %q: want prefix:./path", tok)
		}
		out = append(out, extraMount{prefix: tok[:i], path: tok[i+1:]})
	}
	return out, nil
}

// stageDemoArchive packs one bundled skill via PackSkill into a
// tempfile tar.gz so the multi-source demo exercises the archive-file
// code path without committing a fixture to the repo. The resulting
// archive has SKILL.md at its root; skills.OpenArchive's auto-wrap
// promotes it to "<frontmatter-name>/SKILL.md" at load time, so the
// archive serves cleanly under any prefix layer. Caller removes the
// file via removeOnClose.
func stageDemoArchive(skillsDir, skillName string) (string, error) {
	data, err := skills.PackSkill(os.DirFS(skillsDir), skillName, skills.ArchiveFormatTarGz)
	if err != nil {
		return "", fmt.Errorf("pack %q: %w", skillName, err)
	}
	tmp := filepath.Join(os.TempDir(), "skills-demo-"+skillName+".tar.gz")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("write %q: %w", tmp, err)
	}
	return tmp, nil
}

type removeOnClose struct{ path string }

func (r removeOnClose) Close() error { return os.Remove(r.path) }

// parseGitHubSpec accepts "owner/repo[@ref][:subdir]". Returns the
// four pieces. Default ref is "main"; default subdir is empty.
func parseGitHubSpec(spec string) (owner, repo, ref, subdir string, err error) {
	rest := spec
	if i := strings.Index(rest, ":"); i >= 0 {
		subdir = rest[i+1:]
		rest = rest[:i]
	}
	if i := strings.Index(rest, "@"); i >= 0 {
		ref = rest[i+1:]
		rest = rest[:i]
	} else {
		ref = "main"
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", "", fmt.Errorf("invalid github spec %q: want owner/repo[@ref][:subdir]", spec)
	}
	return parts[0], parts[1], ref, subdir, nil
}

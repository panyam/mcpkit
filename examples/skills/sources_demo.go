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
//   - "multi": layered local + archive + github into one fs.FS via
//     fsutil.NewLayered, demonstrating the full source-adapter
//     composition story in one server run
func buildSourceOption(mode, skillsDir, sourcePath, githubSpec string) (skills.ProviderOption, string, func(), error) {
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
		return buildMultiSource(skillsDir)
	}
	return nil, "", nil, fmt.Errorf("invalid --source: %q (want dir|archive|archives-dir|github|multi)", mode)
}

// buildMultiSource layers three demo sources into one Provider:
//   - "local/": the bundled skills directory (always present)
//   - "archived/": a single archive packed from one bundled skill
//   - "github/": fetched from anthropics/skills (best-effort; soft-fail
//     on network errors so the demo still runs offline)
//
// Demonstrates that fsutil.NewLayered composes any fs.FS — local +
// archive + remote — into one catalog the Provider walks unchanged.
func buildMultiSource(skillsDir string) (skills.ProviderOption, string, func(), error) {
	var layers []fsutil.Layer
	var closers []io.Closer
	cleanup := func() {
		for _, c := range closers {
			c.Close()
		}
	}

	// Layer 1: local bundled skills.
	layers = append(layers, fsutil.Layer{Prefix: "local", FSys: os.DirFS(skillsDir)})

	// Layer 2: pack one skill into a tempfile archive and serve that
	// to exercise the archive-file source. Removed when cleanup runs.
	archivePath, err := stageDemoArchive(skillsDir, "git-workflow")
	if err != nil {
		log.Printf("[skills-demo] multi: skipping archive layer: %v", err)
	} else {
		closers = append(closers, removeOnClose{path: archivePath})
		arc, err := skills.OpenArchive(archivePath)
		if err != nil {
			log.Printf("[skills-demo] multi: open archive layer: %v", err)
		} else {
			closers = append(closers, arc)
			layers = append(layers, fsutil.Layer{Prefix: "archived", FSys: arc, Closer: arc})
		}
	}

	// Layer 3: GitHub-fetched (best-effort).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gh, err := skills.FetchGitHubArchive(ctx, "anthropics", "skills", "main",
		skills.WithGitHubSubdir("skills"))
	if err != nil {
		log.Printf("[skills-demo] multi: skipping github layer: %v", err)
	} else {
		closers = append(closers, gh)
		layers = append(layers, fsutil.Layer{Prefix: "github", FSys: gh, Closer: gh})
	}

	if len(layers) == 0 {
		cleanup()
		return nil, "", nil, fmt.Errorf("multi: no sources loaded")
	}

	composed, err := fsutil.NewLayered(layers...)
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	// Compose every layer's closer through the LayeredFS lifecycle.
	closers = []io.Closer{composed}

	prefixes := make([]string, 0, len(layers))
	for _, l := range layers {
		prefixes = append(prefixes, l.Prefix)
	}
	label := fmt.Sprintf("multi=[%s]", strings.Join(prefixes, ","))
	return skills.WithFS(composed), label, cleanup, nil
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

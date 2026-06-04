package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/panyam/mcpkit/cmd/common"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/spf13/cobra"
)

func newVerifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify <dir>",
		Short: "Lint a skills directory for SEP-2640 compliance",
		Long: `Walk a directory and check that every skill it contains
satisfies SEP-2640's structural rules: SKILL.md at each skill's root,
frontmatter required fields, name matches final segment of skill path,
no nested skills, valid skill-name character class.

verify exits 0 when the directory is clean and 1 when any rule
violates. The full ErrSkillNameMismatch / ErrNestedSkill /
ErrFrontmatterMissing* error surfaces on stderr so an author sees
exactly which file needs editing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			info, err := os.Stat(dir)
			if err != nil {
				return fmt.Errorf("verify: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("verify: %s is not a directory", dir)
			}

			out := cmd.OutOrStdout()
			painter := common.NewPainter(parseColorMode(colorFlag), out)

			// NewProvider walks once and surfaces the first structural
			// error. For a verifier we want every error, not the first
			// one, so we walk per-skill manually.
			skillDirs, err := findSkillDirs(dir)
			if err != nil {
				return fmt.Errorf("verify: walk %s: %w", dir, err)
			}
			if len(skillDirs) == 0 {
				fmt.Fprintf(out, "verify: %s contains no SKILL.md files\n", dir)
				return nil
			}

			problems := 0
			for _, skillDir := range skillDirs {
				if err := verifySkill(skillDir); err != nil {
					fmt.Fprintf(out, "%s %s\n    %s\n",
						painter.Red("✗"),
						relTo(dir, skillDir),
						painter.Red(err.Error()),
					)
					problems++
					continue
				}
				fmt.Fprintf(out, "%s %s\n", painter.Green("✓"), relTo(dir, skillDir))
			}
			fmt.Fprintf(out, "\n%d skills, %d %s\n", len(skillDirs), problems, pluralize(problems, "error", "errors"))
			if problems > 0 {
				return fmt.Errorf("%d %s", problems, pluralize(problems, "error", "errors"))
			}
			return nil
		},
	}
	return cmd
}

// findSkillDirs walks root looking for directories that contain a
// SKILL.md file. Each match becomes a candidate to verify.
func findSkillDirs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != skills.ManifestFilename {
			return nil
		}
		out = append(out, filepath.Dir(path))
		return nil
	})
	return out, err
}

// verifySkill runs the same checks the Provider does at registration
// time on a single skill directory. We re-run them individually so a
// directory with multiple problems surfaces all of them rather than
// halting on the first.
func verifySkill(skillDir string) error {
	src, err := os.ReadFile(filepath.Join(skillDir, skills.ManifestFilename))
	if err != nil {
		return err
	}
	fm, _, err := skills.ParseFrontmatter(src)
	if err != nil {
		return err
	}
	base := filepath.Base(skillDir)
	if fm.Name != base {
		return fmt.Errorf("%w: directory %q vs frontmatter name %q",
			skills.ErrSkillNameMismatch, base, fm.Name)
	}
	if err := skills.ValidateSkillName(base); err != nil {
		return err
	}
	// Nested SKILL.md check: walk the skill's subtree and reject any
	// SKILL.md that sits strictly below the skill root. The skill's own
	// root SKILL.md (filepath.Dir(path) == skillDir) is the expected
	// manifest and must not be flagged.
	nestedErr := filepath.WalkDir(skillDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != skills.ManifestFilename {
			return nil
		}
		if filepath.Dir(path) == skillDir {
			// The skill's own root SKILL.md.
			return nil
		}
		return fmt.Errorf("%w: %s", skills.ErrNestedSkill, path)
	})
	if nestedErr != nil {
		return nestedErr
	}
	return nil
}

func relTo(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return rel
}

// Used for sentinel comparison sanity.
var (
	_ = errors.Is
)

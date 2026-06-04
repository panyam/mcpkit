package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/mcpkit/ext/skills"
	"github.com/spf13/cobra"
)

func newPackCmd() *cobra.Command {
	var (
		formatFlag string
		outFlag    string
	)
	cmd := &cobra.Command{
		Use:   "pack <skill-dir>",
		Short: "Pack a single skill into .tar.gz or .zip",
		Long: `Read the named skill directory and produce an archive
suitable for distribution via SEP-2640's archive entry type. The
archive contains every file under the skill dir, with SKILL.md at the
archive root.

Format defaults to .tar.gz. Output path defaults to <skill-dir>.<ext>
in the current directory.

Examples:
  mcpskills pack ./my-skills/git-workflow
  mcpskills pack ./my-skills/git-workflow --format zip
  mcpskills pack ./my-skills/git-workflow -o /tmp/git.tar.gz`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			skillDir := args[0]
			format, err := parseArchiveFormat(formatFlag)
			if err != nil {
				return err
			}
			info, err := os.Stat(skillDir)
			if err != nil {
				return fmt.Errorf("pack: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("pack: %s is not a directory", skillDir)
			}

			parent := filepath.Dir(absOrDie(skillDir))
			base := filepath.Base(absOrDie(skillDir))
			fsys := os.DirFS(parent)
			data, err := skills.PackSkill(fsys, base, format)
			if err != nil {
				return fmt.Errorf("pack: %w", err)
			}

			outPath := outFlag
			if outPath == "" {
				outPath = base + format.Suffix()
			}
			if err := os.WriteFile(outPath, data, 0o644); err != nil {
				return fmt.Errorf("pack: write %s: %w", outPath, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes, %s)\n", outPath, len(data), format.MimeType())
			return nil
		},
	}
	cmd.Flags().StringVar(&formatFlag, "format", "tar.gz", "archive format: tar.gz | zip")
	cmd.Flags().StringVarP(&outFlag, "out", "o", "", "output file path (defaults to <skill-dir>.<ext>)")
	return cmd
}

func newUnpackCmd() *cobra.Command {
	var (
		outDir     string
		formatFlag string
		maxBytes   int64
	)
	cmd := &cobra.Command{
		Use:   "unpack <archive>",
		Short: "Extract a skill archive with SEP-2640 safety guards",
		Long: `Decode a .tar.gz or .zip archive into a directory,
enforcing the four SEP-2640 archive safety MUSTs:

  - reject ../ path traversal sequences
  - reject absolute paths
  - reject symlinks or hardlinks that escape the archive root
  - enforce a total-unpacked-size limit (default 100 MiB)

Format is auto-detected from the suffix and magic bytes. --max-bytes
overrides the default cap.

Examples:
  mcpskills unpack ./git-workflow.tar.gz
  mcpskills unpack ./skill.zip -o ./extracted
  mcpskills unpack ./big.tar.gz --max-bytes=$((500 * 1024 * 1024))`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			archivePath := args[0]
			data, err := os.ReadFile(archivePath)
			if err != nil {
				return fmt.Errorf("unpack: %w", err)
			}
			peek := data
			if len(peek) > 64 {
				peek = peek[:64]
			}
			format := skills.DetectArchiveFormat(archivePath, peek)
			if formatFlag != "" {
				f, err := parseArchiveFormat(formatFlag)
				if err != nil {
					return err
				}
				format = f
			}
			entries, err := skills.UnpackBytes(data, format, maxBytes)
			if err != nil {
				return fmt.Errorf("unpack: %w", err)
			}

			dest := outDir
			if dest == "" {
				dest = strings.TrimSuffix(filepath.Base(archivePath), format.Suffix())
				if dest == "" || dest == filepath.Base(archivePath) {
					dest = "extracted"
				}
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return fmt.Errorf("unpack: mkdir %s: %w", dest, err)
			}
			for _, e := range entries {
				outPath := filepath.Join(dest, e.Path)
				if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
					return fmt.Errorf("unpack: mkdir %s: %w", filepath.Dir(outPath), err)
				}
				if err := os.WriteFile(outPath, e.Body, fileMode(e.Mode)); err != nil {
					return fmt.Errorf("unpack: write %s: %w", outPath, err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "extracted %d entries to %s\n", len(entries), dest)
			return nil
		},
	}
	cmd.Flags().StringVarP(&outDir, "out", "o", "", "output directory (defaults to archive name without extension)")
	cmd.Flags().StringVar(&formatFlag, "format", "", "force format: tar.gz | zip (default: auto-detect)")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", skills.DefaultArchiveMaxBytes, "total unpacked size cap in bytes (default 100 MiB; pass -1 to disable)")
	return cmd
}

func parseArchiveFormat(s string) (skills.ArchiveFormat, error) {
	switch strings.ToLower(s) {
	case "tar.gz", "targz", "":
		return skills.ArchiveFormatTarGz, nil
	case "zip":
		return skills.ArchiveFormatZip, nil
	}
	return skills.ArchiveFormatUnknown, fmt.Errorf("invalid format %q (want tar.gz or zip)", s)
}

func absOrDie(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func fileMode(m fs.FileMode) fs.FileMode {
	if m == 0 {
		return 0o644
	}
	return m & fs.ModePerm
}

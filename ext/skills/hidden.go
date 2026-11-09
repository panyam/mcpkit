package skills

import (
	"io/fs"
	"strings"
)

// WithHiddenFiles makes the Provider serve hidden files and directories,
// meaning those whose name starts with a dot.
//
// By default a Provider skips them everywhere it enumerates a skill: the
// skills/list and skills/get resource manifests, resources/list and
// resources/read, resources/directory/read, archive packing, and the
// fsnotify watcher. A hidden entry is usually local configuration or
// repository metadata rather than skill content (.env, .git/, .DS_Store),
// and serving it can leak secrets. SEP-2640 and the Agent Skills
// specification are both silent on hidden files, and the TypeScript SDK
// also skips them.
//
// The root passed to WithDirectory or WithFS may itself be hidden, such as
// ~/.claude/skills. Only entries below the root are filtered.
func WithHiddenFiles() ProviderOption {
	return func(c *providerConfig) {
		c.includeHidden = true
	}
}

// isHiddenName reports whether a single path segment names a hidden entry.
// "." and ".." are path syntax, not names, so neither counts.
func isHiddenName(name string) bool {
	return len(name) > 1 && name[0] == '.' && name != ".."
}

// hasHiddenSegment reports whether any segment of a slash-separated path is
// hidden.
func hasHiddenSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if isHiddenName(seg) {
			return true
		}
	}
	return false
}

// skipHidden is the first step of every fs.WalkDir callback under a skill.
// For a hidden entry below root it returns skip=true. For a hidden
// directory it also returns fs.SkipDir, so the walk never descends into it.
// The callback returns err as soon as skip is true.
func (c *providerConfig) skipHidden(walkPath, root string, d fs.DirEntry) (skip bool, err error) {
	if c.includeHidden || walkPath == root || !isHiddenName(d.Name()) {
		return false, nil
	}
	if d.IsDir() {
		return true, fs.SkipDir
	}
	return true, nil
}

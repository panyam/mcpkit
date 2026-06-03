package skills

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// frontmatterDelimiter is the YAML front-matter fence per the Agent Skills
// specification. SEP-2640 delegates the format to that spec.
var frontmatterDelimiter = []byte("---")

// utf8BOM is the UTF-8 byte-order mark. Some editors (notably Windows
// Notepad) prepend it; the parser strips it before scanning.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// ParseFrontmatter parses the YAML front-matter block at the head of a
// SKILL.md document and returns the parsed Frontmatter together with the
// remaining body bytes.
//
// Behavior:
//   - A leading UTF-8 BOM is stripped before scanning.
//   - CRLF line endings are normalized to LF.
//   - The document MUST begin with a line containing exactly "---"
//     (ErrMissingFrontmatter otherwise).
//   - The block MUST be terminated by another line containing exactly "---"
//     (ErrUnterminatedFrontmatter otherwise).
//   - The YAML between the delimiters MUST decode to a mapping
//     (ErrNonMappingFrontmatter otherwise).
//   - The mapping MUST contain non-empty name and description fields
//     (ErrFrontmatterMissingName / ErrFrontmatterMissingDescription).
//
// The returned body bytes are everything after the closing delimiter line
// (including the trailing newline that follows it, if any). The body is
// returned verbatim so callers can republish it without re-encoding.
func ParseFrontmatter(src []byte) (Frontmatter, []byte, error) {
	src = bytes.TrimPrefix(src, utf8BOM)
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	src = bytes.ReplaceAll(src, []byte("\r"), []byte("\n"))

	open, openEnd, ok := matchDelimiter(src, 0)
	if !ok || open != 0 {
		return Frontmatter{}, nil, ErrMissingFrontmatter
	}

	close, closeEnd, ok := matchDelimiter(src, openEnd)
	if !ok {
		return Frontmatter{}, nil, ErrUnterminatedFrontmatter
	}

	yamlBytes := src[openEnd:close]

	var node yaml.Node
	if err := yaml.Unmarshal(yamlBytes, &node); err != nil {
		return Frontmatter{}, nil, fmt.Errorf("skills: invalid YAML frontmatter: %w", err)
	}
	mapping, err := mappingFrom(&node)
	if err != nil {
		return Frontmatter{}, nil, err
	}

	fm := Frontmatter{Extra: map[string]any{}}
	for i := 0; i < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		switch k.Value {
		case "name":
			if v.Kind == yaml.ScalarNode {
				fm.Name = v.Value
			}
		case "description":
			if v.Kind == yaml.ScalarNode {
				fm.Description = v.Value
			}
		default:
			var x any
			if err := v.Decode(&x); err != nil {
				return Frontmatter{}, nil, fmt.Errorf("skills: invalid YAML value for %q: %w", k.Value, err)
			}
			fm.Extra[k.Value] = x
		}
	}

	if fm.Name == "" {
		return Frontmatter{}, nil, ErrFrontmatterMissingName
	}
	if fm.Description == "" {
		return Frontmatter{}, nil, ErrFrontmatterMissingDescription
	}

	body := src[closeEnd:]
	// Strip at most one leading newline that follows the closing delimiter.
	// The closing delimiter line ends in a newline that's part of the
	// fence, not the body; the user-authored body conventionally starts on
	// the next line.
	body = bytes.TrimPrefix(body, []byte("\n"))
	return fm, body, nil
}

// ParseFrontmatterReader streams from r and delegates to ParseFrontmatter.
// SKILL.md files are small in practice, so this buffers fully.
func ParseFrontmatterReader(r io.Reader) (Frontmatter, []byte, error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return Frontmatter{}, nil, fmt.Errorf("skills: read SKILL.md: %w", err)
	}
	return ParseFrontmatter(src)
}

// matchDelimiter searches for a YAML "---" fence line starting at or after
// offset. A fence line consists of exactly the three dashes followed by
// either end-of-input or a newline. Trailing whitespace before the newline
// is accepted (some editors add it). Returns (lineStart, postNewline, true)
// on match, where lineStart points at the first dash and postNewline points
// at the byte after the consumed newline (or len(src) if at EOF).
func matchDelimiter(src []byte, offset int) (int, int, bool) {
	for offset <= len(src) {
		// Find next position which is start-of-line.
		lineStart := offset
		// Identify the end of this line.
		newline := bytes.IndexByte(src[lineStart:], '\n')
		var lineEnd, postNewline int
		if newline < 0 {
			lineEnd = len(src)
			postNewline = lineEnd
		} else {
			lineEnd = lineStart + newline
			postNewline = lineEnd + 1
		}
		line := src[lineStart:lineEnd]
		// Strip trailing whitespace from the line so editors that pad with
		// spaces still match.
		line = bytes.TrimRight(line, " \t")
		if bytes.Equal(line, frontmatterDelimiter) {
			return lineStart, postNewline, true
		}
		// Only the FIRST line is a candidate for the opening fence; for
		// the closing fence we must scan subsequent lines. We do this by
		// advancing offset past this line and continuing. But callers ask
		// for "first match at or after offset" — for the opening fence,
		// they pass offset=0 and we'll either match line 1 or not match at
		// all (any subsequent match is not the opening fence). The early
		// return on non-equal handles this:
		if offset == 0 {
			return 0, 0, false
		}
		if newline < 0 {
			return 0, 0, false
		}
		offset = postNewline
	}
	return 0, 0, false
}

// mappingFrom returns the underlying mapping node of a parsed YAML
// document. yaml.Unmarshal into a *yaml.Node wraps the document in a
// DocumentNode containing a single content node; we descend through that.
// Returns ErrNonMappingFrontmatter if the structure does not resolve to a
// mapping.
func mappingFrom(node *yaml.Node) (*yaml.Node, error) {
	if node == nil {
		return nil, ErrNonMappingFrontmatter
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, ErrNonMappingFrontmatter
		}
		return mappingFrom(node.Content[0])
	}
	if node.Kind != yaml.MappingNode {
		return nil, ErrNonMappingFrontmatter
	}
	return node, nil
}

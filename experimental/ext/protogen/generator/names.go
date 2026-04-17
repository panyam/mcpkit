package generator

import (
	"crypto/sha1"
	"encoding/base32"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var (
	strictSnakeCase = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	looseIdentifier = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)
)

const maxToolNameLen = 64

// ValidateToolName validates a custom (user-provided) tool name.
// Custom names must be strict snake_case.
func ValidateToolName(name string) error {
	return validateName(name, true)
}

// ValidateAutoName validates an auto-generated tool/prompt name.
// Auto names allow mixed case (from fully qualified proto names).
func ValidateAutoName(name string) error {
	return validateName(name, false)
}

func validateName(name string, strict bool) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if len(name) > maxToolNameLen {
		return fmt.Errorf("name %q exceeds %d characters", name, maxToolNameLen)
	}
	if strings.Contains(name, "__") {
		return fmt.Errorf("name %q cannot contain consecutive underscores", name)
	}
	if strings.HasSuffix(name, "_") {
		return fmt.Errorf("name %q cannot end with underscore", name)
	}
	if strict {
		if !strictSnakeCase.MatchString(name) {
			return fmt.Errorf("name %q must be snake_case (lowercase letters, numbers, underscores, starting with a letter)", name)
		}
	} else {
		if !looseIdentifier.MatchString(name) {
			return fmt.Errorf("name %q must contain only letters, numbers, underscores, and start with a letter", name)
		}
	}
	return nil
}

// DeriveToolName generates a tool name from a proto method's fully qualified name.
// If the name exceeds maxToolNameLen, it is mangled with a hash prefix.
func DeriveToolName(fullName string) string {
	name := strings.ReplaceAll(fullName, ".", "_")
	if len(name) > maxToolNameLen {
		return mangleLongName(name)
	}
	return name
}

// MethodToSnakeCase converts a PascalCase method name to snake_case.
// "GetUserProfile" → "get_user_profile"
func MethodToSnakeCase(name string) string {
	var result strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := rune(name[i-1])
				// Insert underscore before uppercase if previous char is lowercase,
				// or if next char is lowercase (for runs like "HTMLParser" → "html_parser").
				if unicode.IsLower(prev) {
					result.WriteByte('_')
				} else if i+1 < len(name) && unicode.IsLower(rune(name[i+1])) {
					result.WriteByte('_')
				}
			}
			result.WriteRune(unicode.ToLower(r))
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// PrefixWithNamespace prepends a namespace to a tool name.
// Returns the name unchanged if namespace is empty.
func PrefixWithNamespace(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "_" + name
}

// mangleLongName shortens a name that exceeds maxToolNameLen by replacing the
// head with a hash prefix, preserving the most specific (tail) portion.
func mangleLongName(name string) string {
	hash := sha1.Sum([]byte(name))
	prefix := base32.StdEncoding.EncodeToString(hash[:])[:6]
	prefix = strings.ToLower(prefix)

	available := maxToolNameLen - len(prefix) - 1 // -1 for underscore separator
	if available <= 0 {
		return prefix
	}

	tail := name[len(name)-available:]
	return prefix + "_" + tail
}

// CleanComment strips leading/trailing whitespace and comment markers from proto comments.
// Also removes linter directives (buf:lint, @ignore-comment, etc.).
func CleanComment(comment string) string {
	lines := strings.Split(comment, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "//")
		line = strings.TrimSpace(line)

		// Skip linter directives.
		if strings.HasPrefix(line, "buf:lint:") || strings.HasPrefix(line, "@ignore") {
			continue
		}
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, " ")
}

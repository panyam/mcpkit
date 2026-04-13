package generator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateToolName(t *testing.T) {
	valid := []string{
		"get_user",
		"create",
		"list_decks_v2",
		"a",
		"tool_with_numbers_123",
		strings.Repeat("a", 64),
	}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			assert.NoError(t, ValidateToolName(name))
		})
	}

	invalid := []struct {
		name    string
		wantErr string
	}{
		{"", "cannot be empty"},
		{strings.Repeat("a", 65), "exceeds 64"},
		{"GetUser", "must be snake_case"},
		{"get-user", "must be snake_case"},
		{"123tool", "must be snake_case"},
		{"get__user", "consecutive underscores"},
		{"get_user_", "end with underscore"},
		{"get user", "must be snake_case"},
		{"_private", "must be snake_case"},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			err := ValidateToolName(tt.name)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateAutoName(t *testing.T) {
	// Auto-generated names allow mixed case.
	valid := []string{
		"users_v1_UserService_GetUser",
		"get_user",
		"GetUser",
	}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			assert.NoError(t, ValidateAutoName(name))
		})
	}

	invalid := []struct {
		name    string
		wantErr string
	}{
		{"", "cannot be empty"},
		{"123bad", "must contain only"},
		{"has space", "must contain only"},
		{"has__double", "consecutive underscores"},
		{"trailing_", "end with underscore"},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			err := ValidateAutoName(tt.name)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestMethodToSnakeCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"GetUser", "get_user"},
		{"ListDecks", "list_decks"},
		{"CreateDeckV2", "create_deck_v2"},
		{"HTMLParser", "html_parser"},
		{"getUser", "get_user"},
		{"Get", "get"},
		{"ID", "id"},
		{"GetUserByID", "get_user_by_id"},
		{"a", "a"},
		{"ABC", "abc"},
		{"OAuth2Token", "o_auth2_token"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, MethodToSnakeCase(tt.input))
		})
	}
}

func TestDeriveToolName(t *testing.T) {
	t.Run("short name", func(t *testing.T) {
		got := DeriveToolName("users.v1.UserService.GetUser")
		assert.Equal(t, "users_v1_UserService_GetUser", got)
	})

	t.Run("long name gets mangled", func(t *testing.T) {
		long := "very.long.package.name.with.many.segments.ServiceName.MethodNameThatIsAlsoVeryLong"
		got := DeriveToolName(long)
		assert.LessOrEqual(t, len(got), maxToolNameLen)
		// Mangled name preserves the tail (most specific part).
		assert.Contains(t, got, "MethodNameThatIsAlsoVeryLong")
	})

	t.Run("exact max length not mangled", func(t *testing.T) {
		name := strings.Repeat("a.", 31) + "bb" // 31*2 + 2 = 64 after dot→underscore
		got := DeriveToolName(name)
		assert.LessOrEqual(t, len(got), maxToolNameLen)
	})
}

func TestPrefixWithNamespace(t *testing.T) {
	assert.Equal(t, "users_get", PrefixWithNamespace("users", "get"))
	assert.Equal(t, "get", PrefixWithNamespace("", "get"))
}

func TestCleanComment(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"simple",
			" GetUser retrieves a user by ID. ",
			"GetUser retrieves a user by ID.",
		},
		{
			"multiline",
			"// GetUser retrieves a user.\n// Returns NotFound if missing.",
			"GetUser retrieves a user. Returns NotFound if missing.",
		},
		{
			"strips linter directives",
			"// GetUser retrieves a user.\n// buf:lint:IGNORE\n// Good stuff.",
			"GetUser retrieves a user. Good stuff.",
		},
		{
			"strips @ignore",
			"// @ignore-comment\n// The real description.",
			"The real description.",
		},
		{
			"empty",
			"",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CleanComment(tt.input))
		})
	}
}

package generator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/descriptorpb"

	mcpv1 "github.com/panyam/mcpkit/ext/protogen/proto/mcp/v1"
	ptestutil "github.com/panyam/mcpkit/ext/protogen/testutil"
)

// buildExtensionBytes encodes annotation fields as raw proto bytes suitable
// for embedding in MethodOptions or ServiceOptions. Field values are keyed
// by their proto field number.
func buildExtensionBytes(extField protowire.Number, fields map[protowire.Number]any) []byte {
	// Encode inner message fields.
	var inner []byte
	for num, val := range fields {
		switch v := val.(type) {
		case string:
			inner = protowire.AppendTag(inner, num, protowire.BytesType)
			inner = protowire.AppendString(inner, v)
		case bool:
			inner = protowire.AppendTag(inner, num, protowire.VarintType)
			if v {
				inner = protowire.AppendVarint(inner, 1)
			} else {
				inner = protowire.AppendVarint(inner, 0)
			}
		}
	}
	// Wrap as an extension field in the parent options message.
	var buf []byte
	buf = protowire.AppendTag(buf, extField, protowire.BytesType)
	buf = protowire.AppendBytes(buf, inner)
	return buf
}

// methodOptionsWithTool creates MethodOptions with an mcp_tool extension.
func methodOptionsWithTool(fields map[protowire.Number]any) *descriptorpb.MethodOptions {
	opts := &descriptorpb.MethodOptions{}
	raw := buildExtensionBytes(mcpv1.FieldMCPTool, fields)
	opts.ProtoReflect().SetUnknown(raw)
	return opts
}

// serviceOptionsWithNamespace creates ServiceOptions with an mcp_service extension.
func serviceOptionsWithNamespace(namespace string) *descriptorpb.ServiceOptions {
	opts := &descriptorpb.ServiceOptions{}
	raw := buildExtensionBytes(mcpv1.FieldMCPService, map[protowire.Number]any{
		1: namespace,
	})
	opts.ProtoReflect().SetUnknown(raw)
	return opts
}

// makePlugin creates a protogen.Plugin with a single service for testing.
func makePlugin(t *testing.T, svc ptestutil.Service) *protogen.Plugin {
	t.Helper()
	return ptestutil.CreatePlugin(t, &ptestutil.ProtoSet{
		Files: []ptestutil.File{{
			Name: "test.proto",
			Pkg:  "test.v1",
			Messages: []ptestutil.Message{
				{Name: "GetUserRequest", Fields: []ptestutil.Field{
					{Name: "user_id", Number: 1, TypeName: "string"},
				}},
				{Name: "GetUserResponse", Fields: []ptestutil.Field{
					{Name: "name", Number: 1, TypeName: "string"},
				}},
			},
			Services: []ptestutil.Service{svc},
		}},
	})
}

// collectTools is a test helper that runs collectServiceData and returns
// the resulting tools, failing the test on error.
func collectTools(t *testing.T, svc ptestutil.Service) []toolData {
	t.Helper()
	plugin := makePlugin(t, svc)
	protoSvc := ptestutil.FindService(t, plugin, svc.Name)
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, gf)
	require.NoError(t, err)
	return sd.Tools
}

// TestNoAnnotationFallback verifies that methods without annotations use
// the auto-derived snake_case name and comment-based description.
func TestNoAnnotationFallback(t *testing.T) {
	tools := collectTools(t, ptestutil.Service{
		Name: "UserService",
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
		}},
	})

	require.Len(t, tools, 1)
	assert.Equal(t, "get_user", tools[0].ToolName)
	assert.False(t, tools[0].Structured)
	assert.Empty(t, tools[0].Timeout)
}

// TestAnnotationToolName verifies that mcp_tool.name overrides the
// auto-derived tool name.
func TestAnnotationToolName(t *testing.T) {
	tools := collectTools(t, ptestutil.Service{
		Name: "UserService",
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithTool(map[protowire.Number]any{
				1: "fetch_user", // mcp_tool.name
			}),
		}},
	})

	require.Len(t, tools, 1)
	assert.Equal(t, "fetch_user", tools[0].ToolName)
}

// TestAnnotationDescription verifies that mcp_tool.description overrides
// the comment-derived description.
func TestAnnotationDescription(t *testing.T) {
	tools := collectTools(t, ptestutil.Service{
		Name: "UserService",
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithTool(map[protowire.Number]any{
				2: "Retrieve a user by their unique ID", // mcp_tool.description
			}),
		}},
	})

	require.Len(t, tools, 1)
	assert.Equal(t, "Retrieve a user by their unique ID", tools[0].Description)
}

// TestAnnotationTimeout verifies that mcp_tool.timeout is parsed at
// generation time and emitted as a Go duration literal.
func TestAnnotationTimeout(t *testing.T) {
	tools := collectTools(t, ptestutil.Service{
		Name: "UserService",
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithTool(map[protowire.Number]any{
				3: "30s", // mcp_tool.timeout
			}),
		}},
	})

	require.Len(t, tools, 1)
	assert.Contains(t, tools[0].Timeout, "30000000000")
	assert.Contains(t, tools[0].Timeout, "30s")
}

// TestAnnotationStructuredOutput verifies that mcp_tool.structured_output
// sets the Structured flag on toolData.
func TestAnnotationStructuredOutput(t *testing.T) {
	tools := collectTools(t, ptestutil.Service{
		Name: "UserService",
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithTool(map[protowire.Number]any{
				4: true, // mcp_tool.structured_output
			}),
		}},
	})

	require.Len(t, tools, 1)
	assert.True(t, tools[0].Structured)
}

// TestAnnotationNamespace verifies that mcp_service.namespace prefixes
// auto-derived tool names.
func TestAnnotationNamespace(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name: "UserService",
		Options: serviceOptionsWithNamespace("users"),
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
		}},
	})

	protoSvc := ptestutil.FindService(t, plugin, "UserService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, gf)
	require.NoError(t, err)

	require.Len(t, sd.Tools, 1)
	assert.Equal(t, "users_get_user", sd.Tools[0].ToolName)
	assert.Equal(t, "users", sd.Namespace)
}

// TestAnnotationNamespaceWithToolName verifies that mcp_service.namespace
// is combined with mcp_tool.name.
func TestAnnotationNamespaceWithToolName(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name: "UserService",
		Options: serviceOptionsWithNamespace("users"),
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithTool(map[protowire.Number]any{
				1: "get", // mcp_tool.name
			}),
		}},
	})

	protoSvc := ptestutil.FindService(t, plugin, "UserService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, gf)
	require.NoError(t, err)

	require.Len(t, sd.Tools, 1)
	assert.Equal(t, "users_get", sd.Tools[0].ToolName)
}

// TestAnnotationInvalidToolName verifies that an invalid mcp_tool.name
// produces a generation error.
func TestAnnotationInvalidToolName(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name: "UserService",
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithTool(map[protowire.Number]any{
				1: "Bad-Name", // invalid: contains hyphen
			}),
		}},
	})

	protoSvc := ptestutil.FindService(t, plugin, "UserService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	_, err := collectServiceData(protoSvc, gf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mcp_tool.name")
}

// TestAnnotationInvalidTimeout verifies that an unparseable mcp_tool.timeout
// produces a generation error.
func TestAnnotationInvalidTimeout(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name: "UserService",
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithTool(map[protowire.Number]any{
				3: "not-a-duration", // invalid
			}),
		}},
	})

	protoSvc := ptestutil.FindService(t, plugin, "UserService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	_, err := collectServiceData(protoSvc, gf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid timeout")
}

// --- Resource annotation tests ---

// methodOptionsWithResource creates MethodOptions with an mcp_resource extension.
func methodOptionsWithResource(fields map[protowire.Number]any) *descriptorpb.MethodOptions {
	opts := &descriptorpb.MethodOptions{}
	raw := buildExtensionBytes(mcpv1.FieldMCPResource, fields)
	opts.ProtoReflect().SetUnknown(raw)
	return opts
}

// collectResources is a test helper that runs collectServiceData and returns
// the resulting resources, failing the test on error.
func collectResources(t *testing.T, svc ptestutil.Service) []resourceData {
	t.Helper()
	plugin := makePlugin(t, svc)
	protoSvc := ptestutil.FindService(t, plugin, svc.Name)
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, gf)
	require.NoError(t, err)
	return sd.Resources
}

// TestResourceStaticRegistration verifies that a method with mcp_resource
// and no template variables produces a static resource (IsTemplate=false).
func TestResourceStaticRegistration(t *testing.T) {
	resources := collectResources(t, ptestutil.Service{
		Name: "ConfigService",
		Methods: []ptestutil.Method{{
			Name:       "GetSettings",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithResource(map[protowire.Number]any{
				1: "config://app/settings", // uri_template (no params)
				2: "App Settings",          // name
				3: "application/json",      // mime_type
				4: "Application settings",  // description
			}),
		}},
	})

	require.Len(t, resources, 1)
	r := resources[0]
	assert.Equal(t, "GetSettings", r.MethodName)
	assert.Equal(t, "config://app/settings", r.URI)
	assert.Equal(t, "App Settings", r.Name)
	assert.Equal(t, "application/json", r.MimeType)
	assert.Equal(t, "Application settings", r.Description)
	assert.False(t, r.IsTemplate)
	assert.Empty(t, r.Params)
}

// TestResourceTemplateRegistration verifies that a method with mcp_resource
// containing {param} placeholders produces a template resource with extracted params.
func TestResourceTemplateRegistration(t *testing.T) {
	resources := collectResources(t, ptestutil.Service{
		Name: "UserService",
		Methods: []ptestutil.Method{{
			Name:       "GetUserProfile",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithResource(map[protowire.Number]any{
				1: "user://{user_id}/profile", // uri_template with param
				2: "User Profile",
			}),
		}},
	})

	require.Len(t, resources, 1)
	r := resources[0]
	assert.True(t, r.IsTemplate)
	assert.Equal(t, []string{"user_id"}, r.Params)
	assert.Equal(t, "user://{user_id}/profile", r.URI)
}

// TestResourceMimeTypeDefault verifies that omitting mime_type defaults
// to "application/json".
func TestResourceMimeTypeDefault(t *testing.T) {
	resources := collectResources(t, ptestutil.Service{
		Name: "ConfigService",
		Methods: []ptestutil.Method{{
			Name:       "GetSettings",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithResource(map[protowire.Number]any{
				1: "config://app/settings",
				// mime_type omitted
			}),
		}},
	})

	require.Len(t, resources, 1)
	assert.Equal(t, "application/json", resources[0].MimeType)
}

// TestResourceNoURITemplate verifies that mcp_resource with empty uri_template
// produces an error.
func TestResourceNoURITemplate(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name: "BadService",
		Methods: []ptestutil.Method{{
			Name:       "GetStuff",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithResource(map[protowire.Number]any{
				2: "Missing URI", // name but no uri_template
			}),
		}},
	})

	protoSvc := ptestutil.FindService(t, plugin, "BadService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	_, err := collectServiceData(protoSvc, gf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uri_template is required")
}

// TestResourceMutualExclusion verifies that a method with both mcp_tool and
// mcp_resource annotations produces an error.
func TestResourceMutualExclusion(t *testing.T) {
	// Build options with both extensions.
	opts := &descriptorpb.MethodOptions{}
	toolRaw := buildExtensionBytes(mcpv1.FieldMCPTool, map[protowire.Number]any{
		1: "my_tool",
	})
	resRaw := buildExtensionBytes(mcpv1.FieldMCPResource, map[protowire.Number]any{
		1: "res://thing",
	})
	opts.ProtoReflect().SetUnknown(append(toolRaw, resRaw...))

	plugin := makePlugin(t, ptestutil.Service{
		Name: "BadService",
		Methods: []ptestutil.Method{{
			Name:       "Ambiguous",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options:    opts,
		}},
	})

	protoSvc := ptestutil.FindService(t, plugin, "BadService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	_, err := collectServiceData(protoSvc, gf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot have both mcp_tool and mcp_resource")
}

// TestMixedToolAndResource verifies that a service with both tool methods
// and resource methods collects both correctly.
func TestMixedToolAndResource(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name: "MixedService",
		Methods: []ptestutil.Method{
			{
				Name:       "GetUser",
				InputType:  "test.v1.GetUserRequest",
				OutputType: "test.v1.GetUserResponse",
				// No annotation → default tool
			},
			{
				Name:       "GetUserProfile",
				InputType:  "test.v1.GetUserRequest",
				OutputType: "test.v1.GetUserResponse",
				Options: methodOptionsWithResource(map[protowire.Number]any{
					1: "user://{user_id}/profile",
					2: "User Profile",
				}),
			},
		},
	})

	protoSvc := ptestutil.FindService(t, plugin, "MixedService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, gf)
	require.NoError(t, err)

	assert.Len(t, sd.Tools, 1, "should have 1 tool")
	assert.Equal(t, "get_user", sd.Tools[0].ToolName)

	assert.Len(t, sd.Resources, 1, "should have 1 resource")
	assert.Equal(t, "user://{user_id}/profile", sd.Resources[0].URI)
	assert.True(t, sd.Resources[0].IsTemplate)
}

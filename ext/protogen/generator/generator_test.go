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
		case []string:
			for _, s := range v {
				inner = protowire.AppendTag(inner, num, protowire.BytesType)
				inner = protowire.AppendString(inner, s)
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
	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,svc.Name)
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
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

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"UserService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
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

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"UserService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
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

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"UserService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	_, err := collectServiceData(protoSvc, protoFile, gf)
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

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"UserService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	_, err := collectServiceData(protoSvc, protoFile, gf)
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
	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,svc.Name)
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
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

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"BadService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	_, err := collectServiceData(protoSvc, protoFile, gf)
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

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"BadService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	_, err := collectServiceData(protoSvc, protoFile, gf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot have multiple MCP annotations")
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

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"MixedService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
	require.NoError(t, err)

	assert.Len(t, sd.Tools, 1, "should have 1 tool")
	assert.Equal(t, "get_user", sd.Tools[0].ToolName)

	assert.Len(t, sd.Resources, 1, "should have 1 resource")
	assert.Equal(t, "user://{user_id}/profile", sd.Resources[0].URI)
	assert.True(t, sd.Resources[0].IsTemplate)
}

// --- Prompt annotation tests ---

// methodOptionsWithPrompt creates MethodOptions with an mcp_prompt extension.
func methodOptionsWithPrompt(fields map[protowire.Number]any) *descriptorpb.MethodOptions {
	opts := &descriptorpb.MethodOptions{}
	raw := buildExtensionBytes(mcpv1.FieldMCPPrompt, fields)
	opts.ProtoReflect().SetUnknown(raw)
	return opts
}

// TestPromptRegistration verifies that a method with mcp_prompt produces
// a promptData with arguments derived from the request message fields.
func TestPromptRegistration(t *testing.T) {
	plugin := ptestutil.CreatePlugin(t, &ptestutil.ProtoSet{
		Files: []ptestutil.File{{
			Name: "test.proto",
			Pkg:  "test.v1",
			Messages: []ptestutil.Message{
				{Name: "SummarizeRequest", Fields: []ptestutil.Field{
					{Name: "document_id", Number: 1, TypeName: "string"},
					{Name: "max_length", Number: 2, TypeName: "int32", Optional: true},
				}},
				{Name: "SummarizeResponse", Fields: []ptestutil.Field{
					{Name: "summary", Number: 1, TypeName: "string"},
				}},
			},
			Services: []ptestutil.Service{{
				Name: "DocService",
				Methods: []ptestutil.Method{{
					Name:       "Summarize",
					InputType:  "test.v1.SummarizeRequest",
					OutputType: "test.v1.SummarizeResponse",
					Options: methodOptionsWithPrompt(map[protowire.Number]any{
						1: "summarize_doc",       // name
						2: "Summarize a document", // description
					}),
				}},
			}},
		}},
	})

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"DocService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
	require.NoError(t, err)

	require.Len(t, sd.Prompts, 1)
	p := sd.Prompts[0]
	assert.Equal(t, "summarize_doc", p.PromptName)
	assert.Equal(t, "Summarize a document", p.Description)
	assert.Equal(t, "Summarize", p.MethodName)

	// Arguments derived from request message fields.
	require.Len(t, p.Arguments, 2)
	assert.Equal(t, "document_id", p.Arguments[0].Name)
	assert.True(t, p.Arguments[0].Required, "non-optional scalar should be required")
	assert.Equal(t, "max_length", p.Arguments[1].Name)
	assert.False(t, p.Arguments[1].Required, "optional field should not be required")
}

// TestPromptDefaultName verifies that prompt name defaults to snake_case
// of the method name when not specified in the annotation.
func TestPromptDefaultName(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name: "DocService",
		Methods: []ptestutil.Method{{
			Name:       "SummarizeDocument",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithPrompt(map[protowire.Number]any{
				2: "Summarize", // description only, no custom name
			}),
		}},
	})

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"DocService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
	require.NoError(t, err)

	require.Len(t, sd.Prompts, 1)
	assert.Equal(t, "summarize_document", sd.Prompts[0].PromptName)
}

// TestPromptWithNamespace verifies that mcp_service.namespace prefixes
// prompt names.
func TestPromptWithNamespace(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name:    "DocService",
		Options: serviceOptionsWithNamespace("docs"),
		Methods: []ptestutil.Method{{
			Name:       "Summarize",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithPrompt(map[protowire.Number]any{
				1: "summarize",
			}),
		}},
	})

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"DocService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
	require.NoError(t, err)

	require.Len(t, sd.Prompts, 1)
	assert.Equal(t, "docs_summarize", sd.Prompts[0].PromptName)
}

// --- Result summary tests ---

// TestResultSummaryAnnotation verifies that mcp_tool.result_summary is
// captured in toolData.
func TestResultSummaryAnnotation(t *testing.T) {
	tools := collectTools(t, ptestutil.Service{
		Name: "UserService",
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithTool(map[protowire.Number]any{
				4: true,                                  // structured_output
				5: "User {name} retrieved (id: {user_id})", // result_summary
			}),
		}},
	})

	require.Len(t, tools, 1)
	assert.True(t, tools[0].Structured)
	assert.Equal(t, "User {name} retrieved (id: {user_id})", tools[0].ResultSummary)
}

// --- Completion annotation tests ---

// TestCompletionFromPrompt verifies that completable_fields on a prompt
// annotation generates completion data with the correct ref type and fields.
func TestCompletionFromPrompt(t *testing.T) {
	plugin := ptestutil.CreatePlugin(t, &ptestutil.ProtoSet{
		Files: []ptestutil.File{{
			Name: "test.proto",
			Pkg:  "test.v1",
			Messages: []ptestutil.Message{
				{Name: "SummarizeRequest", Fields: []ptestutil.Field{
					{Name: "book_id", Number: 1, TypeName: "string"},
					{Name: "style", Number: 2, TypeName: "string"},
				}},
				{Name: "SummarizeResponse", Fields: []ptestutil.Field{
					{Name: "summary", Number: 1, TypeName: "string"},
				}},
			},
			Services: []ptestutil.Service{{
				Name: "BookService",
				Options: serviceOptionsWithNamespace("books"),
				Methods: []ptestutil.Method{{
					Name:       "SummarizeBook",
					InputType:  "test.v1.SummarizeRequest",
					OutputType: "test.v1.SummarizeResponse",
					Options: methodOptionsWithPrompt(map[protowire.Number]any{
						1: "summarize",                       // name
						3: []string{"book_id", "style"},      // completable_fields
					}),
				}},
			}},
		}},
	})

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"BookService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
	require.NoError(t, err)

	require.Len(t, sd.Completions, 1)
	c := sd.Completions[0]
	assert.Equal(t, "ref/prompt", c.RefType)
	assert.Equal(t, "books_summarize", c.RefName)
	require.Len(t, c.Fields, 2)
	assert.Equal(t, "book_id", c.Fields[0].Name)
	assert.Equal(t, "CompleteBookId", c.Fields[0].GoMethod)
	assert.Equal(t, "style", c.Fields[1].Name)
	assert.Equal(t, "CompleteStyle", c.Fields[1].GoMethod)
}

// TestCompletionFromResource verifies that completable_fields on a resource
// annotation generates completion data with ref/resource type.
func TestCompletionFromResource(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name: "BookService",
		Methods: []ptestutil.Method{{
			Name:       "GetBook",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithResource(map[protowire.Number]any{
				1: "book://{book_id}",           // uri_template
				5: []string{"book_id"},           // completable_fields
			}),
		}},
	})

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"BookService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
	require.NoError(t, err)

	require.Len(t, sd.Completions, 1)
	c := sd.Completions[0]
	assert.Equal(t, "ref/resource", c.RefType)
	assert.Equal(t, "book://{book_id}", c.RefName)
	require.Len(t, c.Fields, 1)
	assert.Equal(t, "book_id", c.Fields[0].Name)
	assert.Equal(t, "CompleteBookId", c.Fields[0].GoMethod)
}

// TestCompleterMethodsDeduplication verifies that CompleterMethods deduplicates
// methods when the same field appears in both a prompt and resource.
func TestCompleterMethodsDeduplication(t *testing.T) {
	sd := serviceData{
		Completions: []completionData{
			{
				RefType: "ref/prompt",
				RefName: "summarize",
				Fields:  []completionField{{Name: "book_id", GoMethod: "CompleteBookId"}},
			},
			{
				RefType: "ref/resource",
				RefName: "book://{book_id}",
				Fields:  []completionField{{Name: "book_id", GoMethod: "CompleteBookId"}},
			},
		},
	}

	methods := sd.CompleterMethods()
	require.Len(t, methods, 1, "should deduplicate across ref types")
	assert.Equal(t, "CompleteBookId", methods[0].GoMethod)
}

// TestNoCompletions verifies that services without completable_fields
// produce no completion data.
func TestNoCompletions(t *testing.T) {
	plugin := makePlugin(t, ptestutil.Service{
		Name: "SimpleService",
		Methods: []ptestutil.Method{{
			Name:       "GetUser",
			InputType:  "test.v1.GetUserRequest",
			OutputType: "test.v1.GetUserResponse",
			Options: methodOptionsWithPrompt(map[protowire.Number]any{
				1: "get_user",
				// no completable_fields
			}),
		}},
	})

	protoSvc, protoFile := ptestutil.FindServiceFile(t, plugin,"SimpleService")
	gf := plugin.NewGeneratedFile("_test.go", "")
	sd, err := collectServiceData(protoSvc, protoFile, gf)
	require.NoError(t, err)

	assert.Empty(t, sd.Completions)
}

// TestCompleteMethodName verifies the field-to-method name conversion.
func TestCompleteMethodName(t *testing.T) {
	tests := []struct{ field, want string }{
		{"book_id", "CompleteBookId"},
		{"genre", "CompleteGenre"},
		{"user_name", "CompleteUserName"},
		{"id", "CompleteId"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, completeMethodName(tt.field), "field=%q", tt.field)
	}
}

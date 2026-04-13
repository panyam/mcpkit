// Package testutil re-exports protokit/testutil types with MCP-specific finder helpers.
// All proto descriptor building is delegated to protokit.
package testutil

import (
	"testing"

	"github.com/panyam/protokit/testutil"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Re-export protokit types so callers don't need a double import.
type (
	ProtoSet  = testutil.TestProtoSet
	File      = testutil.TestFile
	Message   = testutil.TestMessage
	Field     = testutil.TestField
	Enum      = testutil.TestEnum
	EnumValue = testutil.TestEnumValue
	Service   = testutil.TestService
	Method    = testutil.TestMethod
)

// CreatePlugin creates a protogen.Plugin from a ProtoSet.
var CreatePlugin = testutil.CreateTestPlugin

// FindMessage finds a message descriptor by short name in a plugin's files.
func FindMessage(t *testing.T, plugin *protogen.Plugin, name string) protoreflect.MessageDescriptor {
	t.Helper()
	for _, f := range plugin.Files {
		for _, msg := range f.Messages {
			if string(msg.Desc.Name()) == name {
				return msg.Desc
			}
		}
	}
	t.Fatalf("message %q not found", name)
	return nil
}

// FindService finds a service by short name in a plugin's files.
func FindService(t *testing.T, plugin *protogen.Plugin, name string) *protogen.Service {
	t.Helper()
	for _, f := range plugin.Files {
		for _, svc := range f.Services {
			if string(svc.Desc.Name()) == name {
				return svc
			}
		}
	}
	t.Fatalf("service %q not found", name)
	return nil
}

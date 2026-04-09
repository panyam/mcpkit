package auth

import (
	"github.com/panyam/oneauth/client"
)

// Type aliases re-exported from oneauth/client for backward compatibility.
// The DCR types and RegisterClient function were moved to oneauth as part
// of mcpkit#158 (generic OAuth pushdown). MCP-specific defaults remain here.
type ClientRegistrationRequest = client.ClientRegistrationRequest
type ClientRegistrationResponse = client.ClientRegistrationResponse

// RegisterClient is re-exported from oneauth/client. Performs RFC 7591
// Dynamic Client Registration against the given endpoint.
var RegisterClient = client.RegisterClient

// DefaultClientRegistration returns a sensible default DCR request for an MCP client.
// This remains in mcpkit because the defaults are MCP-specific (client name,
// redirect URIs, grant types, auth method).
func DefaultClientRegistration() client.ClientRegistrationRequest {
	return client.ClientRegistrationRequest{
		ClientName:              "mcpkit-client",
		RedirectURIs:            []string{"http://127.0.0.1:0/callback"},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	}
}

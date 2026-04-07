package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ClientRegistrationRequest is the payload for RFC 7591 Dynamic Client Registration.
type ClientRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// ClientRegistrationResponse is the parsed response from a DCR endpoint.
type ClientRegistrationResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// RegisterClient performs RFC 7591 Dynamic Client Registration against the given
// endpoint. Returns the assigned client_id and optional client_secret.
//
// This is used as a fallback when no pre-registered client_id or CIMD URL is
// available. Per MCP spec (2025-11-25): clients MAY support DCR for backwards
// compatibility with earlier spec versions.
func RegisterClient(endpoint string, meta ClientRegistrationRequest, httpClient *http.Client) (*ClientRegistrationResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	body, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal DCR request: %w", err)
	}

	resp, err := httpClient.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("DCR POST: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read DCR response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DCR returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result ClientRegistrationResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse DCR response: %w (body: %s)", err, string(respBody))
	}
	if result.ClientID == "" {
		return nil, fmt.Errorf("DCR returned empty client_id: %s", string(respBody))
	}

	return &result, nil
}

// DefaultClientRegistration returns a sensible default DCR request for an MCP client.
func DefaultClientRegistration() ClientRegistrationRequest {
	return ClientRegistrationRequest{
		ClientName:              "mcpkit-client",
		RedirectURIs:            []string{"http://127.0.0.1:0/callback"},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	}
}

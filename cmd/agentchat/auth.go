package main

import (
	"fmt"
	"os"

	"github.com/panyam/mcpkit/client"
	extauth "github.com/panyam/mcpkit/ext/auth"
)

// authOption maps a server's AuthConfig onto the client option that carries
// it: a static bearer header, or a discovery-driven client-credentials token
// source (PRM to AS resolution, caching, and refresh live in ext/auth). Nil
// config means unauthenticated. Config validation has already checked env
// presence; this only assembles.
func authOption(sc ServerConfig) (client.ClientOption, error) {
	if sc.Auth == nil {
		return nil, nil
	}
	switch sc.Auth.Type {
	case "bearer":
		return client.WithClientBearerToken(os.Getenv(sc.Auth.TokenEnv)), nil
	case "client-credentials":
		return client.WithTokenSource(clientCredentialsSource(sc)), nil
	default:
		return nil, fmt.Errorf("agentchat: server %s: unsupported auth type %q", sc.ID, sc.Auth.Type)
	}
}

func clientCredentialsSource(sc ServerConfig) *extauth.ClientCredentialsTokenSource {
	return &extauth.ClientCredentialsTokenSource{
		ServerURL:     sc.URL,
		ClientID:      os.Getenv(sc.Auth.ClientIDEnv),
		ClientSecret:  os.Getenv(sc.Auth.ClientSecretEnv),
		Scopes:        sc.Auth.Scopes,
		AllowInsecure: sc.Auth.AllowInsecure,
	}
}

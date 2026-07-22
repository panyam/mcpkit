package host

import (
	"fmt"
	"os"

	"github.com/panyam/mcpkit/client"
	extauth "github.com/panyam/mcpkit/ext/auth"
)

// authOption maps a server's AuthConfig onto the client option that carries
// it: a static bearer header, a discovery-driven client-credentials token
// source, or an interactive authorization-code (oauth) token source (PRM to AS
// resolution, PKCE, caching, and refresh all live in ext/auth). Nil config
// means unauthenticated. For the oauth type it also returns the token source as
// a loginSource so the host can force a fresh login later; the other types
// return a nil loginSource. Config validation has already checked env presence;
// this only assembles.
func authOption(sc ServerConfig) (client.ClientOption, loginSource, error) {
	if sc.Auth == nil {
		return nil, nil, nil
	}
	switch sc.Auth.Type {
	case "bearer":
		return client.WithClientBearerToken(os.Getenv(sc.Auth.TokenEnv)), nil, nil
	case "client-credentials":
		return client.WithTokenSource(clientCredentialsSource(sc)), nil, nil
	case "oauth":
		src := oauthSource(sc)
		return client.WithTokenSource(src), src, nil
	default:
		return nil, nil, fmt.Errorf("agentchat: server %s: unsupported auth type %q", sc.ID, sc.Auth.Type)
	}
}

// oauthSource builds the interactive authorization-code token source: it
// self-registers via DCR when no client is pre-registered, else pins the
// client from clientIdEnv (+ optional clientSecretEnv). Scopes stay empty by
// default so acquisition follows the server's 401 WWW-Authenticate challenge.
func oauthSource(sc ServerConfig) *extauth.OAuthTokenSource {
	s := &extauth.OAuthTokenSource{
		ServerURL:     sc.URL,
		Scopes:        sc.Auth.Scopes,
		AllowInsecure: sc.Auth.AllowInsecure,
	}
	if sc.Auth.ClientIDEnv != "" {
		s.ClientID = os.Getenv(sc.Auth.ClientIDEnv)
		s.ClientSecret = os.Getenv(sc.Auth.ClientSecretEnv)
	} else {
		s.EnableDCR = true
	}
	return s
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

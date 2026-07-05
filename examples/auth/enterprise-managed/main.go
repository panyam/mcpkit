// Example: Enterprise-Managed Authorization (EMA / SEP-990).
//
// Demonstrates the SEP-990 two-stage token chain end-to-end, all
// in-process. The MCP client presents an upstream IdP-issued id_token and
// walks it through two token endpoints to reach an MCP access token:
//
//	IdP  /token  (RFC 8693 token-exchange)  id_token -> id-jag
//	AS   /token  (RFC 7523 jwt-bearer)       id-jag   -> MCP access token
//
// Then it calls an MCP tool (whoami) with that access token.
//
// The client side is driven by ext/auth.EnterpriseManagedTokenSource. Both
// token endpoints run oneauth's real token endpoint: the IdP mints the
// ID-JAG via oneauth's ID-JAG issuer (apiauth.NewJWTIDJAGIssuer, wired into
// the token-exchange grant per oneauth#350), and the AS redeems it via
// oneauth's jwt-bearer granter. Nothing in this example is a stub.
//
// This example is self-driving: it stands up the IdP, the AS, and the MCP
// server in-process, runs the chain, prints each stage, and exits.
//
// Run:
//
//	go run ./enterprise-managed
//	go run ./enterprise-managed -addr :8091
package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	mcpcommon "github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/oneauth/admin"
	"github.com/panyam/oneauth/apiauth"
	oacore "github.com/panyam/oneauth/core"
	"github.com/panyam/oneauth/keys"
	"github.com/panyam/oneauth/utils"
)

// demoUser is the subject the IdP authenticates. It flows through the id
// token, the ID-JAG, and finally the MCP access token's sub claim, which
// the whoami tool reads back.
const demoUser = "alice@corp.example"

func main() {
	addr := flag.String("addr", ":8090", "MCP server listen address")
	flag.Parse()
	if err := run(*addr); err != nil {
		log.Fatalf("enterprise-managed: %v", err)
	}
}

func run(addr string) error {
	// 1. Stand up the IdP-role AS (mints id tokens + RFC 8693 token-exchange
	//    into an ID-JAG via oneauth's ID-JAG issuer).
	idp, err := startIdP()
	if err != nil {
		return fmt.Errorf("start IdP: %w", err)
	}
	defer idp.Close()

	// 2. Stand up the RS-role MCP authorization server. It trusts the IdP
	//    as a jwt-bearer assertion issuer and redeems the ID-JAG.
	as, err := startRSAS(idp.URL, &idp.priv.PublicKey)
	if err != nil {
		return fmt.Errorf("start AS: %w", err)
	}
	defer as.Close()

	// 3. Register the MCP client at the AS (RFC 7591 DCR) as a confidential
	//    client. SEP-990 has the client authenticate at the AS on the
	//    jwt-bearer redemption, and oneauth's granter enforces it: the AppStore
	//    wired into the RS-AS (see startRSAS) lets the granter recognize this
	//    client_id as confidential and require its credential, binding
	//    redemption to the client the ID-JAG names (oneauth#356). The client
	//    authenticates via client_secret_basic (the SEP-990 default); oneauth
	//    v0.1.35 honors the Authorization: Basic header on this path
	//    (oneauth#362).
	clientID, clientSecret, err := dcrRegister(as.URL)
	if err != nil {
		return fmt.Errorf("DCR at AS: %w", err)
	}

	// 4. Mint the demo id_token at the IdP for the demo user. In a real
	//    deployment the client already holds this from an OIDC login. Its aud
	//    is the IdP issuer so the IdP's token-exchange grant accepts it as a
	//    trusted-issuer assertion (see the IdP TrustedAssertionIssuer below).
	idToken, err := mintIDToken(idp.priv, idp.URL, demoUser, idp.URL)
	if err != nil {
		return fmt.Errorf("mint id_token: %w", err)
	}

	// 5. Start the MCP server (validator pointed at the AS) and wait for it.
	listenURL := fmt.Sprintf("http://localhost%s", addr)
	stopMCP, err := startMCPServer(addr, listenURL, as.URL, as.JWKSURL)
	if err != nil {
		return fmt.Errorf("start MCP server: %w", err)
	}
	defer stopMCP()
	if err := waitReady(listenURL + "/.well-known/oauth-protected-resource/mcp"); err != nil {
		return fmt.Errorf("MCP server not ready: %w", err)
	}

	fmt.Println("=== Enterprise-Managed Authorization (SEP-990) ===")
	fmt.Printf("IdP token endpoint: %s\n", idp.TokenEndpoint)
	fmt.Printf("MCP AS issuer:      %s\n", as.URL)
	fmt.Printf("MCP server:         %s/mcp\n", listenURL)
	fmt.Printf("Client:             %s\n\n", clientID)

	// Stage 1: the client holds the IdP id_token.
	fmt.Printf("[1] id_token acquired (sub=%s)\n    %s\n\n", demoUser, elide(idToken))

	// 6. Build the EnterpriseManagedTokenSource and run the full chain.
	src := &auth.EnterpriseManagedTokenSource{
		ServerURL:        listenURL + "/mcp",
		ClientID:         clientID,
		ClientSecret:     clientSecret,
		IdpClientID:      clientID,
		IdpIDToken:       idToken,
		IdpTokenEndpoint: idp.TokenEndpoint,
		AllowInsecure:    true, // in-process endpoints are http://
	}

	accessToken, err := src.Token()
	if err != nil {
		return fmt.Errorf("token chain: %w", err)
	}

	// Stage 2: the ID-JAG the IdP minted during the chain (captured for
	//          demo visibility; the token source treats it as opaque).
	if idjag := lastIDJAG(); idjag != "" {
		fmt.Printf("[2] id-jag minted at IdP (typ=%s)\n    %s\n", apiauth.IDJAGTypeHeader, elide(idjag))
		if sub, aud := jagClaims(idjag); sub != "" {
			fmt.Printf("    sub=%s  aud=%s\n", sub, aud)
		}
		fmt.Println()
	}

	// Stage 3: the MCP access token the AS issued from the ID-JAG.
	fmt.Printf("[3] MCP access token issued by AS\n    %s\n", elide(accessToken))
	if sub := tokenSubject(accessToken); sub != "" {
		fmt.Printf("    sub=%s\n", sub)
	}
	fmt.Println()

	// 7. Call the MCP tool with the access token via a real mcpkit client.
	//    Wiring the source directly (WithTokenSource) reuses the cached
	//    token; the chain does not re-run.
	mcpClient := client.NewClient(listenURL+"/mcp",
		core.ClientInfo{Name: "enterprise-managed-host", Version: "1.0"},
		client.WithTokenSource(src),
	)
	if err := mcpClient.Connect(); err != nil {
		return fmt.Errorf("connect MCP client: %w", err)
	}
	defer mcpClient.Close()

	result, err := mcpClient.ToolCall("whoami", map[string]any{})
	if err != nil {
		return fmt.Errorf("whoami: %w", err)
	}
	fmt.Printf("[4] tools/call whoami -> %s\n", result)
	fmt.Println("\nDone. The EMA chain completed end-to-end.")
	return nil
}

// --- IdP-role authorization server ---

type idpEnv struct {
	URL           string
	TokenEndpoint string
	priv          *rsa.PrivateKey
	ts            *httptest.Server
}

func (e *idpEnv) Close() { e.ts.Close() }

// startIdP stands up the IdP-role server: JWKS + metadata + oneauth's real
// token endpoint. The token-exchange grant is opted into ID-JAG issuance via
// apiauth.NewJWTIDJAGIssuer (oneauth#350). The IdP trusts its own id_token
// issuer so the self-minted id_token validates as the stage-1 subject_token,
// then oneauth binds the ID-JAG's aud from the request audience and mints it
// with the IdP key.
func startIdP() (*idpEnv, error) {
	priv, pub, err := newRSAKey()
	if err != nil {
		return nil, err
	}

	ks := keys.NewInMemoryKeyStore()
	if _, err := ks.PutKey(context.Background(), &keys.PutKeyRequest{
		Record: &keys.KeyRecord{ClientID: "_idp_signer", Key: pub, Algorithm: "RS256"},
	}); err != nil {
		return nil, fmt.Errorf("register IdP key: %w", err)
	}

	env := &idpEnv{priv: priv}
	mux := http.NewServeMux()
	mux.Handle("GET /.well-known/jwks.json", &keys.JWKSHandler{KeyStore: ks})

	var metaHandler http.Handler
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if metaHandler == nil {
			http.Error(w, "IdP not ready", http.StatusServiceUnavailable)
			return
		}
		metaHandler.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(mux)
	env.ts = ts
	env.URL = ts.URL
	env.TokenEndpoint = ts.URL + "/token"

	// Real oneauth AS with ID-JAG issuance enabled. TrustedAssertionIssuers
	// trusts the IdP's own id_token issuer (this AS), so the self-minted
	// id_token passes ValidateAssertion as the subject_token; its aud must
	// match the trusted-issuer audience (the IdP issuer URL). IDJAGIssuer
	// signs the ID-JAG with the IdP key, iss=this AS.
	oa := apiauth.NewOneAuth(apiauth.OneAuthConfig{
		KeyStore:   ks,
		SigningKey: priv,
		VerifyKey:  &priv.PublicKey,
		SigningAlg: "RS256",
		Issuer:     ts.URL,
		IDJAGIssuer: apiauth.NewJWTIDJAGIssuer(apiauth.IDJAGIssuerConfig{
			SigningKey: priv,
			SigningAlg: "RS256",
			Issuer:     ts.URL,
			TTL:        2 * time.Minute,
		}),
		TrustedAssertionIssuers: []apiauth.TrustedAssertionIssuer{{
			Issuer:             ts.URL,
			PublicKey:          &priv.PublicKey,
			Audiences:          []string{ts.URL},
			AcceptedAlgorithms: []string{"RS256"},
		}},
	})
	// The tee captures the minted ID-JAG for stage-2 display only; the
	// response passes through unchanged. The token source treats the ID-JAG
	// as opaque, so this demo observability hook is the only way to show it.
	mux.Handle("POST /token", captureIDJAG(apiauth.NewTokenEndpointHandler(oa)))

	metaHandler = apiauth.NewASMetadataHandler(&apiauth.ASServerMetadata{
		Issuer:              ts.URL,
		TokenEndpoint:       ts.URL + "/token",
		JWKSURI:             ts.URL + "/.well-known/jwks.json",
		GrantTypesSupported: []string{apiauth.TokenExchangeGrantType},
	})
	return env, nil
}

// --- RS-role MCP authorization server ---

type rsasEnv struct {
	URL     string
	JWKSURL string
	ts      *httptest.Server
}

func (e *rsasEnv) Close() { e.ts.Close() }

// startRSAS stands up the MCP authorization server. It runs oneauth's real
// token endpoint with the jwt-bearer granter enabled (TrustedAssertionIssuers
// trusts the IdP's key), so redeeming the ID-JAG for an access token uses
// the production code path. It also mounts the DCR registrar so the client
// can register.
func startRSAS(idpIssuer string, idpPub *rsa.PublicKey) (*rsasEnv, error) {
	privPEM, pubPEM, err := utils.GenerateRSAKeyPair(2048)
	if err != nil {
		return nil, fmt.Errorf("generate AS keypair: %w", err)
	}
	parsed, err := utils.ParsePrivateKeyPEM(privPEM)
	if err != nil {
		return nil, fmt.Errorf("parse AS key: %w", err)
	}
	privKey := parsed.(*rsa.PrivateKey)

	ks := keys.NewInMemoryKeyStore()
	if _, err := ks.PutKey(context.Background(), &keys.PutKeyRequest{
		Record: &keys.KeyRecord{ClientID: "_as_signer", Key: pubPEM, Algorithm: "RS256"},
	}); err != nil {
		return nil, fmt.Errorf("register AS key: %w", err)
	}

	// Shared app-registration store: the DCR registrar writes the confidential
	// client into it, and OneAuthConfig.AppStore reads from it so the
	// jwt-bearer granter enforces client auth on ID-JAG redemption. Without a
	// shared AppStore the DCR'd client is invisible to the granter, which then
	// treats it as public and skips auth (oneauth#356).
	appStore := oacore.NewInMemoryAppStore()
	registrar := admin.NewAppRegistrarWithStore(ks, admin.NewNoAuth(), appStore)

	env := &rsasEnv{}
	mux := http.NewServeMux()
	mux.Handle("/apps/", registrar.Handler())
	mux.Handle("GET /.well-known/jwks.json", &keys.JWKSHandler{KeyStore: ks})

	var metaHandler http.Handler
	metaFunc := func(w http.ResponseWriter, r *http.Request) {
		if metaHandler == nil {
			http.Error(w, "AS not ready", http.StatusServiceUnavailable)
			return
		}
		metaHandler.ServeHTTP(w, r)
	}
	// Serve both well-known paths DiscoverAS probes (RFC 8414 first, OIDC fallback).
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", metaFunc)
	mux.HandleFunc("GET /.well-known/openid-configuration", metaFunc)

	ts := httptest.NewServer(mux)
	env.ts = ts
	env.URL = ts.URL
	env.JWKSURL = ts.URL + "/.well-known/jwks.json"

	// Trust the IdP for the jwt-bearer grant. The ID-JAG's iss must equal
	// idpIssuer, its signature must verify against the IdP public key, and
	// its aud must be this AS's issuer (RFC 7523 §3).
	oa := apiauth.NewOneAuth(apiauth.OneAuthConfig{
		KeyStore:   ks,
		SigningKey: privKey,
		VerifyKey:  &privKey.PublicKey,
		SigningAlg: "RS256",
		Issuer:     ts.URL,
		AppStore:   appStore, // enables confidential-client auth on redemption (oneauth#356)
		TrustedAssertionIssuers: []apiauth.TrustedAssertionIssuer{{
			Issuer:             idpIssuer,
			PublicKey:          idpPub,
			Audiences:          []string{ts.URL},
			AcceptedAlgorithms: []string{"RS256"},
		}},
	})
	mux.Handle("POST /token", apiauth.NewTokenEndpointHandler(oa))

	metaHandler = apiauth.NewASMetadataHandler(&apiauth.ASServerMetadata{
		Issuer:                   ts.URL,
		TokenEndpoint:            ts.URL + "/token",
		JWKSURI:                  ts.URL + "/.well-known/jwks.json",
		GrantTypesSupported:      []string{apiauth.JwtBearerGrantType},
		TokenEndpointAuthMethods: []string{"client_secret_basic"},
	})
	return env, nil
}

// dcrRegister performs RFC 7591 Dynamic Client Registration at the AS and
// returns the issued client_id + client_secret.
func dcrRegister(asURL string) (string, string, error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":                "enterprise-managed-demo",
		"grant_types":                []string{apiauth.JwtBearerGrantType},
		"token_endpoint_auth_method": "client_secret_basic",
	})
	resp, err := http.Post(asURL+"/apps/dcr", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("DCR returned %d: %s", resp.StatusCode, b)
	}
	var reg struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		return "", "", err
	}
	return reg.ClientID, reg.ClientSecret, nil
}

// --- MCP server ---

// startMCPServer starts the mcpkit server in a goroutine and returns a stop
// func. The JWT validator points at the AS's JWKS + issuer; the whoami tool
// echoes the authenticated subject.
func startMCPServer(addr, listenURL, asIssuer, asJWKS string) (func(), error) {
	prmURL := listenURL + "/.well-known/oauth-protected-resource/mcp"
	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:             asJWKS,
		Issuer:              asIssuer,
		Audience:            "", // AS mints access tokens without an aud in this demo.
		ResourceMetadataURL: prmURL,
	})
	validator.Start()

	errc := make(chan error, 1)
	go func() {
		errc <- mcpcommon.RunServer(mcpcommon.ServerConfig{
			Name:    "enterprise-managed",
			Version: "1.0.0",
			Addr:    addr,
			Options: []server.Option{
				server.WithAuth(validator),
			},
			Register: func(srv *server.Server) {
				srv.Register(core.TextTool[struct{}]("whoami",
					"Returns the authenticated subject from the MCP access token.",
					func(ctx core.ToolContext, _ struct{}) (string, error) {
						claims := ctx.AuthClaims()
						if claims == nil {
							return "unauthenticated", nil
						}
						return "authenticated subject: " + claims.Subject, nil
					},
				))
			},
			TransportOptions: []server.TransportOption{
				server.WithMux(func(mux *http.ServeMux) {
					auth.MountAuth(mux, auth.AuthConfig{
						ResourceURI:          listenURL,
						AuthorizationServers: []string{asIssuer},
						MCPPath:              "/mcp",
					})
				}),
			},
		})
	}()

	stop := func() {
		validator.Stop()
		select {
		case err := <-errc:
			if err != nil {
				log.Printf("MCP server exited: %v", err)
			}
		default:
		}
	}
	return stop, nil
}

// waitReady polls url until it returns 200 or the deadline passes.
func waitReady(url string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", url)
}

// --- helpers ---

// idjagRecorder captures the last ID-JAG the IdP minted so run() can print
// it. This is a demo observability hook, not part of the token flow; the
// token source treats the ID-JAG as opaque.
var idjagRecorder struct {
	sync.Mutex
	value string
}

func recordIDJAG(v string) {
	idjagRecorder.Lock()
	idjagRecorder.value = v
	idjagRecorder.Unlock()
}

func lastIDJAG() string {
	idjagRecorder.Lock()
	defer idjagRecorder.Unlock()
	return idjagRecorder.value
}

func newRSAKey() (*rsa.PrivateKey, *rsa.PublicKey, error) {
	privPEM, _, err := utils.GenerateRSAKeyPair(2048)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := utils.ParsePrivateKeyPEM(privPEM)
	if err != nil {
		return nil, nil, err
	}
	priv := parsed.(*rsa.PrivateKey)
	return priv, &priv.PublicKey, nil
}

// jagClaims returns the sub + aud of an ID-JAG without verifying it (the
// AS verifies; this is display only).
func jagClaims(idjag string) (sub, aud string) {
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(idjag, claims)
	if err != nil {
		return "", ""
	}
	sub, _ = claims["sub"].(string)
	aud, _ = claims["aud"].(string)
	return sub, aud
}

// tokenSubject returns the sub claim of a JWT without verifying it.
func tokenSubject(token string) string {
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(token, claims)
	if err != nil {
		return ""
	}
	sub, _ := claims["sub"].(string)
	return sub
}

// elide shortens a token for display.
func elide(tok string) string {
	if len(tok) <= 48 {
		return tok
	}
	return tok[:32] + "..." + tok[len(tok)-12:]
}

// captureIDJAG wraps oneauth's token endpoint and records the ID-JAG from a
// successful token-exchange response so run() can print stage 2. It is a
// pass-through: it buffers the response, tees the ID-JAG when
// issued_token_type is id-jag, then writes the original bytes unchanged.
// Demo-only observability — it does not participate in the token flow.
func captureIDJAG(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		if rec.Code == http.StatusOK {
			var body struct {
				AccessToken     string `json:"access_token"`
				IssuedTokenType string `json:"issued_token_type"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &body) == nil &&
				body.IssuedTokenType == apiauth.TokenTypeIDJAG {
				recordIDJAG(body.AccessToken)
			}
		}

		for k, vs := range rec.Header() {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

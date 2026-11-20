package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

func cardServer(t *testing.T) (mcpURL string, revalidations *atomic.Int32) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "card-test", Version: "1.2.3"},
		server.WithServerCard(server.WithCardName("io.github.example/card-test"), server.WithCardDescription("test")))
	srv.SetCardExtensionInfo("io.example/ext", func(string) map[string]any { return map[string]any{"k": "v"} })
	h := srv.Handler(server.WithStreamableHTTP(true))
	revalidations = &atomic.Int32{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			revalidations.Add(1)
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp", revalidations
}

func TestFetchServerCard_ReadsTheServersCard(t *testing.T) {
	mcpURL, _ := cardServer(t)
	card, err := client.FetchServerCard(context.Background(), mcpURL, client.WithInsecureCardFetch())
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "io.github.example/card-test" || card.Version != "1.2.3" {
		t.Fatalf("card = %s %s", card.Name, card.Version)
	}
	remote, ok := client.CardRemote(card, mcpURL+"/")
	if !ok {
		t.Fatalf("no remote for %s in %+v", mcpURL, card.Remotes)
	}
	if remote.ExtensionsInfo["io.example/ext"]["k"] != "v" {
		t.Fatalf("extensionsInfo = %v", remote.ExtensionsInfo)
	}
	if _, ok := client.CardRemote(card, "http://elsewhere/mcp"); ok {
		t.Fatal("CardRemote matched a URL the card does not list")
	}
}

func TestServerCardCache_RevalidatesWithETag(t *testing.T) {
	mcpURL, revalidations := cardServer(t)
	cache := client.NewServerCardCache(client.WithInsecureCardFetch())
	first, err := cache.Fetch(context.Background(), mcpURL)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.Fetch(context.Background(), mcpURL)
	if err != nil {
		t.Fatal(err)
	}
	if revalidations.Load() != 1 {
		t.Fatalf("second fetch sent If-None-Match %d times, want 1", revalidations.Load())
	}
	if first != second {
		t.Fatal("a 304 must return the cached card")
	}
}

func TestFetchServerCard_RefusesPlainHTTPByDefault(t *testing.T) {
	mcpURL, _ := cardServer(t)
	_, err := client.FetchServerCard(context.Background(), mcpURL)
	if !errors.Is(err, client.ErrCardInsecure) {
		t.Fatalf("err = %v, want ErrCardInsecure", err)
	}
}

func TestFetchServerCard_RefusesCrossOriginRedirect(t *testing.T) {
	elsewhereURL, _ := cardServer(t)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhereURL+"/server-card", http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	_, err := client.FetchServerCard(context.Background(), redirector.URL+"/mcp", client.WithInsecureCardFetch())
	if !errors.Is(err, client.ErrCardCrossOrigin) {
		t.Fatalf("err = %v, want ErrCardCrossOrigin (a different port is a different origin)", err)
	}
}

func TestFetchServerCard_FollowsSameOriginRedirect(t *testing.T) {
	mcpURL, _ := cardServer(t)
	mux := http.NewServeMux()
	var target string
	mux.HandleFunc("/old/server-card", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
	mux.HandleFunc("/mcp/server-card", func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(mcpURL + "/server-card")
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		io.Copy(w, resp.Body)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	target = ts.URL + "/mcp/server-card"

	card, err := client.FetchServerCard(context.Background(), ts.URL+"/old", client.WithInsecureCardFetch())
	if err != nil {
		t.Fatalf("same-origin redirect must be followed: %v", err)
	}
	if card.Name != "io.github.example/card-test" {
		t.Fatalf("card.Name = %q", card.Name)
	}
}

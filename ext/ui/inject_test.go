package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppBridgeScript_NotEmpty(t *testing.T) {
	if len(AppBridgeScript) == 0 {
		t.Fatal("AppBridgeScript is empty — go:embed may have failed")
	}
	if !strings.Contains(AppBridgeScript, "MCPApp") {
		t.Fatal("AppBridgeScript does not contain 'MCPApp' — wrong file embedded?")
	}
}

func TestInjectAppBridge_InsertsBeforeBody(t *testing.T) {
	html := `<html><body><p>Hello</p></body></html>`
	got := InjectAppBridge(html)
	if !strings.Contains(got, bridgeSentinel) {
		t.Fatal("sentinel not found in output")
	}
	bodyIdx := strings.Index(strings.ToLower(got), "</body>")
	scriptIdx := strings.Index(got, bridgeSentinel)
	if scriptIdx > bodyIdx {
		t.Errorf("script (%d) appears after </body> (%d)", scriptIdx, bodyIdx)
	}
}

func TestInjectAppBridge_CaseInsensitive(t *testing.T) {
	html := `<html><BODY><p>Hi</p></BODY></html>`
	got := InjectAppBridge(html)
	if !strings.Contains(got, bridgeSentinel) {
		t.Fatal("sentinel not found — case-insensitive match failed")
	}
}

func TestInjectAppBridge_NoBody(t *testing.T) {
	html := `<div>fragment</div>`
	got := InjectAppBridge(html)
	if !strings.HasSuffix(got, "\n</script>\n") {
		t.Fatal("expected script appended at end when no </body>")
	}
}

func TestInjectAppBridge_Idempotent(t *testing.T) {
	html := `<html><body></body></html>`
	first := InjectAppBridge(html)
	second := InjectAppBridge(first)
	if first != second {
		t.Fatal("InjectAppBridge is not idempotent — double injection detected")
	}
}

func TestAppShellHTML_Structure(t *testing.T) {
	got := AppShellHTML("Test App", `<div id="app">hello</div>`)

	checks := []string{
		"<!DOCTYPE html>",
		"<title>Test App</title>",
		`<div id="app">hello</div>`,
		bridgeSentinel,
		"MCPApp",
		"</body>",
		"</html>",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("AppShellHTML missing %q", want)
		}
	}
}

func TestAppShellHTML_Idempotent(t *testing.T) {
	shell := AppShellHTML("Test", "<p>hi</p>")
	reinjected := InjectAppBridge(shell)
	if shell != reinjected {
		t.Fatal("InjectAppBridge on AppShellHTML output should be a no-op")
	}
}

func TestServeBridge_ContentType(t *testing.T) {
	h := ServeBridge()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, BridgePath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("expected application/javascript, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "MCPApp") {
		t.Error("response body missing MCPApp")
	}
}

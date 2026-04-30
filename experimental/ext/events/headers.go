package events

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WebhookHeaderMode selects the header / signature wire format for outbound
// webhook deliveries. Defaults to StandardWebhooks per upstream WG PR#1
// line 434 (comment r3167245184) — author signaled alignment on Standard
// Webhooks naming. MCPHeaders is retained as an opt-in mode for clients
// that already standardized on the X-MCP-* shape.
type WebhookHeaderMode int

const (
	// StandardWebhooks (default) emits webhook-id, webhook-timestamp, and
	// webhook-signature ("v1,<base64>") per https://standardwebhooks.com/.
	// HMAC base is webhook_id + "." + webhook_timestamp + "." + body.
	// Note: only the headers/signature scheme is adopted — the Standard
	// Webhooks payload envelope is intentionally out of scope here.
	StandardWebhooks WebhookHeaderMode = iota

	// MCPHeaders emits X-MCP-Signature and X-MCP-Timestamp with the
	// signature computed as HMAC(secret, ts + "." + body). Was the
	// pre-r3167245184 default; kept as an opt-in for callers that
	// already wired against this shape.
	MCPHeaders
)

// String renders the mode as a config-flag-friendly token.
func (m WebhookHeaderMode) String() string {
	switch m {
	case MCPHeaders:
		return "mcp"
	default:
		return "standard"
	}
}

// ParseHeaderMode converts a flag-style token ("mcp" or "standard") to a
// WebhookHeaderMode. Empty string returns the default (StandardWebhooks).
func ParseHeaderMode(s string) (WebhookHeaderMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "standard", "standardwebhooks", "standard-webhooks":
		return StandardWebhooks, nil
	case "mcp":
		return MCPHeaders, nil
	default:
		return StandardWebhooks, fmt.Errorf("unknown header mode %q (want standard|mcp)", s)
	}
}

// signedDelivery holds the headers and the request body for one webhook POST.
// applyHeaders writes them onto an outbound *http.Request.
type signedDelivery struct {
	headers map[string]string
	body    []byte
}

func (d signedDelivery) applyHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	for k, v := range d.headers {
		req.Header.Set(k, v)
	}
}

// signMCP produces the today's-default signed delivery.
//   X-MCP-Signature:  sha256=<hex(HMAC(secret, ts + "." + body))>
//   X-MCP-Timestamp:  <unix>
func signMCP(body []byte, secret string, now time.Time) signedDelivery {
	ts := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return signedDelivery{
		headers: map[string]string{
			"X-MCP-Signature": sig,
			"X-MCP-Timestamp": ts,
		},
		body: body,
	}
}

// signStandardWebhooks produces a Standard Webhooks v1 signed delivery.
//   webhook-id:        <random message id>
//   webhook-timestamp: <unix>
//   webhook-signature: v1,<base64(HMAC(secret, msgId + "." + ts + "." + body))>
func signStandardWebhooks(body []byte, secret string, now time.Time) signedDelivery {
	msgID := newMessageID()
	ts := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msgID))
	mac.Write([]byte("."))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return signedDelivery{
		headers: map[string]string{
			"webhook-id":        msgID,
			"webhook-timestamp": ts,
			"webhook-signature": sig,
		},
		body: body,
	}
}

// signFor selects the right signer for the registry's mode.
func signFor(mode WebhookHeaderMode, body []byte, secret string, now time.Time) signedDelivery {
	switch mode {
	case MCPHeaders:
		return signMCP(body, secret, now)
	default:
		return signStandardWebhooks(body, secret, now)
	}
}

// newMessageID returns a Standard-Webhooks-shaped message ID. Uses the
// "msg_" prefix Stripe / Standard Webhooks examples use, with 16 random
// bytes as URL-safe base64. Independent of the event's own EventID.
func newMessageID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Should never happen on supported platforms; degrade with a
		// timestamp-based fallback rather than panicking the delivery loop.
		return "msg_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "msg_" + base64.RawURLEncoding.EncodeToString(buf[:])
}

// VerifyMCPSignature checks an X-MCP-Signature header against body+timestamp.
// Equivalent to (and kept as) the existing VerifySignature helper.
func VerifyMCPSignature(body []byte, secret, timestamp, signature string) bool {
	expected := signMCP(body, secret, time.Unix(parseUnix(timestamp), 0)).headers["X-MCP-Signature"]
	return hmac.Equal([]byte(expected), []byte(signature))
}

// VerifyStandardWebhooksSignature checks a webhook-signature header. Standard
// Webhooks allows multiple space-separated versioned signatures
// (e.g. "v1,abc v1,def"); we accept any matching v1.
func VerifyStandardWebhooksSignature(body []byte, secret, msgID, timestamp, signature string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msgID))
	mac.Write([]byte("."))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, candidate := range strings.Fields(signature) {
		if hmac.Equal([]byte(candidate), []byte(expected)) {
			return true
		}
	}
	return false
}

func parseUnix(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

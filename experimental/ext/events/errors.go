package events

// JSON-RPC error codes for the MCP Events extension.
//
// The spec reserves the range -32011..-32017 for events errors
// (-32014 is intentionally unused). Codes outside this range MUST
// NOT be added here — events handlers should either use one of the
// spec codes below, fall back to a base JSON-RPC code (-32602
// InvalidParams, -32603 InternalError, -32601 MethodNotFound), or
// surface the issue through the body shape rather than a new code.
//
// Spec-defined codes (full set):
//   -32011 EventNotFound           — Unknown event name
//   -32012 Unauthorized            — Caller lacks permission for this event/params
//   -32013 TooManySubscriptions    — Server-imposed subscription limit reached
//   -32014 (reserved by the spec)
//   -32015 InvalidCallbackUrl      — Webhook URL unreachable / rejected
//   -32016 SubscriptionNotFound    — Unsubscribe target doesn't exist
//   -32017 DeliveryModeUnsupported — Event type doesn't support requested mode
//
// Only codes the events handlers currently emit are declared below.
// Subsequent spec-alignment PRs (ζ for delivery status / unsubscribe
// lookup, etc.) add the remaining constants in the commit that
// introduces the behavior using them — keeps each new code visible
// alongside its first use.
const (
	ErrCodeEventNotFound           = -32011 // Unknown event name
	ErrCodeUnauthorized            = -32012 // Caller lacks permission for this event/params
	ErrCodeInvalidCallbackUrl      = -32015 // Webhook URL unreachable / rejected
	ErrCodeDeliveryModeUnsupported = -32017 // Event type doesn't support requested mode
)

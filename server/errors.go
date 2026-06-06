package server

import "errors"

// Sentinels for server-package errors returned to callers and matched
// with errors.Is.

// ErrStatelessStreamCapExceeded indicates the requesting scope has hit
// the per-scope concurrent-stream cap configured via
// [WithStatelessSubscriptionCap]. Returned by the stateless subscription
// registry's tryRegister so handleStatelessSubscribe can map it to the
// wire error.
var ErrStatelessStreamCapExceeded = errors.New("stateless subscription stream cap exceeded")

// ErrStatelessStreamRateLimited indicates the requesting scope has
// exceeded the per-scope open-rate configured via
// [WithStatelessSubscriptionRateLimit].
var ErrStatelessStreamRateLimited = errors.New("stateless subscription stream rate limited")

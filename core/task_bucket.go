package core

import "context"

// TaskBucketKeyer derives the per-request bucket key that the task store
// isolates against — the value passed as the store's sessionID argument for
// Create / Get / Update / Cancel / List.
//
// The default (no keyer installed) is the transport session ID: on the legacy
// wire that is the per-connection session; on the SEP-2575 stateless wire it is
// "" (no session), which is why, by default, all stateless tasks share one
// bucket per process. Multi-tenant stateless deployments install a keyer that
// derives the bucket from an authenticated subject (JWT `sub`), a request
// header, or any other per-tenant signal, so tenants cannot read / cancel /
// update each other's tasks.
//
// The keyer takes a raw context.Context so it stays decoupled from ext/auth:
// an auth-based keyer reads the subject from the auth claims already on the
// context, but the core/server/ext-tasks packages never import ext/auth. Wire
// it with server.WithTaskBucketKeyer.
type TaskBucketKeyer func(ctx context.Context) string

type taskBucketKeyerCtxKey struct{}

// WithTaskBucketKeyer installs a TaskBucketKeyer on the context so downstream
// task create/get/update/cancel sites resolve the bucket through it. A nil
// keyer is a no-op (the context is returned unchanged), so callers can inject
// unconditionally.
func WithTaskBucketKeyer(ctx context.Context, keyer TaskBucketKeyer) context.Context {
	if keyer == nil {
		return ctx
	}
	return context.WithValue(ctx, taskBucketKeyerCtxKey{}, keyer)
}

// TaskBucketKey resolves the task-store isolation bucket for this request. If a
// TaskBucketKeyer was installed (via WithTaskBucketKeyer / the
// server.WithTaskBucketKeyer option) it is used; otherwise it falls back to the
// session ID. This is the single accessor both the v1 and v2 task surfaces call
// instead of reading the session ID directly.
func TaskBucketKey(ctx context.Context) string {
	if k, ok := ctx.Value(taskBucketKeyerCtxKey{}).(TaskBucketKeyer); ok && k != nil {
		return k(ctx)
	}
	return GetSessionID(ctx)
}

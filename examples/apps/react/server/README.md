# examples/apps/react/server

React MCP App server. The accompanying React client lives at `../client/`.

> **SEP-2577 deprecation note**: this example demonstrates `ctx.Sample(...)`, which is deprecated per SEP-2577 (scheduled for removal in mcpkit v0.4). The code still works on v0.3.x. See [`docs/SEP_2577_DEPRECATIONS.md`](../../../../docs/SEP_2577_DEPRECATIONS.md) for the migration story.

Run from this directory:

```
go run .
```

Pairs with the React frontend in `../client/` — see that directory's README for the host-side setup.

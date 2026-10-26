package stateless

import (
	"context"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

func TestRequestMetaFromContext(t *testing.T) {
	if got := RequestMetaFromContext(context.Background()); got != nil {
		t.Errorf("RequestMetaFromContext(bare) = %+v, want nil", got)
	}
	meta := &core.RequestMeta{ProtocolVersion: "2026-07-28"}
	ctx := core.WithRequestMeta(context.Background(), meta)
	if got := RequestMetaFromContext(ctx); got != meta {
		t.Errorf("RequestMetaFromContext: got %+v, want %+v", got, meta)
	}
}

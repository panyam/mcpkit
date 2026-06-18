package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetScopes_NoClaims(t *testing.T) {
	assert.Nil(t, GetScopes(context.Background()), "absent claims must yield nil, not empty slice")
}

func TestGetScopes_PresentWithScopes(t *testing.T) {
	ctx := ContextWithSession(context.Background(), nil, nil, nil, nil,
		&Claims{Subject: "u", Scopes: []string{"docs:read", "docs:write"}})
	assert.Equal(t, []string{"docs:read", "docs:write"}, GetScopes(ctx))
}

func TestGetScopes_PresentEmptyScopes(t *testing.T) {
	ctx := ContextWithSession(context.Background(), nil, nil, nil, nil,
		&Claims{Subject: "u", Scopes: nil})
	assert.Nil(t, GetScopes(ctx))
}

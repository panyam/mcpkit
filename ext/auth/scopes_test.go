package auth_test

import (
	"testing"

	"github.com/panyam/mcpkit/ext/auth"
	"github.com/stretchr/testify/assert"
)

func TestUnionScopes_BothNil(t *testing.T) {
	assert.Nil(t, auth.UnionScopes(nil, nil))
}

func TestUnionScopes_DisjointSetsPreserveOrder(t *testing.T) {
	got := auth.UnionScopes([]string{"a", "b"}, []string{"c", "d"})
	assert.Equal(t, []string{"a", "b", "c", "d"}, got, "disjoint sets concatenate in (a, b) order")
}

func TestUnionScopes_OverlapDedupsKeepingFirstOccurrence(t *testing.T) {
	got := auth.UnionScopes([]string{"a", "b"}, []string{"b", "c"})
	assert.Equal(t, []string{"a", "b", "c"}, got, "overlapping entries appear once, in first-seen order")
}

func TestUnionScopes_NilFirstReturnsSecondCopy(t *testing.T) {
	b := []string{"x", "y"}
	got := auth.UnionScopes(nil, b)
	assert.Equal(t, []string{"x", "y"}, got)
	got[0] = "MUTATED"
	assert.Equal(t, "x", b[0], "caller's slice must not be mutated through the returned union")
}

func TestUnionScopes_NilSecondReturnsFirstCopy(t *testing.T) {
	a := []string{"x", "y"}
	got := auth.UnionScopes(a, nil)
	assert.Equal(t, []string{"x", "y"}, got)
	got[0] = "MUTATED"
	assert.Equal(t, "x", a[0])
}

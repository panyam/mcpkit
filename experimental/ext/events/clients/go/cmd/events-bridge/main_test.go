package main

import (
	"testing"
	"time"

	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/experimental/ext/events/clients/go/bridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChooseApprover(t *testing.T) {
	d := eventsclient.Delivery{}

	approver, pending := chooseApprover(false, bridge.DefaultApprovalTimeout)
	require.NotNil(t, pending, "by default a person answers through the approvals tools")
	assert.Same(t, pending, approver)
	assert.Equal(t, bridge.DefaultApprovalTimeout, pending.Timeout)

	approver, pending = chooseApprover(true, time.Minute)
	assert.Nil(t, pending, "-approve-outward asks nobody")
	ok, err := approver.Approve(t.Context(), d, []string{"x"})
	require.NoError(t, err)
	assert.True(t, ok)

	approver, pending = chooseApprover(false, 0)
	assert.Nil(t, pending, "-approval-timeout 0 asks nobody")
	ok, err = approver.Approve(t.Context(), d, []string{"x"})
	require.NoError(t, err)
	assert.False(t, ok, "and denies at once")
}

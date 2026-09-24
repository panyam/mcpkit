package events

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Spec §"Endpoint verification": the verification POST "SHOULD be
// rate-limited per destination host". The (principal, url) cache caps what one
// principal can aim at one URL; it does nothing about many paths, or many
// principals, on one host. Issue 1438.

func TestVerificationRateLimit_ThrottlesChallengesToOneHost(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	srv, _ := buildVerifyingStack(t, "alice", WithVerificationRateLimit(3, time.Minute))

	for i := 0; i < 3; i++ {
		resp := dispatchSubscribe(t, srv, webhookSubscribeParams(fmt.Sprintf("%s/hook-%d", rcv.URL, i), generateSecret(), nil))
		require.Nil(t, resp.Error, "challenge %d is inside the budget: %+v", i, resp.Error)
	}
	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL+"/hook-over", generateSecret(), nil))

	require.NotNil(t, resp.Error, "the fourth challenge to one host inside the window must be refused")
	assert.Equal(t, ErrCodeResourceExhausted, resp.Error.Code)
	raw, _ := json.Marshal(resp.Error.Data)
	var data ResourceExhaustedData
	require.NoError(t, json.Unmarshal(raw, &data))
	assert.Equal(t, "verifications_per_host", data.Limit)
	assert.Equal(t, int64(3), data.Max)
	assert.Equal(t, 3, rcv.verificationCount(), "a throttled challenge must not be sent")
}

func TestVerificationRateLimit_HostsAreIndependent(t *testing.T) {
	a := newVerificationReceiver(t, nil)
	b := newVerificationReceiver(t, nil)
	srv, _ := buildVerifyingStack(t, "alice", WithVerificationRateLimit(1, time.Minute))

	require.Nil(t, dispatchSubscribe(t, srv, webhookSubscribeParams(a.URL+"/x", generateSecret(), nil)).Error)
	// httptest servers share 127.0.0.1, so "localhost" is the second host.
	other := "http://localhost:" + b.URL[len("http://127.0.0.1:"):] + "/x"
	require.Nil(t, dispatchSubscribe(t, srv, webhookSubscribeParams(other, generateSecret(), nil)).Error,
		"another host has its own budget")
}

func TestVerificationRateLimit_CacheHitsCostNothing(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	srv, _ := buildVerifyingStack(t, "alice", WithVerificationRateLimit(1, time.Minute))
	secret := generateSecret()

	for i := 0; i < 5; i++ {
		resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL, secret, map[string]any{"n": i}))
		require.Nil(t, resp.Error, "subscribe %d rides the cached verification: %+v", i, resp.Error)
	}
	assert.Equal(t, 1, rcv.verificationCount())
}

func TestVerificationRateLimit_RefillsOverTheWindow(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	srv, _ := buildVerifyingStack(t, "alice", WithVerificationRateLimit(1, 200*time.Millisecond))

	require.Nil(t, dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL+"/a", generateSecret(), nil)).Error)
	require.NotNil(t, dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL+"/b", generateSecret(), nil)).Error)
	time.Sleep(250 * time.Millisecond)
	require.Nil(t, dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL+"/c", generateSecret(), nil)).Error,
		"a full window later the budget is back")
}

func TestVerificationRateLimit_DefaultIsOnAndZeroDisables(t *testing.T) {
	assert.Equal(t, DefaultVerificationsPerHost, NewWebhookRegistry().verifyLimit.max)

	rcv := newVerificationReceiver(t, nil)
	srv, _ := buildVerifyingStack(t, "alice", WithVerificationRateLimit(0, 0))
	for i := 0; i < DefaultVerificationsPerHost+5; i++ {
		require.Nil(t, dispatchSubscribe(t, srv, webhookSubscribeParams(fmt.Sprintf("%s/h%d", rcv.URL, i), generateSecret(), nil)).Error)
	}
}

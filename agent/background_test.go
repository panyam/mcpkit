package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

type hookLog struct {
	mu     sync.Mutex
	events []string
}

func (h *hookLog) add(e string) {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
}

func (h *hookLog) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.events...)
}

func (h *hookLog) waitFor(t *testing.T, prefix string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range h.snapshot() {
			if strings.HasPrefix(e, prefix) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for hook %q, saw %v", prefix, h.snapshot())
}

func connectGraceSource(t *testing.T, f *taskFixture, grace time.Duration, ui ElicitationUI) (*ClientSource, *hookLog) {
	t.Helper()
	src, _ := connectTaskSource(t, f, ui)
	log := &hookLog{}
	src.taskGrace = grace
	src.onDetach = func(bt *client.BackgroundTask) { log.add("detach:" + bt.TaskID) }
	src.onComplete = func(bt *client.BackgroundTask) { log.add("complete:" + bt.TaskID) }
	return src, log
}

func acceptAda(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
	return core.ElicitationResult{Action: "accept", Content: map[string]any{"name": "Ada"}}, nil
}

func TestGraceDetachAndBackgroundCompletion(t *testing.T) {
	f := &taskFixture{holdUntil: make(chan struct{})}
	src, events := connectGraceSource(t, f, 60*time.Millisecond, acceptAda)

	var btMu sync.Mutex
	var detachedBT *client.BackgroundTask
	src.onDetach = func(bt *client.BackgroundTask) {
		btMu.Lock()
		detachedBT = bt
		btMu.Unlock()
		events.add("detach:" + bt.TaskID)
	}

	res, err := src.Call(context.Background(), "long_job", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "moved to the background") {
		t.Fatalf("detached call must return the started result, got %+v", res)
	}
	btMu.Lock()
	defer btMu.Unlock()
	if detachedBT == nil || detachedBT.Tool != "long_job" {
		t.Fatalf("detach hook must deliver the handle: %+v", detachedBT)
	}
	if detachedBT.Status() != core.TaskWorking {
		t.Fatalf("status = %v", detachedBT.Status())
	}

	close(f.holdUntil)
	select {
	case <-detachedBT.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("background completion never arrived")
	}
	bres, berr := detachedBT.Result()
	if berr != nil || bres.Status != core.TaskCompleted || bres.Result.Content[0].Text != "held job done" {
		t.Fatalf("background result = %+v %v", bres, berr)
	}
	events.waitFor(t, "complete:")
}

func TestGraceInlineCompletionSkipsDetach(t *testing.T) {
	f := &taskFixture{} // normal flow: input pause then complete, all fast
	src, events := connectGraceSource(t, f, 5*time.Second, acceptAda)

	res, err := src.Call(context.Background(), "long_job", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "job done for Ada") {
		t.Fatalf("inline completion must return the real result: %+v", res)
	}
	if got := events.snapshot(); len(got) != 0 {
		t.Fatalf("no hooks may fire for inline completion: %v", got)
	}
}

func TestGraceHoldsDuringInputPause(t *testing.T) {
	f := &taskFixture{}
	// The fixture parks input_required on poll 2. The UI takes far longer
	// than the grace to answer; the timer must hold instead of detaching
	// mid-prompt.
	slowUI := func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		time.Sleep(200 * time.Millisecond)
		return acceptAda(ctx, req)
	}
	src, events := connectGraceSource(t, f, 50*time.Millisecond, slowUI)

	res, err := src.Call(context.Background(), "long_job", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "job done for Ada") {
		t.Fatalf("input pause must stay inline: %+v", res)
	}
	for _, e := range events.snapshot() {
		if strings.HasPrefix(e, "detach:") {
			t.Fatalf("must not detach during an active input pause: %v", events.snapshot())
		}
	}
}

func TestBackgroundCancel(t *testing.T) {
	f := &taskFixture{holdUntil: make(chan struct{})}
	src, _ := connectGraceSource(t, f, 40*time.Millisecond, acceptAda)
	btCh := make(chan *client.BackgroundTask, 1)
	src.onDetach = func(b *client.BackgroundTask) { btCh <- b }

	if _, err := src.Call(context.Background(), "long_job", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	var bt *client.BackgroundTask
	select {
	case bt = <-btCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected detach")
	}
	if err := bt.Cancel(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bt.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("cancel must finish the handle")
	}
	// Cancel reaches Done two ways, both valid: the poll context cancels
	// (context.Canceled) or the next tasks/get returns cancelled status
	// (nil error, cancelled DetailedTask). Accept either.
	dt, err := bt.Result()
	cancelled := errors.Is(err, context.Canceled) || (dt != nil && dt.Status == core.TaskCancelled)
	if !cancelled {
		t.Fatalf("cancel must surface a cancellation outcome: dt=%+v err=%v", dt, err)
	}
}

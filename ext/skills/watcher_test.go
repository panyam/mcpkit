package skills_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

func TestFSWatcher_EventOpMapping_Create(t *testing.T) {
	action, fwd := skills.ExportedMapFSNotifyOp(fsnotify.Create)
	if !fwd {
		t.Fatalf("Create not forwarded")
	}
	if action != skills.ChangeActionCreated {
		t.Errorf("Create → %q, want %q", action, skills.ChangeActionCreated)
	}
}

func TestFSWatcher_EventOpMapping_Write(t *testing.T) {
	action, fwd := skills.ExportedMapFSNotifyOp(fsnotify.Write)
	if !fwd {
		t.Fatalf("Write not forwarded")
	}
	if action != skills.ChangeActionModified {
		t.Errorf("Write → %q, want %q", action, skills.ChangeActionModified)
	}
}

func TestFSWatcher_EventOpMapping_Remove(t *testing.T) {
	action, fwd := skills.ExportedMapFSNotifyOp(fsnotify.Remove)
	if !fwd {
		t.Fatalf("Remove not forwarded")
	}
	if action != skills.ChangeActionDeleted {
		t.Errorf("Remove → %q, want %q", action, skills.ChangeActionDeleted)
	}
}

func TestFSWatcher_EventOpMapping_Rename(t *testing.T) {
	action, fwd := skills.ExportedMapFSNotifyOp(fsnotify.Rename)
	if !fwd {
		t.Fatalf("Rename not forwarded")
	}
	if action != skills.ChangeActionDeleted {
		t.Errorf("Rename → %q, want %q (rename = deleted from watcher POV)", action, skills.ChangeActionDeleted)
	}
}

func TestProvider_FSWatcher_RequiresHostRoot(t *testing.T) {
	_, err := skills.NewProvider(
		skills.WithFS(fstest.MapFS{
			"foo/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: foo\ndescription: x\n---\n")},
		}),
		skills.WithFSWatcher(),
	)
	if err == nil {
		t.Fatalf("NewProvider with WithFS + WithFSWatcher succeeded; want ErrFSWatcherMissingHostRoot")
	}
	if !errors.Is(err, skills.ErrFSWatcherMissingHostRoot) {
		t.Errorf("error = %v, want ErrFSWatcherMissingHostRoot", err)
	}
}

func TestProvider_FSWatcher_DefaultsToNoWatcher(t *testing.T) {
	p, err := skills.NewProvider(skills.WithDirectory("testdata/valid"))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { p.Close() })
}

func TestProvider_FSWatcher_BroadcastsOnFileModify(t *testing.T) {
	dir := stageSkillsDir(t)
	c, p := bootFSWatcherTest(t, dir, skills.WithCoalesceWindow(150*time.Millisecond))

	payloads := make(chan skills.PathsChangedPayload, 4)
	listenForPayloads(t, c, payloads)

	target := filepath.Join(dir, "git-workflow", "SKILL.md")
	body := []byte("---\nname: git-workflow\ndescription: edited at " + time.Now().Format(time.RFC3339Nano) + "\n---\n\nupdated body\n")
	if err := os.WriteFile(target, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}

	select {
	case payload := <-payloads:
		entry, ok := payload.Paths["git-workflow/SKILL.md"]
		if !ok {
			t.Fatalf("payload missing git-workflow/SKILL.md (paths: %v)", payload.Paths)
		}
		if entry.Action != skills.ChangeActionModified {
			t.Errorf("action = %q, want %q", entry.Action, skills.ChangeActionModified)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no broadcast received within deadline")
	}
	_ = p
}

func TestProvider_FSWatcher_BroadcastsOnFileCreate(t *testing.T) {
	dir := stageSkillsDir(t)
	c, p := bootFSWatcherTest(t, dir, skills.WithCoalesceWindow(150*time.Millisecond))

	payloads := make(chan skills.PathsChangedPayload, 4)
	listenForPayloads(t, c, payloads)

	target := filepath.Join(dir, "git-workflow", "newfile.md")
	if err := os.WriteFile(target, []byte("# new file"), 0o644); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}

	select {
	case payload := <-payloads:
		entry, ok := payload.Paths["git-workflow/newfile.md"]
		if !ok {
			t.Fatalf("payload missing git-workflow/newfile.md (paths: %v)", payload.Paths)
		}
		if entry.Action != skills.ChangeActionCreated && entry.Action != skills.ChangeActionModified {
			t.Errorf("action = %q, want Created or Modified", entry.Action)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no broadcast received within deadline")
	}
	_ = p
}

func TestProvider_FSWatcher_BroadcastsOnFileDelete(t *testing.T) {
	dir := stageSkillsDir(t)
	c, p := bootFSWatcherTest(t, dir, skills.WithCoalesceWindow(150*time.Millisecond))

	payloads := make(chan skills.PathsChangedPayload, 4)
	listenForPayloads(t, c, payloads)

	target := filepath.Join(dir, "git-workflow", "SKILL.md")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove %s: %v", target, err)
	}

	select {
	case payload := <-payloads:
		entry, ok := payload.Paths["git-workflow/SKILL.md"]
		if !ok {
			t.Fatalf("payload missing git-workflow/SKILL.md (paths: %v)", payload.Paths)
		}
		if entry.Action != skills.ChangeActionDeleted {
			t.Errorf("action = %q, want %q", entry.Action, skills.ChangeActionDeleted)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no broadcast received within deadline")
	}
	_ = p
}

func TestProvider_FSWatcher_RespectsIgnoreList(t *testing.T) {
	dir := stageSkillsDir(t)
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	c, p := bootFSWatcherTest(t, dir, skills.WithCoalesceWindow(100*time.Millisecond))

	got := atomic.Int32{}
	c.RegisterNotificationOnList(func() { got.Add(1) }, t)

	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	if c := got.Load(); c != 0 {
		t.Errorf("writes to .git/ triggered %d broadcasts, want 0", c)
	}
	_ = p
}

func TestProvider_Shutdown_DrainsBufferedEvents(t *testing.T) {
	dir := stageSkillsDir(t)
	c, p := bootFSWatcherTest(t, dir, skills.WithCoalesceWindow(500*time.Millisecond))

	payloads := make(chan skills.PathsChangedPayload, 4)
	listenForPayloads(t, c, payloads)

	target := filepath.Join(dir, "git-workflow", "SKILL.md")
	if err := os.WriteFile(target, []byte("---\nname: git-workflow\ndescription: y\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case payload := <-payloads:
		if _, ok := payload.Paths["git-workflow/SKILL.md"]; !ok {
			t.Errorf("graceful shutdown lost the pending path (paths: %v)", payload.Paths)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("Shutdown did not flush the pending broadcast")
	}
}

func TestProvider_Close_StopsWatcherAbruptly(t *testing.T) {
	dir := stageSkillsDir(t)
	c, p := bootFSWatcherTest(t, dir, skills.WithCoalesceWindow(100*time.Millisecond))

	got := atomic.Int32{}
	c.RegisterNotificationOnList(func() { got.Add(1) }, t)

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	target := filepath.Join(dir, "git-workflow", "SKILL.md")
	if err := os.WriteFile(target, []byte("post-close"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if c := got.Load(); c != 0 {
		t.Errorf("after Close: %d broadcasts arrived, want 0", c)
	}
}

func stageSkillsDir(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	if err := copyTree("testdata/valid", dst); err != nil {
		t.Fatalf("stage skills: %v", err)
	}
	return dst
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

func bootFSWatcherTest(t *testing.T, dir string, providerOpts ...skills.ProviderOption) (*clientWithCallbacks, *skills.Provider) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "skills-test", Version: "0.0.1"})

	opts := []skills.ProviderOption{
		skills.WithDirectory(dir),
		skills.WithFSWatcher(),
	}
	opts = append(opts, providerOpts...)
	p, err := skills.NewProvider(opts...)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)
	t.Cleanup(func() { p.Close() })

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	wrapper := &clientWithCallbacks{}
	c := client.NewClient(ts.URL+"/mcp",
		core.ClientInfo{Name: "skills-test-client", Version: "0.0.1"},
		client.WithGetSSEStream(),
		client.WithNotificationCallback(wrapper.onNotify),
	)
	wrapper.Client = c
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	time.Sleep(150 * time.Millisecond)
	return wrapper, p
}

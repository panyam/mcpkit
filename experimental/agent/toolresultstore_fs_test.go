package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileToolResultStore_PutGet(t *testing.T) {
	ctx := context.Background()
	s, err := NewFileToolResultStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutToolResult(ctx, PutToolResultRequest{Ref: "res:a1b2", Result: textResult("payload")}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	resp, err := s.GetToolResult(ctx, GetToolResultRequest{Ref: "res:a1b2"})
	if err != nil || !resp.Found {
		t.Fatalf("GetToolResult = (%+v, %v)", resp, err)
	}
	if toolResultText(&resp.Result) != "payload" {
		t.Fatalf("round-trip lost content: %q", toolResultText(&resp.Result))
	}
}

func TestFileToolResultStore_UnknownRefIsAppState(t *testing.T) {
	s, err := NewFileToolResultStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.GetToolResult(context.Background(), GetToolResultRequest{Ref: "res:gone"})
	if err != nil || resp.Found {
		t.Fatalf("Get(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
}

// TestFileToolResultStore_SurvivesReopen pins the local-agent value: a
// second store over the same directory sees blobs the first wrote, with
// no server involved.
func TestFileToolResultStore_SurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "blobs")

	s1, err := NewFileToolResultStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.PutToolResult(ctx, PutToolResultRequest{Ref: "res:keep", Result: textResult("durable")}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}

	s2, err := NewFileToolResultStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s2.GetToolResult(ctx, GetToolResultRequest{Ref: "res:keep"})
	if err != nil || !resp.Found {
		t.Fatalf("blob did not survive reopen: (%+v, %v)", resp, err)
	}
	if toolResultText(&resp.Result) != "durable" {
		t.Fatalf("reopened blob wrong: %q", toolResultText(&resp.Result))
	}
}

func TestFileToolResultStore_DeletedBlobIsGraceful(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewFileToolResultStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutToolResult(ctx, PutToolResultRequest{Ref: "res:tmp", Result: textResult("x")}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	// an operator (or retention sweep) deletes the file
	if err := os.Remove(s.path("res:tmp")); err != nil {
		t.Fatalf("removing blob: %v", err)
	}
	if resp, err := s.GetToolResult(ctx, GetToolResultRequest{Ref: "res:tmp"}); err != nil || resp.Found {
		t.Fatalf("Get after delete = (%+v, %v), want Found=false", resp, err)
	}
}

func TestRefToFilename_SafeAndInjective(t *testing.T) {
	// the ':' in a minted ref is escaped, and no unsafe char leaks into
	// the filename
	name := refToFilename("res:ab12")
	if name != "res_3aab12.json" {
		t.Fatalf("refToFilename(res:ab12) = %q, want res_3aab12.json", name)
	}
	// distinct refs never collide, including refs that could alias under
	// naive sanitization (":" vs "_3a")
	if refToFilename("res:x") == refToFilename("res_3ax") {
		t.Fatal("escaping is not injective: two refs collided on one filename")
	}
	for _, bad := range []rune{':', '/', '\\', ' ', '_'} {
		if strings.ContainsRune(refToFilename(string(bad)), bad) && bad != '_' {
			t.Fatalf("unsafe rune %q leaked into filename", bad)
		}
	}
}

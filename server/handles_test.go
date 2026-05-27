package server

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type cart struct {
	Items []string
	Total float64
}

func TestHandleStore_MintAndGet(t *testing.T) {
	s := NewHandleStore[cart]()
	defer s.Close()

	original := cart{Items: []string{"apple", "bread"}, Total: 4.50}
	id := s.Mint(original, 0)
	if id == "" {
		t.Fatal("Mint returned empty id")
	}

	got, ok := s.Get(id)
	if !ok {
		t.Fatalf("Get(%q) ok=false; want true", id)
	}
	if got.Total != original.Total || len(got.Items) != len(original.Items) {
		t.Errorf("Get returned %+v, want %+v", got, original)
	}
}

func TestHandleStore_Get_MissingIDReturnsZero(t *testing.T) {
	s := NewHandleStore[cart]()
	defer s.Close()

	got, ok := s.Get("does-not-exist")
	if ok {
		t.Errorf("Get(unknown) ok=true; want false")
	}
	if got.Total != 0 || got.Items != nil {
		t.Errorf("Get(unknown) returned non-zero: %+v", got)
	}
}

func TestHandleStore_Delete(t *testing.T) {
	s := NewHandleStore[cart]()
	defer s.Close()

	id := s.Mint(cart{}, 0)
	if !s.Delete(id) {
		t.Errorf("Delete returned false on a known id")
	}
	if _, ok := s.Get(id); ok {
		t.Errorf("Get returned ok after Delete")
	}
	if s.Delete(id) {
		t.Errorf("second Delete returned true on already-removed id")
	}
}

func TestHandleStore_Put(t *testing.T) {
	s := NewHandleStore[cart]()
	defer s.Close()

	id := s.Mint(cart{Total: 1}, 0)
	// Put under an existing id → overwrite + report existed.
	existed := s.Put(id, cart{Total: 99, Items: []string{"x"}}, 0)
	if !existed {
		t.Errorf("Put returned existed=false for known id")
	}
	got, ok := s.Get(id)
	if !ok || got.Total != 99 || len(got.Items) != 1 {
		t.Errorf("Get after Put returned %+v ok=%v, want updated value", got, ok)
	}

	// Put under an unknown id → insert + report existed=false.
	existed = s.Put("never-minted", cart{Total: 7}, 0)
	if existed {
		t.Errorf("Put returned existed=true for unknown id")
	}
	got, ok = s.Get("never-minted")
	if !ok || got.Total != 7 {
		t.Errorf("Get after seed-Put returned %+v ok=%v, want Total=7", got, ok)
	}
}

func TestHandleStore_LazyExpiry(t *testing.T) {
	// No gc goroutine — verify Get returns ok=false past TTL.
	s := NewHandleStore[cart]()
	defer s.Close()
	id := s.Mint(cart{Total: 99}, 10*time.Millisecond)
	if _, ok := s.Get(id); !ok {
		t.Fatal("freshly-minted handle missing")
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := s.Get(id); ok {
		t.Errorf("Get after expiry ok=true; want false")
	}
}

func TestHandleStore_BackgroundGC(t *testing.T) {
	s := NewHandleStore[cart](WithHandleGCInterval(5 * time.Millisecond))
	defer s.Close()

	for i := 0; i < 50; i++ {
		s.Mint(cart{Total: float64(i)}, 10*time.Millisecond)
	}
	if s.Len() != 50 {
		t.Fatalf("Len = %d, want 50", s.Len())
	}
	time.Sleep(60 * time.Millisecond) // a few GC ticks past expiry
	if got := s.Len(); got != 0 {
		t.Errorf("Len after GC = %d, want 0", got)
	}
}

func TestHandleStore_DefaultTTL(t *testing.T) {
	s := NewHandleStore[cart](WithHandleDefaultTTL(20 * time.Millisecond))
	defer s.Close()
	id := s.Mint(cart{}, 0) // 0 → falls back to default
	time.Sleep(5 * time.Millisecond)
	if _, ok := s.Get(id); !ok {
		t.Errorf("handle expired prematurely (5ms < 20ms default TTL)")
	}
	time.Sleep(25 * time.Millisecond)
	if _, ok := s.Get(id); ok {
		t.Errorf("handle did not honor default TTL")
	}
}

func TestHandleStore_NegativeTTLForcesNoExpiry(t *testing.T) {
	s := NewHandleStore[cart](WithHandleDefaultTTL(5 * time.Millisecond))
	defer s.Close()
	// Negative TTL overrides the default and pins "never expires".
	id := s.Mint(cart{}, -1)
	time.Sleep(15 * time.Millisecond)
	if _, ok := s.Get(id); !ok {
		t.Errorf("negative-TTL handle was expired against caller intent")
	}
}

func TestHandleStore_IDPrefix(t *testing.T) {
	s := NewHandleStore[cart](WithHandleIDPrefix("cart"))
	defer s.Close()
	id := s.Mint(cart{}, 0)
	if !strings.HasPrefix(id, "cart-") {
		t.Errorf("id = %q, want prefix cart-", id)
	}
}

func TestHandleStore_IDsAreUnique(t *testing.T) {
	s := NewHandleStore[cart]()
	defer s.Close()
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := s.Mint(cart{}, 0)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id at iteration %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestHandleStore_ConcurrentMintGetDelete(t *testing.T) {
	s := NewHandleStore[cart]()
	defer s.Close()

	const workers = 20
	const opsPerWorker = 200
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				id := s.Mint(cart{Total: float64(j)}, 0)
				if _, ok := s.Get(id); !ok {
					t.Errorf("Get after Mint failed")
					return
				}
				s.Delete(id)
			}
		}()
	}
	wg.Wait()
	if s.Len() != 0 {
		t.Errorf("Len after concurrent Mint/Delete = %d, want 0", s.Len())
	}
}

func TestHandleStore_TypeIsolation(t *testing.T) {
	// Two stores of different types share no state. This is the
	// expected pattern when a server holds multiple kinds of handles.
	carts := NewHandleStore[cart]()
	defer carts.Close()
	type doc struct{ Body string }
	docs := NewHandleStore[doc]()
	defer docs.Close()

	cid := carts.Mint(cart{Total: 9}, 0)
	did := docs.Mint(doc{Body: "x"}, 0)

	if _, ok := carts.Get(did); ok {
		t.Errorf("carts store returned ok for a doc id (cross-store leak)")
	}
	if _, ok := docs.Get(cid); ok {
		t.Errorf("docs store returned ok for a cart id (cross-store leak)")
	}
}

func TestHandleStore_CloseIsIdempotent(t *testing.T) {
	s := NewHandleStore[cart](WithHandleGCInterval(5 * time.Millisecond))
	s.Close()
	s.Close() // must not panic / deadlock
}

// TestHandleStore_UsedThroughInterface demonstrates the design intent:
// tool authors program against the HandleStore[T] interface, so future
// backends (Redis etc.) drop in at the call site without code change.
// The compile-time assert in handles.go (var _ HandleStore[struct{}] =
// ...) catches drift between interface and impl; this runtime test
// shows the call-site shape callers should adopt.
func TestHandleStore_UsedThroughInterface(t *testing.T) {
	var s HandleStore[cart] = NewHandleStore[cart]()
	defer s.Close()
	id := s.Mint(cart{Total: 42}, 0)
	got, ok := s.Get(id)
	if !ok || got.Total != 42 {
		t.Errorf("Mint/Get round-trip via interface failed: ok=%v got=%+v", ok, got)
	}
	if !s.Delete(id) {
		t.Errorf("Delete returned false via interface")
	}
}

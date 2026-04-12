package auth

import (
	"sync"

	"github.com/panyam/oneauth/client"
)

// MemoryCredentialStore is a simple in-memory implementation of
// client.CredentialStore. Used as the default backing store for
// OAuthTokenSource when no external CredStore is provided, so the
// refresh_token grant path (AuthClient.GetToken -> refreshTokenLocked)
// has a place to read the current credential from.
//
// Not suitable for cross-process or cross-restart persistence. For those,
// consumers should provide their own CredStore (e.g., a file-backed one).
//
// Zero value is ready to use. NewMemoryCredentialStore returns an
// equivalent initialized value for callers who prefer explicit
// construction.
type MemoryCredentialStore struct {
	mu    sync.Mutex
	creds map[string]*client.ServerCredential
}

// NewMemoryCredentialStore returns a new, empty MemoryCredentialStore.
func NewMemoryCredentialStore() *MemoryCredentialStore {
	return &MemoryCredentialStore{
		creds: make(map[string]*client.ServerCredential),
	}
}

// GetCredential returns the stored credential for serverURL, or nil if none.
// Returns a copy so callers cannot mutate cached state through the pointer.
func (s *MemoryCredentialStore) GetCredential(serverURL string) (*client.ServerCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creds == nil {
		return nil, nil
	}
	cred, ok := s.creds[serverURL]
	if !ok || cred == nil {
		return nil, nil
	}
	copy := *cred
	return &copy, nil
}

// SetCredential stores the credential for serverURL, overwriting any
// previous entry. Stores a copy so the caller cannot mutate cached state.
func (s *MemoryCredentialStore) SetCredential(serverURL string, cred *client.ServerCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creds == nil {
		s.creds = make(map[string]*client.ServerCredential)
	}
	if cred == nil {
		delete(s.creds, serverURL)
		return nil
	}
	copy := *cred
	s.creds[serverURL] = &copy
	return nil
}

// RemoveCredential deletes the stored credential for serverURL. No-op if
// no entry exists.
func (s *MemoryCredentialStore) RemoveCredential(serverURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, serverURL)
	return nil
}

// ListServers returns the set of server URLs with stored credentials.
func (s *MemoryCredentialStore) ListServers() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	servers := make([]string, 0, len(s.creds))
	for url := range s.creds {
		servers = append(servers, url)
	}
	return servers, nil
}

// Save is a no-op for in-memory storage.
func (s *MemoryCredentialStore) Save() error {
	return nil
}

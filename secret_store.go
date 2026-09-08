package main

import (
	"errors"
	"fmt"
	"sync"
)

// ErrSecureStorageUnavailable is returned when the OS protected store cannot
// be reached. MCP sessions fail closed; the rest of the launcher stays usable.
var ErrSecureStorageUnavailable = errors.New("secure credential storage is unavailable")

// SecretStore is the native protected store for assistant session secrets.
// There is no file fallback.
type SecretStore interface {
	Name() string
	Available() error
	Set(account, secret string) error
	Get(account string) (string, error)
	Delete(account string) error
}

// MemorySecretStore is the test double. Production uses defaultSecretStore().
type MemorySecretStore struct {
	mu      sync.Mutex
	name    string
	err     error
	secrets map[string]string
}

func NewMemorySecretStore() *MemorySecretStore {
	return &MemorySecretStore{name: "memory", secrets: map[string]string{}}
}

func (s *MemorySecretStore) Name() string { return s.name }

func (s *MemorySecretStore) Available() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *MemorySecretStore) SetUnavailable(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *MemorySecretStore) Set(account, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.secrets == nil {
		s.secrets = map[string]string{}
	}
	s.secrets[account] = secret
	return nil
}

func (s *MemorySecretStore) Get(account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	secret, ok := s.secrets[account]
	if !ok {
		return "", fmt.Errorf("assistant session is missing or has been revoked")
	}
	return secret, nil
}

func (s *MemorySecretStore) Delete(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	delete(s.secrets, account)
	return nil
}

func storageStatus(store SecretStore) AgentStorageStatus {
	if store == nil {
		store = defaultSecretStore()
	}
	status := AgentStorageStatus{Backend: store.Name(), Available: true}
	if err := store.Available(); err != nil {
		status.Available = false
		status.Message = fmt.Sprintf(
			"%s is unavailable or locked. Ligand-X itself still works; reconnecting an AI assistant needs an unlocked %s.",
			store.Name(), store.Name(),
		)
		return status
	}
	status.Message = store.Name() + " is ready for assistant sessions."
	return status
}

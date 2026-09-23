// Package secretstore keeps assistant session secrets in the OS protected
// credential store (Keychain, Credential Manager, Secret Service).
package secretstore

import (
	"errors"
	"fmt"
	"sync"
)

// ErrUnavailable is returned when the OS protected store cannot
// be reached. MCP sessions fail closed; the rest of the launcher stays usable.
var ErrUnavailable = errors.New("secure credential storage is unavailable")

// Store is the native protected store for assistant session secrets.
// There is no file fallback.
type Store interface {
	Name() string
	Available() error
	Set(account, secret string) error
	Get(account string) (string, error)
	Delete(account string) error
}

// Memory is the test double. Production uses Default().
type Memory struct {
	mu      sync.Mutex
	name    string
	err     error
	secrets map[string]string
}

func NewMemory() *Memory {
	return &Memory{name: "memory", secrets: map[string]string{}}
}

func (s *Memory) Name() string { return s.name }

func (s *Memory) Available() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Memory) SetUnavailable(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *Memory) Set(account, secret string) error {
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

func (s *Memory) Get(account string) (string, error) {
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

func (s *Memory) Delete(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	delete(s.secrets, account)
	return nil
}

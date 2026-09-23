package secretstore

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/zalando/go-keyring"
)

const agentKeyringService = "com.ligandx.launcher.agent"

type native struct{}

// Default returns the platform's native protected store.
func Default() Store {
	return native{}
}

func (native) Name() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS Keychain"
	case "windows":
		return "Windows Credential Manager"
	default:
		return "Linux Secret Service"
	}
}

func (s native) Available() error {
	_, err := keyring.Get(agentKeyringService, "__ligandx_probe__")
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%w: %s (%v)", ErrUnavailable, s.Name(), err)
}

func (s native) Set(account, secret string) error {
	if err := s.Available(); err != nil {
		return err
	}
	if err := keyring.Set(agentKeyringService, account, secret); err != nil {
		return fmt.Errorf("%w: %s (%v)", ErrUnavailable, s.Name(), err)
	}
	return nil
}

func (s native) Get(account string) (string, error) {
	if err := s.Available(); err != nil {
		return "", err
	}
	secret, err := keyring.Get(agentKeyringService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("assistant session is missing or has been revoked")
	}
	if err != nil {
		return "", fmt.Errorf("%w: %s (%v)", ErrUnavailable, s.Name(), err)
	}
	return secret, nil
}

func (s native) Delete(account string) error {
	if err := s.Available(); err != nil {
		return err
	}
	err := keyring.Delete(agentKeyringService, account)
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%w: %s (%v)", ErrUnavailable, s.Name(), err)
}

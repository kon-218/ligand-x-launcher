package main

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/zalando/go-keyring"
)

const agentKeyringService = "com.ligandx.launcher.agent"

type nativeSecretStore struct{}

func defaultSecretStore() SecretStore {
	return nativeSecretStore{}
}

func (nativeSecretStore) Name() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS Keychain"
	case "windows":
		return "Windows Credential Manager"
	default:
		return "Linux Secret Service"
	}
}

func (s nativeSecretStore) Available() error {
	_, err := keyring.Get(agentKeyringService, "__ligandx_probe__")
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%w: %s (%v)", ErrSecureStorageUnavailable, s.Name(), err)
}

func (s nativeSecretStore) Set(account, secret string) error {
	if err := s.Available(); err != nil {
		return err
	}
	if err := keyring.Set(agentKeyringService, account, secret); err != nil {
		return fmt.Errorf("%w: %s (%v)", ErrSecureStorageUnavailable, s.Name(), err)
	}
	return nil
}

func (s nativeSecretStore) Get(account string) (string, error) {
	if err := s.Available(); err != nil {
		return "", err
	}
	secret, err := keyring.Get(agentKeyringService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("assistant session is missing or has been revoked")
	}
	if err != nil {
		return "", fmt.Errorf("%w: %s (%v)", ErrSecureStorageUnavailable, s.Name(), err)
	}
	return secret, nil
}

func (s nativeSecretStore) Delete(account string) error {
	if err := s.Available(); err != nil {
		return err
	}
	err := keyring.Delete(agentKeyringService, account)
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%w: %s (%v)", ErrSecureStorageUnavailable, s.Name(), err)
}

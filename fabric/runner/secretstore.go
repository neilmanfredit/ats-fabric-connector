package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// SecretStore persists rotated Bullhorn tokens (build brief section 9.3.5).
type SecretStore interface {
	GetSecret(ctx context.Context, name string) (string, error)
	SetSecret(ctx context.Context, name, value string) error
}

// FileSecretStore stores secrets in one local JSON file, written atomically
// with owner-only permissions (build brief section 6.3.3).
type FileSecretStore struct {
	path string
}

func NewFileSecretStore(path string) *FileSecretStore {
	return &FileSecretStore{path: path}
}

func (s *FileSecretStore) load() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading secret store %s: %w", s.path, err)
	}
	m := map[string]string{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parsing secret store %s: %w", s.path, err)
		}
	}
	return m, nil
}

func (s *FileSecretStore) GetSecret(_ context.Context, name string) (string, error) {
	m, err := s.load()
	if err != nil {
		return "", err
	}
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("secret %q not found in %s", name, s.path)
	}
	return v, nil
}

func (s *FileSecretStore) SetSecret(_ context.Context, name, value string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	m[name] = value
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding secret store: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating secret store directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".secret-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp secret file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("setting temp secret file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing temp secret file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp secret file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming temp secret file into place: %w", err)
	}
	return nil
}

// KeyVaultSecretStore stores secrets in Azure Key Vault, authenticated with
// DefaultAzureCredential (matching the Entra identity described in the
// README prerequisites). It talks to the Key Vault REST API directly rather
// than pulling in the azsecrets SDK, to avoid an extra module dependency.
type KeyVaultSecretStore struct {
	vaultURL string
	cred     *azidentity.DefaultAzureCredential
	client   *http.Client
}

const keyVaultAPIVersion = "7.4"

func NewKeyVaultSecretStore(vaultURL string) (*KeyVaultSecretStore, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("creating Azure credential: %w", err)
	}
	return &KeyVaultSecretStore{
		vaultURL: strings.TrimSuffix(vaultURL, "/"),
		cred:     cred,
		client:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (s *KeyVaultSecretStore) token(ctx context.Context) (string, error) {
	tok, err := s.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://vault.azure.net/.default"}})
	if err != nil {
		return "", fmt.Errorf("acquiring Key Vault token: %w", err)
	}
	return tok.Token, nil
}

func (s *KeyVaultSecretStore) GetSecret(ctx context.Context, name string) (string, error) {
	tok, err := s.token(ctx)
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/secrets/%s?api-version=%s", s.vaultURL, name, keyVaultAPIVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching secret %q: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("fetching secret %q returned status %d: %s", name, resp.StatusCode, string(body))
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding secret %q response: %w", name, err)
	}
	return out.Value, nil
}

func (s *KeyVaultSecretStore) SetSecret(ctx context.Context, name, value string) error {
	tok, err := s.token(ctx)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/secrets/%s?api-version=%s", s.vaultURL, name, keyVaultAPIVersion)
	body, err := json.Marshal(struct {
		Value string `json:"value"`
	}{Value: value})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("setting secret %q: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("setting secret %q returned status %d: %s", name, resp.StatusCode, string(respBody))
	}
	return nil
}

func NewSecretStore(kind, fileStorePath, keyVaultURL string) (SecretStore, error) {
	switch kind {
	case "file":
		return NewFileSecretStore(fileStorePath), nil
	case "keyvault":
		return NewKeyVaultSecretStore(keyVaultURL)
	default:
		return nil, fmt.Errorf("unknown secret store %q", kind)
	}
}

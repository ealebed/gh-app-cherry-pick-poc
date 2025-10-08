package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"strings"
	"testing"
)

// mkTestPEM returns a valid RSA private key (PKCS#1) as PEM bytes.
func mkTestPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func TestLoad_Success_Base64(t *testing.T) {
	// Set ONLY the variables your loader requires.
	t.Setenv("GITHUB_APP_ID", "12345")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "s3cr3t")

	pemBytes := mkTestPEM(t)
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PEM_BASE64", base64.StdEncoding.EncodeToString(pemBytes))

	// Optional extras (if your loader consumes them, fine; if not, also fine)
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@noreply")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppID != 12345 {
		t.Fatalf("AppID = %d, want 12345", cfg.AppID)
	}
	if string(cfg.WebhookSecret) != "s3cr3t" {
		t.Fatalf("WebhookSecret = %q, want %q", string(cfg.WebhookSecret), "s3cr3t")
	}
	if !strings.Contains(string(cfg.PrivateKeyPEM), "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("PrivateKey not decoded from base64")
	}
}

func TestLoad_Error_MissingRequired(t *testing.T) {
	// Unset all required vars so Load must fail.
	for _, k := range []string{
		"GITHUB_APP_ID",
		"GITHUB_WEBHOOK_SECRET",
		"GITHUB_APP_PRIVATE_KEY_PEM_BASE64",
		"GIT_USER_NAME",
		"GIT_USER_EMAIL",
	} {
		os.Unsetenv(k)
	}
	_, err := Load()
	if err == nil {
		t.Fatalf("Load() = nil error, want non-nil for missing env")
	}
	// Optional: ensure the error mentions the required keys as your loader announces them
	msg := err.Error()
	wantSnips := []string{
		"GITHUB_APP_ID",
		"GITHUB_WEBHOOK_SECRET",
		"GITHUB_APP_PRIVATE_KEY_PEM_BASE64",
	}
	for _, s := range wantSnips {
		if !strings.Contains(msg, s) {
			t.Fatalf("expected error to mention %q; got: %s", s, msg)
		}
	}
}

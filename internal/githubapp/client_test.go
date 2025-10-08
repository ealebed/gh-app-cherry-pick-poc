package githubapp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// mkPEM returns a valid RSA private key in PKCS#1 PEM.
func mkPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024) // small & fast for tests
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func TestNewClients_InvalidPEM(t *testing.T) {
	invalid := []byte("not-a-private-key")
	// NOTE: order: appID, installationID, pem
	cli, err := NewClients(12345, 67890, invalid)
	if err == nil {
		t.Fatalf("expected error for invalid PEM, got nil (cli=%v)", cli)
	}
}

func TestNewClients_Success(t *testing.T) {
	pem := mkPEM(t)
	cli, err := NewClients(12345, 67890, pem)
	if err != nil {
		t.Fatalf("NewClients error = %v", err)
	}
	if cli == nil || cli.REST == nil || cli.HTTP == nil {
		t.Fatalf("expected non-nil clients/REST/HTTP, got %+v", cli)
	}
	// No network calls happen here; we just ensure construction works.
}

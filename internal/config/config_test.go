package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
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

func TestLoad_Success_WithDefaultsAndSQS(t *testing.T) {
	// Required GitHub bits
	t.Setenv("GITHUB_APP_ID", "12345")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "s3cr3t")

	pemBytes := mkTestPEM(t)
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PEM_BASE64", base64.StdEncoding.EncodeToString(pemBytes))

	// Required SQS bit
	t.Setenv("SQS_QUEUE_URL", "https://sqs.eu-north-1.amazonaws.com/123456789012/my-queue")

	// Optional overrides (ensure they are propagated if set)
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@noreply")

	// Leave AWS_REGION unset to exercise default "eu-north-1"
	// Leave SQS_* tuning vars unset to exercise their defaults

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Core
	if cfg.AppID != 12345 {
		t.Fatalf("AppID = %d, want 12345", cfg.AppID)
	}
	if string(cfg.WebhookSecret) != "s3cr3t" {
		t.Fatalf("WebhookSecret = %q, want %q", string(cfg.WebhookSecret), "s3cr3t")
	}
	if !strings.Contains(string(cfg.PrivateKeyPEM), "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("PrivateKey not decoded from base64")
	}
	if cfg.ListenPort != ":8080" {
		t.Fatalf("ListenPort = %q, want %q", cfg.ListenPort, ":8080")
	}
	if cfg.GitUserName != "bot" || cfg.GitUserEmail != "bot@noreply" {
		t.Fatalf("Git actor mismatch: %q / %q", cfg.GitUserName, cfg.GitUserEmail)
	}

	// AWS/SQS defaults
	if cfg.AWSRegion != "eu-north-1" {
		t.Fatalf("AWSRegion = %q, want %q", cfg.AWSRegion, "eu-north-1")
	}
	if cfg.SQSQueueURL != "https://sqs.eu-north-1.amazonaws.com/123456789012/my-queue" {
		t.Fatalf("SQSQueueURL mismatch: %q", cfg.SQSQueueURL)
	}
	if cfg.SQSMaxMessages != 10 {
		t.Fatalf("SQSMaxMessages = %d, want 10", cfg.SQSMaxMessages)
	}
	if cfg.SQSWaitTimeSeconds != 10 {
		t.Fatalf("SQSWaitTimeSeconds = %d, want 10", cfg.SQSWaitTimeSeconds)
	}
	if cfg.SQSVisibilityTimeout != 120 {
		t.Fatalf("SQSVisibilityTimeout = %d, want 120", cfg.SQSVisibilityTimeout)
	}
	if cfg.SQSDeleteOn4xx != true {
		t.Fatalf("SQSDeleteOn4xx = %v, want true", cfg.SQSDeleteOn4xx)
	}
	if cfg.SQSExtendOnProcessing != false {
		t.Fatalf("SQSExtendOnProcessing = %v, want false", cfg.SQSExtendOnProcessing)
	}
}

func TestLoad_ErrorsForMissingRequired(t *testing.T) {
	t.Run("missing GitHub required envs", func(t *testing.T) {
		// Unset everything relevant.
		for _, k := range []string{
			"GITHUB_APP_ID",
			"GITHUB_WEBHOOK_SECRET",
			"GITHUB_APP_PRIVATE_KEY_PEM_BASE64",
			"SQS_QUEUE_URL",
			"AWS_REGION",
			"GIT_USER_NAME",
			"GIT_USER_EMAIL",
		} {
			t.Setenv(k, "")
		}

		_, err := Load()
		if err == nil {
			t.Fatalf("Load() = nil error, want non-nil for missing GitHub envs")
		}
		// Ensure the error mentions the GitHub-required keys as your loader returns them
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
	})

	t.Run("missing SQS_QUEUE_URL", func(t *testing.T) {
		// Provide GitHub bits so we get to the SQS validation path
		t.Setenv("GITHUB_APP_ID", "1")
		t.Setenv("GITHUB_WEBHOOK_SECRET", "x")
		pemBytes := mkTestPEM(t)
		t.Setenv("GITHUB_APP_PRIVATE_KEY_PEM_BASE64", base64.StdEncoding.EncodeToString(pemBytes))

		// Ensure SQS_QUEUE_URL is empty
		t.Setenv("SQS_QUEUE_URL", "")

		_, err := Load()
		if err == nil {
			t.Fatalf("Load() = nil error, want non-nil for missing SQS_QUEUE_URL")
		}
		if !strings.Contains(err.Error(), "SQS_QUEUE_URL is required") {
			t.Fatalf("expected error to mention SQS_QUEUE_URL; got: %v", err)
		}
	})
}

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

func Test_envOr(t *testing.T) {
	t.Setenv("TEST_VAR", "test-value")
	t.Setenv("EMPTY_VAR", "")

	tests := []struct {
		name string
		key  string
		def  string
		want string
	}{
		{
			name: "env var set",
			key:  "TEST_VAR",
			def:  "default",
			want: "test-value",
		},
		{
			name: "env var not set",
			key:  "NOT_SET",
			def:  "default",
			want: "default",
		},
		{
			name: "env var empty",
			key:  "EMPTY_VAR",
			def:  "default",
			want: "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := envOr(tt.key, tt.def)
			if got != tt.want {
				t.Errorf("envOr(%q, %q) = %q, want %q", tt.key, tt.def, got, tt.want)
			}
		})
	}
}

func Test_envOrInt(t *testing.T) {
	t.Setenv("TEST_INT", "42")
	t.Setenv("TEST_INVALID", "not-a-number")
	t.Setenv("TEST_EMPTY", "")

	tests := []struct {
		name string
		key  string
		def  int
		want int
	}{
		{
			name: "valid integer",
			key:  "TEST_INT",
			def:  0,
			want: 42,
		},
		{
			name: "invalid integer",
			key:  "TEST_INVALID",
			def:  100,
			want: 100,
		},
		{
			name: "not set",
			key:  "NOT_SET",
			def:  200,
			want: 200,
		},
		{
			name: "empty string",
			key:  "TEST_EMPTY",
			def:  300,
			want: 300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := envOrInt(tt.key, tt.def)
			if got != tt.want {
				t.Errorf("envOrInt(%q, %d) = %d, want %d", tt.key, tt.def, got, tt.want)
			}
		})
	}
}

func Test_envOrBool(t *testing.T) {
	t.Setenv("TEST_TRUE1", "true")
	t.Setenv("TEST_TRUE2", "1")
	t.Setenv("TEST_TRUE3", "yes")
	t.Setenv("TEST_TRUE4", "y")
	t.Setenv("TEST_FALSE1", "false")
	t.Setenv("TEST_FALSE2", "0")
	t.Setenv("TEST_FALSE3", "no")
	t.Setenv("TEST_FALSE4", "n")
	t.Setenv("TEST_INVALID", "maybe")
	t.Setenv("TEST_EMPTY", "")

	tests := []struct {
		name string
		key  string
		def  bool
		want bool
	}{
		{
			name: "true string",
			key:  "TEST_TRUE1",
			def:  false,
			want: true,
		},
		{
			name: "1 as true",
			key:  "TEST_TRUE2",
			def:  false,
			want: true,
		},
		{
			name: "yes as true",
			key:  "TEST_TRUE3",
			def:  false,
			want: true,
		},
		{
			name: "y as true",
			key:  "TEST_TRUE4",
			def:  false,
			want: true,
		},
		{
			name: "false string",
			key:  "TEST_FALSE1",
			def:  true,
			want: false,
		},
		{
			name: "0 as false",
			key:  "TEST_FALSE2",
			def:  true,
			want: false,
		},
		{
			name: "no as false",
			key:  "TEST_FALSE3",
			def:  true,
			want: false,
		},
		{
			name: "n as false",
			key:  "TEST_FALSE4",
			def:  true,
			want: false,
		},
		{
			name: "invalid value",
			key:  "TEST_INVALID",
			def:  true,
			want: true,
		},
		{
			name: "not set",
			key:  "NOT_SET",
			def:  false,
			want: false,
		},
		{
			name: "empty string",
			key:  "TEST_EMPTY",
			def:  true,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := envOrBool(tt.key, tt.def)
			if got != tt.want {
				t.Errorf("envOrBool(%q, %v) = %v, want %v", tt.key, tt.def, got, tt.want)
			}
		})
	}
}

func Test_safeInt32(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int32
	}{
		{
			name:  "zero",
			input: 0,
			want:  0,
		},
		{
			name:  "positive value",
			input: 42,
			want:  42,
		},
		{
			name:  "negative value",
			input: -42,
			want:  -42,
		},
		{
			name:  "max int32",
			input: 2147483647,
			want:  2147483647,
		},
		{
			name:  "min int32",
			input: -2147483648,
			want:  -2147483648,
		},
		{
			name:  "overflow clamped to max",
			input: 2147483648,
			want:  2147483647,
		},
		{
			name:  "underflow clamped to min",
			input: -2147483649,
			want:  -2147483648,
		},
		{
			name:  "large overflow",
			input: 9999999999,
			want:  2147483647,
		},
		{
			name:  "large underflow",
			input: -9999999999,
			want:  -2147483648,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeInt32(tt.input)
			if got != tt.want {
				t.Errorf("safeInt32(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

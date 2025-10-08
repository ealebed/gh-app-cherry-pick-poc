package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
)

type Config struct {
	AppID         int64
	WebhookSecret []byte
	PrivateKeyPEM []byte // decoded PEM
	Port          string // ":8080"
	// Optional:
	GitUserName  string // "stabilisation-bot"
	GitUserEmail string // "stabilisation-bot@users.noreply.github.com"
}

func Load() (*Config, error) {
	appIDStr := os.Getenv("GITHUB_APP_ID")
	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	pemB64 := os.Getenv("GITHUB_APP_PRIVATE_KEY_PEM_BASE64")
	port := os.Getenv("PORT")
	if port == "" {
		port = ":8080"
	}

	if appIDStr == "" || secret == "" || pemB64 == "" {
		return nil, errors.New("GITHUB_APP_ID, GITHUB_WEBHOOK_SECRET, GITHUB_APP_PRIVATE_KEY_PEM_BASE64 are required")
	}
	var appID int64
	_, err := fmt.Sscan(appIDStr, &appID)
	if err != nil {
		return nil, err
	}

	pem, err := base64.StdEncoding.DecodeString(pemB64)
	if err != nil {
		return nil, err
	}

	return &Config{
		AppID:         appID,
		WebhookSecret: []byte(secret),
		PrivateKeyPEM: pem,
		Port:          port,
		GitUserName:   envOr("GIT_USER_NAME", "stabilisation-bot"),
		GitUserEmail:  envOr("GIT_USER_EMAIL", "stabilisation-bot@users.noreply.github.com"),
	}, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

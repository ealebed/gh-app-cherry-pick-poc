package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AppID         int64
	WebhookSecret []byte
	PrivateKeyPEM []byte // decoded PEM
	ListenPort    string // ":8080"

	// Optional Git actor
	GitUserName  string // "stabilisation-bot"
	GitUserEmail string // "stabilisation-bot@users.noreply.github.com"

	// AWS/SQS
	AWSRegion             string
	SQSQueueURL           string
	SQSMaxMessages        int32
	SQSWaitTimeSeconds    int32
	SQSVisibilityTimeout  int32
	SQSDeleteOn4xx        bool
	SQSExtendOnProcessing bool

	// Processing
	CherryTimeoutSeconds int // max time to process one merged PR (incl. git ops)
}

func Load() (*Config, error) {
	appIDStr := os.Getenv("GITHUB_APP_ID")
	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	pemB64 := os.Getenv("GITHUB_APP_PRIVATE_KEY_PEM_BASE64")
	listenPort := envOr("LISTEN_PORT", ":8080")

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

	// AWS/SQS defaults suitable for PoC
	awsRegion := envOr("AWS_REGION", "eu-north-1")
	queueURL := os.Getenv("SQS_QUEUE_URL")
	if queueURL == "" {
		return nil, errors.New("SQS_QUEUE_URL is required")
	}

	return &Config{
		AppID:         appID,
		WebhookSecret: []byte(secret),
		PrivateKeyPEM: pem,
		ListenPort:    listenPort,
		GitUserName:   envOr("GIT_USER_NAME", "stabilisation-bot"),
		GitUserEmail:  envOr("GIT_USER_EMAIL", "stabilisation-bot@users.noreply.github.com"),

		AWSRegion:             awsRegion,
		SQSQueueURL:           queueURL,
		SQSMaxMessages:        int32(envOrInt("SQS_MAX_MESSAGES", 10)),
		SQSWaitTimeSeconds:    int32(envOrInt("SQS_WAIT_TIME_SECONDS", 10)),
		SQSVisibilityTimeout:  int32(envOrInt("SQS_VISIBILITY_TIMEOUT", 120)),
		SQSDeleteOn4xx:        envOrBool("SQS_DELETE_ON_4XX", true),
		SQSExtendOnProcessing: envOrBool("SQS_EXTEND_ON_PROCESSING", false),

		// Give slow repos enough time; make it easy to override
		CherryTimeoutSeconds: envOrInt("CHERRY_TIMEOUT_SECONDS", 600),
	}, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envOrInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envOrBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "t", "yes", "y":
			return true
		case "0", "false", "f", "no", "n":
			return false
		}
	}
	return def
}

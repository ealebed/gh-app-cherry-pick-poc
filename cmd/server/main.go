package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/ealebed/gh-app-cherry-pick-poc/internal/config"
	"github.com/ealebed/gh-app-cherry-pick-poc/internal/ingest/sqs"
	"github.com/ealebed/gh-app-cherry-pick-poc/internal/processor"
)

func main() {
	_ = godotenv.Load() // ok if no .env

	// Structured JSON logs; control with LOG_LEVEL=debug|info|warn|error
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	// Build the GitHub processor.
	p := &processor.Processor{
		AppID:         cfg.AppID,
		PrivateKeyPEM: cfg.PrivateKeyPEM,
		WebhookSecret: cfg.WebhookSecret,
		GitUserName:   cfg.GitUserName,
		GitUserEmail:  cfg.GitUserEmail,
		// Make the per-PR processing timeout configurable.
		CherryTimeout: time.Duration(cfg.CherryTimeoutSeconds) * time.Second,
	}

	// AWS SDK v2 config + SQS client.
	awsCfg, err := awscfg.LoadDefaultConfig(context.Background(),
		awscfg.WithRegion(cfg.AWSRegion),
	)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}
	sqsClient := awssqs.NewFromConfig(awsCfg)

	// SQS worker wiring — note: we pass *processor.Processor which implements the Worker’s Handler interface.
	worker := &sqs.Worker{
		Client:            sqsClient,
		QueueURL:          cfg.SQSQueueURL,
		MaxMessages:       cfg.SQSMaxMessages,
		WaitTimeSeconds:   cfg.SQSWaitTimeSeconds,
		VisibilityTimeout: cfg.SQSVisibilityTimeout,
		DeleteOn4xx:       cfg.SQSDeleteOn4xx,
		Processor:         p,
	}

	// Health endpoint.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{
		Addr:              cfg.ListenPort,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Run worker until we get a shutdown signal.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("sqs.worker.exit", "err", err)
			_ = srv.Shutdown(context.Background())
		}
	}()

	// Handle SIGINT/SIGTERM for graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// Run HTTP (healthz) server.
	go func() {
		slog.Info("server.start", "addr", cfg.ListenPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server.error", "err", err)
			stop <- syscall.SIGTERM
		}
	}()

	<-stop
	slog.Info("shutdown.begin")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("server.shutdown.error", "err", err)
	}
	slog.Info("shutdown.complete")
}

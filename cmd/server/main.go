package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"

	"github.com/ealebed/gh-app-cherry-pick-poc/internal/config"
	"github.com/ealebed/gh-app-cherry-pick-poc/internal/webhook"
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

	srv := &webhook.Server{
		AppID:         cfg.AppID,
		PrivateKeyPEM: cfg.PrivateKeyPEM,
		WebhookSecret: cfg.WebhookSecret,
		GitUserName:   cfg.GitUserName,
		GitUserEmail:  cfg.GitUserEmail,
	}

	r := mux.NewRouter()
	r.Handle("/webhook", srv).Methods("POST")
	r.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }).Methods("GET")

	slog.Info("server.start", "addr", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, r))
}

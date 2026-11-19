// Command boai runs the podcast library server: same-origin API, SQLite
// persistence, a background feed refresher and the built web client.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"boai/internal/api"
	"boai/internal/config"
	"boai/internal/service"
	"boai/internal/store"
)

var version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		log.Fatalf("boai: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "boai ", log.LstdFlags)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	secret, secretSource, err := loadSecret(cfg.DataDir, cfg.Secret)
	if err != nil {
		return err
	}
	if secretSource == "file" {
		logger.Printf("AI key encryption uses %s", filepath.Join(cfg.DataDir, "ai-secret"))
	}
	dbPath := filepath.Join(cfg.DataDir, "boai.db")
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	svc := service.New(db, service.Options{
		UpdateInterval:  cfg.UpdateInterval,
		HTTPTimeout:     cfg.HTTPTimeout,
		MaxFeedBytes:    cfg.MaxFeedBytes,
		UserAgent:       "Boai/1.0 (+self-hosted podcast library)",
		AllowPrivate:    cfg.AllowPrivateNetwork,
		AllowFakeIP:     cfg.AllowFakeIPDNS,
		ITunesBase:      cfg.ITunesBase,
		Secret:          secret,
		AllowInsecureAI: cfg.AllowInsecureAI,
		DownloadDir:     filepath.Join(cfg.DataDir, "downloads"),
	})

	httpHandler := api.New(svc, api.Options{
		WebDir:          cfg.WebDir,
		UserHeader:      cfg.UserHeader,
		TrustUserHeader: cfg.TrustUserHeader,
		Version:         version,
		Logger:          logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go svc.Scheduler(ctx, logger.Printf)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpHandler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       90 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Printf("listening on %s (data=%s, web=%s, trusted-user-header=%t)",
			cfg.Addr, cfg.DataDir, cfg.WebDir, cfg.TrustUserHeader)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Printf("shutting down")
	case err := <-errCh:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// loadSecret uses BOAI_SECRET when supplied. Otherwise it creates a random
// per-data-directory secret with owner-only permissions. The value is never
// logged or returned by an API.
func loadSecret(dataDir, configured string) (string, string, error) {
	if strings.TrimSpace(configured) != "" {
		return configured, "environment", nil
	}
	path := filepath.Join(dataDir, "ai-secret")
	raw, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(raw)) != "" {
		return strings.TrimSpace(string(raw)), "file", nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("read AI secret: %w", err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate AI secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", "", fmt.Errorf("write AI secret: %w", err)
	}
	return secret, "file", nil
}

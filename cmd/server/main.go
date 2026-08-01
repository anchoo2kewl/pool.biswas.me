// Command server runs pool.biswas.me: the JSON API and the web application,
// backed by an embedded Turso database, in a single process.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/biswas-dev/pool/internal/api"
	"github.com/biswas-dev/pool/internal/auth"
	"github.com/biswas-dev/pool/internal/config"
	"github.com/biswas-dev/pool/internal/store"
	"github.com/biswas-dev/pool/web"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("pool ")

	cfg := config.Load()

	// The runtime image has no shell or wget, so the binary answers its own
	// container health check.
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck(cfg.Addr))
	}

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		log.Fatalf("create database directory: %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("create data directory: %v", err)
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	log.Printf("database ready at %s", cfg.DBPath)

	if err := seedAdmin(db, cfg); err != nil {
		log.Fatalf("seed admin: %v", err)
	}

	static, err := fs.Sub(web.Files, "static")
	if err != nil {
		log.Fatalf("mount web assets: %v", err)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(db, cfg, static).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// Uploads are capped at 25 MB and analysis calls can take a while, so
		// the write timeout has to accommodate both.
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Expired sessions accumulate otherwise; clearing them hourly is enough.
	go func() {
		for range time.Tick(time.Hour) {
			if err := db.PurgeExpiredSessions(); err != nil {
				log.Printf("purge sessions: %v", err)
			}
		}
	}()

	go func() {
		log.Printf("listening on %s (env=%s, app_url=%s, registration=%s)", cfg.Addr, cfg.Env, cfg.AppURL, cfg.Registration)
		if cfg.GitHubEnabled() {
			log.Print("GitHub sign-in enabled")
		}
		if cfg.GoogleEnabled() {
			log.Print("Google sign-in enabled")
		}
		if cfg.AIEnabled() {
			log.Printf("AI insights enabled (%s via %s)", cfg.AIModel, cfg.AIBaseURL)
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Print("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// healthcheck probes the running server and returns a process exit code.
func healthcheck(addr string) int {
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// seedAdmin creates the first account from the environment, so a fresh
// deployment is usable without a registration flow.
func seedAdmin(db *store.DB, cfg *config.Config) error {
	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		return nil
	}
	if _, err := db.UserByEmail(cfg.AdminEmail); err == nil {
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	if _, err := db.CreateUser(cfg.AdminEmail, "Admin", hash, "admin"); err != nil {
		return err
	}
	log.Printf("seeded admin account %s", cfg.AdminEmail)
	return nil
}

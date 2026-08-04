// Command gh-proxy is a private GitHub download accelerator meant to be mounted
// on a path of an existing site behind a reverse proxy.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prettyleaf/gh-proxy/internal/config"
	"github.com/prettyleaf/gh-proxy/internal/ghcli"
	"github.com/prettyleaf/gh-proxy/internal/proxy"
	"github.com/prettyleaf/gh-proxy/internal/server"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// The scratch-based image has no shell and no curl, so the binary probes
	// its own health endpoint for Docker's HEALTHCHECK.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, "gh-proxy: unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gh-proxy:", err)
		os.Exit(1)
	}
}

func healthcheck() error {
	addr := os.Getenv("GHP_ADMIN_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8900"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	// Set up before the credential is read, so that whatever the gh source
	// starts in the background is bound to the same shutdown signal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Resolved before anything listens: a gh CLI that is missing or logged out
	// is a configuration error, and refusing to start says so far more clearly
	// than 404ing every private-repo request afterwards.
	upstreamToken, err := upstreamCredential(ctx, cfg, log)
	if err != nil {
		return err
	}

	p := proxy.New(proxy.Options{
		RedirectHosts:         cfg.RedirectHosts,
		MaxRedirects:          cfg.MaxRedirects,
		SizeLimit:             cfg.SizeLimit,
		UpstreamToken:         cfg.UpstreamToken,
		UpstreamTokenFunc:     upstreamToken,
		CORS:                  cfg.CORS,
		LogTargets:            cfg.LogTargets,
		DialTimeout:           cfg.DialTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		Logger:                log,
	})

	public := &http.Server{
		Addr:    cfg.Listen,
		Handler: server.New(cfg, p, log),
		// No WriteTimeout on purpose: it is an absolute deadline on the whole
		// response, which would sever long release downloads and idle git
		// fetches. ReadHeaderTimeout covers the slowloris case instead.
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}

	admin := &http.Server{
		Addr:              cfg.AdminListen,
		Handler:           adminHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}

	log.Info("starting gh-proxy",
		"version", version,
		"listen", cfg.Listen,
		"admin_listen", cfg.AdminListen,
		"prefix", cfg.Prefix,
		"auth", authMode(cfg),
		"upstream", upstreamMode(cfg, upstreamToken != nil),
		"allow_list", cfg.AllowList.String(),
		"deny_list", cfg.DenyList.String(),
		"size_limit", cfg.SizeLimit,
	)
	if cfg.AllowAnonymous {
		log.Warn("running without authentication: anyone who can reach this listener can use it as a GitHub relay")
		if cfg.UpstreamToken != "" || upstreamToken != nil {
			log.Warn("an anonymous instance lends its GitHub credential to every caller: unset the upstream token or restrict who can reach the listener")
		}
	}

	errCh := make(chan error, 2)
	go serve(public, "public", errCh)
	go serve(admin, "admin", errCh)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = admin.Shutdown(shutdownCtx)
	return public.Shutdown(shutdownCtx)
}

func serve(s *http.Server, name string, errCh chan<- error) {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("%s listener on %s: %w", name, s.Addr, err)
	}
}

// adminHandler serves the health check on a listener of its own. Putting it
// under the public mount prefix would hand an unauthenticated prober a reliable
// way to confirm the service exists.
func adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "ok %s\n", version)
	})
	return mux
}

// upstreamCredential resolves the credential presented to GitHub. It returns
// nil when the token is a fixed string from the environment, leaving the proxy
// on its static field; a non-nil func means the value can change while the
// process runs.
func upstreamCredential(ctx context.Context, cfg *config.Config, log *slog.Logger) (func() string, error) {
	if cfg.UpstreamSource != config.UpstreamSourceGH {
		return nil, nil
	}
	src := ghcli.New(ghcli.Options{
		Bin:       cfg.GHBin,
		Host:      cfg.GHHost,
		ConfigDir: cfg.GHConfigDir,
		Refresh:   cfg.GHRefresh,
		Logger:    log,
	})
	if err := src.Load(ctx); err != nil {
		return nil, fmt.Errorf("GHP_UPSTREAM_TOKEN_SOURCE=gh: %w; run `gh auth login`, or set GHP_UPSTREAM_TOKEN instead", err)
	}
	go src.Refresh(ctx)
	return src.Token, nil
}

func authMode(cfg *config.Config) string {
	if cfg.AllowAnonymous {
		return "anonymous"
	}
	return "token"
}

func upstreamMode(cfg *config.Config, fromGH bool) string {
	switch {
	case fromGH:
		return "gh-cli"
	case cfg.UpstreamToken != "":
		return "token"
	default:
		return "none"
	}
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

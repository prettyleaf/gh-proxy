// Package ghcli sources the credential the proxy presents to GitHub from an
// already authenticated `gh` CLI, so an instance that only needs private-repo
// access does not require a PAT minted by hand and pasted into the environment.
//
// Two lookups, in this order:
//
//	gh auth token --hostname <host>   whenever the binary is reachable
//	<config dir>/hosts.yml            when it is not
//
// The file fallback is not a convenience: the production image is built on
// scratch and has no shell, no gh and nothing to exec. Mounting ~/.config/gh
// read-only is the only way the credential reaches that container without being
// copied into the environment, which is the whole point of the feature.
//
// The credential is cached and re-read on an interval. gh rotates OAuth tokens
// on its own schedule, and a proxy that read the token once at startup would
// answer 404 for every private repository some hours later with no hint why.
package ghcli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultBin     = "gh"
	defaultHost    = "github.com"
	defaultTimeout = 5 * time.Second
)

// Options configures a Source.
type Options struct {
	// Bin is the gh binary, resolved through PATH when it is a bare name.
	Bin string
	// Host is the GitHub host whose credential is wanted.
	Host string
	// ConfigDir is gh's configuration directory, used for the hosts.yml
	// fallback and passed to the binary so both lookups agree.
	ConfigDir string
	// Refresh is how often the credential is re-read; 0 disables re-reading.
	Refresh time.Duration
	// Timeout caps a single gh invocation.
	Timeout time.Duration

	Logger *slog.Logger
}

// Source holds the credential read from gh.
type Source struct {
	opts Options

	mu    sync.RWMutex
	token string
}

// New returns a Source. Nothing is read until Load runs.
func New(opts Options) *Source {
	if opts.Bin == "" {
		opts.Bin = defaultBin
	}
	if opts.Host == "" {
		opts.Host = defaultHost
	}
	if opts.ConfigDir == "" {
		opts.ConfigDir = DefaultConfigDir()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Source{opts: opts}
}

// DefaultConfigDir mirrors gh's own resolution order for its config directory.
func DefaultConfigDir() string {
	if d := os.Getenv("GH_CONFIG_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "gh")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "gh")
	}
	return ""
}

// Load reads the credential once and caches it.
//
// This is the startup probe: an unauthenticated or absent gh is a configuration
// error, and failing here is far kinder than starting up and 404ing every
// private-repo request afterwards.
func (s *Source) Load(ctx context.Context) error {
	token, via, err := s.fetch(ctx)
	if err != nil {
		return err
	}
	s.store(token)
	s.opts.Logger.Info("upstream credential taken from gh",
		"via", via, "host", s.opts.Host, "kind", kind(token))
	return nil
}

// Token returns the cached credential. It never blocks on gh, because it runs
// on the request path.
func (s *Source) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// Refresh re-reads the credential until ctx is done. Run it in a goroutine.
func (s *Source) Refresh(ctx context.Context) {
	if s.opts.Refresh <= 0 {
		return
	}
	t := time.NewTicker(s.opts.Refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			token, _, err := s.fetch(ctx)
			if err != nil {
				// Keep serving with the credential already in hand: a transient
				// gh failure must not silently downgrade the proxy to anonymous.
				s.opts.Logger.Warn("re-reading the gh credential failed, keeping the previous one", "error", err)
				continue
			}
			if s.store(token) {
				s.opts.Logger.Info("gh credential changed, now using the new one", "kind", kind(token))
			}
		}
	}
}

// store caches a credential and reports whether it differed from the old one.
func (s *Source) store(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.token != token
	s.token = token
	return changed
}

// fetch resolves the credential, preferring the binary. via names the source it
// came from, for the log line.
func (s *Source) fetch(ctx context.Context) (token, via string, err error) {
	bin, lookErr := exec.LookPath(s.opts.Bin)
	if lookErr == nil {
		token, err = s.fromBinary(ctx, bin)
		if err != nil {
			return "", "", err
		}
		return token, bin, nil
	}

	path := s.hostsPath()
	token, fileErr := s.fromHostsFile(path)
	if fileErr != nil {
		return "", "", fmt.Errorf("%s is not runnable (%v) and %s did not yield a credential: %w",
			s.opts.Bin, lookErr, path, fileErr)
	}
	return token, path, nil
}

func (s *Source) fromBinary(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "auth", "token", "--hostname", s.opts.Host)
	// No stdin: gh must fail loudly rather than wait on a prompt for a terminal
	// this process does not have.
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "GH_NO_UPDATE_NOTIFIER=1")
	if s.opts.ConfigDir != "" {
		cmd.Env = append(cmd.Env, "GH_CONFIG_DIR="+s.opts.ConfigDir)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		detail := firstLine(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("gh auth token --hostname %s: %s", s.opts.Host, detail)
	}
	return clean(stdout.String())
}

func (s *Source) hostsPath() string {
	if s.opts.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.opts.ConfigDir, "hosts.yml")
}

// fromHostsFile reads the token gh stores for the configured host.
//
// Hand-rolled rather than pulled from a YAML library on purpose: this project
// has no third-party modules, and the shape here is two levels of a map with a
// known key. Anything gh writes that this cannot parse is better reported as
// "no credential" than papered over.
func (s *Source) fromHostsFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("no gh config directory; set GHP_GH_CONFIG_DIR")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	inHost := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// A line at column zero opens a host block: "github.com:".
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inHost = strings.EqualFold(strings.TrimSuffix(trimmed, ":"), s.opts.Host)
			continue
		}
		if !inHost {
			continue
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found || strings.TrimSpace(key) != "oauth_token" {
			continue
		}
		// gh 2.x repeats the key under users.<login>; both copies hold the same
		// credential, so the first hit at any depth is the right one.
		return clean(strings.Trim(strings.TrimSpace(value), `"'`))
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no oauth_token for %s (gh may be keeping it in the system keyring, "+
		"which only the gh binary itself can read)", s.opts.Host)
}

// clean validates what gh handed back. The value goes straight into an
// Authorization header, so anything that cannot legally appear in one is a
// failure here rather than a mangled request later.
func clean(raw string) (string, error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", errors.New("gh returned an empty token")
	}
	if strings.ContainsFunc(token, func(r rune) bool { return r < '!' || r > '~' }) {
		return "", errors.New("token contains characters that cannot appear in an Authorization header")
	}
	return token, nil
}

// kind is the token's type prefix (gho, ghp, ghu, ...), which is safe to log.
// The rest of it never reaches a log line.
func kind(token string) string {
	if prefix, _, found := strings.Cut(token, "_"); found && prefix != "" {
		return prefix
	}
	return "opaque"
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// Package config loads the proxy's settings from the environment.
//
// Every setting is an environment variable so the service configures cleanly
// from a compose file. Secrets additionally accept a <NAME>_FILE form pointing
// at a file, which is how Docker and systemd hand over credentials without
// putting them in `docker inspect` output or the process environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prettyleaf/gh-proxy/internal/ghurl"
)

// Config is the fully validated runtime configuration.
type Config struct {
	Listen      string // public listener, reverse-proxied by nginx
	AdminListen string // health endpoint; never exposed publicly
	Prefix      string // mount path, always "/...../" with both slashes

	Token          string
	AllowAnonymous bool

	AllowList ghurl.RuleSet // empty means no restriction
	DenyList  ghurl.RuleSet

	UpstreamToken string // GitHub PAT used towards GitHub, if any
	RedirectHosts map[string]bool
	MaxRedirects  int
	SizeLimit     int64 // 0 disables the limit

	CORS       bool
	LogTargets bool
	LogLevel   string

	ReadHeaderTimeout     time.Duration
	IdleTimeout           time.Duration
	DialTimeout           time.Duration
	ResponseHeaderTimeout time.Duration
	ShutdownTimeout       time.Duration
}

// DefaultRedirectHosts are the hosts a GitHub download may legitimately bounce
// to. Following these server-side is the whole point of the proxy: a client that
// cannot reach github.com cannot reach objects.githubusercontent.com either, so
// handing the redirect back would defeat the purpose.
var DefaultRedirectHosts = []string{
	"github.com",
	"codeload.github.com",
	"objects.githubusercontent.com",
	"objects-origin.githubusercontent.com",
	"release-assets.githubusercontent.com",
	"github-releases.githubusercontent.com",
	"github-registry-files.githubusercontent.com",
	"raw.githubusercontent.com",
	"gist.githubusercontent.com",
	"media.githubusercontent.com",
	"user-images.githubusercontent.com",
	"avatars.githubusercontent.com",
}

// Load reads and validates the configuration from the process environment.
func Load() (*Config, error) {
	c := &Config{
		Listen:                env("GHP_LISTEN", "0.0.0.0:8899"),
		AdminListen:           env("GHP_ADMIN_LISTEN", "127.0.0.1:8900"),
		CORS:                  envBool("GHP_CORS", false),
		LogTargets:            envBool("GHP_LOG_TARGETS", false),
		LogLevel:              env("GHP_LOG_LEVEL", "info"),
		ReadHeaderTimeout:     envDuration("GHP_READ_HEADER_TIMEOUT", 15*time.Second),
		IdleTimeout:           envDuration("GHP_IDLE_TIMEOUT", 120*time.Second),
		DialTimeout:           envDuration("GHP_DIAL_TIMEOUT", 10*time.Second),
		ResponseHeaderTimeout: envDuration("GHP_RESPONSE_HEADER_TIMEOUT", 30*time.Second),
		ShutdownTimeout:       envDuration("GHP_SHUTDOWN_TIMEOUT", 30*time.Second),
	}

	var err error
	if c.Prefix, err = normalizePrefix(env("GHP_PREFIX", "/")); err != nil {
		return nil, err
	}
	if c.Token, err = secret("GHP_TOKEN"); err != nil {
		return nil, err
	}
	if c.UpstreamToken, err = secret("GHP_UPSTREAM_TOKEN"); err != nil {
		return nil, err
	}
	c.AllowAnonymous = envBool("GHP_ALLOW_ANONYMOUS", false)

	// Refusing to start beats silently becoming an open relay for the internet.
	switch {
	case c.Token == "" && !c.AllowAnonymous:
		return nil, fmt.Errorf("GHP_TOKEN (or GHP_TOKEN_FILE) is required; set GHP_ALLOW_ANONYMOUS=1 to run without authentication")
	case c.Token != "" && c.AllowAnonymous:
		return nil, fmt.Errorf("GHP_ALLOW_ANONYMOUS=1 conflicts with a configured GHP_TOKEN")
	case c.Token != "" && len(c.Token) < 16:
		return nil, fmt.Errorf("GHP_TOKEN is %d characters; use at least 16 (try: openssl rand -hex 24)", len(c.Token))
	case strings.ContainsAny(c.Token, "/?# "):
		return nil, fmt.Errorf("GHP_TOKEN must not contain '/', '?', '#' or spaces: it is carried as a single URL path segment")
	}

	if c.AllowList, err = rules("GHP_ALLOW_LIST"); err != nil {
		return nil, err
	}
	if c.DenyList, err = rules("GHP_DENY_LIST"); err != nil {
		return nil, err
	}

	if c.MaxRedirects, err = envInt("GHP_MAX_REDIRECTS", 5); err != nil {
		return nil, err
	}
	if c.MaxRedirects < 0 {
		return nil, fmt.Errorf("GHP_MAX_REDIRECTS must not be negative")
	}
	if c.SizeLimit, err = envBytes("GHP_SIZE_LIMIT", 0); err != nil {
		return nil, err
	}

	c.RedirectHosts = map[string]bool{}
	hosts := DefaultRedirectHosts
	if v := os.Getenv("GHP_REDIRECT_HOSTS"); strings.TrimSpace(v) != "" {
		hosts = splitList(v)
	}
	for _, h := range hosts {
		c.RedirectHosts[strings.ToLower(h)] = true
	}

	return c, nil
}

// normalizePrefix forces the mount path into the "/x/y/" shape the request
// handler expects, so that the value here and the nginx `location` agree no
// matter how the operator wrote it.
func normalizePrefix(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/"
	}
	if strings.ContainsAny(p, "?# ") {
		return "", fmt.Errorf("GHP_PREFIX must be a plain path, got %q", p)
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	if strings.Contains(p, "//") {
		return "", fmt.Errorf("GHP_PREFIX must not contain empty segments, got %q", p)
	}
	return p, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// secret reads <key>_FILE in preference to <key>.
func secret(key string) (string, error) {
	if path := os.Getenv(key + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", key, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(os.Getenv(key)), nil
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// envBytes accepts a plain byte count or a KB/MB/GB suffix.
func envBytes(key string, def int64) (int64, error) {
	v := strings.TrimSpace(strings.ToUpper(os.Getenv(key)))
	if v == "" {
		return def, nil
	}
	// Ordered longest-first so "MB" is matched before "B".
	suffixes := []struct {
		s string
		m int64
	}{
		{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10},
		{"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1},
	}
	mult := int64(1)
	for _, s := range suffixes {
		if strings.HasSuffix(v, s.s) {
			v, mult = strings.TrimSuffix(v, s.s), s.m
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must not be negative", key)
	}
	return n * mult, nil
}

func rules(key string) (ghurl.RuleSet, error) {
	rs, err := ghurl.ParseRules(os.Getenv(key))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return rs, nil
}

func splitList(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == '\n' || r == ' ' || r == '\t' || r == '\r'
	})
}

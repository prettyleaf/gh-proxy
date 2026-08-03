package config

import (
	"os"
	"path/filepath"
	"testing"
)

const goodToken = "0123456789abcdef0123"

func TestNormalizePrefix(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "/", false},
		{"/", "/", false},
		{"ivanghproxy", "/ivanghproxy/", false},
		{"/ivanghproxy", "/ivanghproxy/", false},
		{"ivanghproxy/", "/ivanghproxy/", false},
		{"/ivanghproxy/", "/ivanghproxy/", false},
		{"/a/b/", "/a/b/", false},
		{"/a//b/", "", true},
		{"/a?b", "", true},
		{"/a b", "", true},
	}
	for _, tc := range tests {
		got, err := normalizePrefix(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizePrefix(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizePrefix(%q): %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("normalizePrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnvBytes(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"1024", 1024, false},
		{"1KB", 1 << 10, false},
		{"5MB", 5 << 20, false},
		{"2GB", 2 << 30, false},
		{"3G", 3 << 30, false},
		{"512B", 512, false},
		{" 8mb ", 8 << 20, false},
		{"-1", 0, true},
		{"lots", 0, true},
	}
	for _, tc := range tests {
		t.Setenv("GHP_TEST_BYTES", tc.in)
		got, err := envBytes("GHP_TEST_BYTES", 0)
		if tc.wantErr {
			if err == nil {
				t.Errorf("envBytes(%q) = %d, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("envBytes(%q): %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("envBytes(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestLoadRequiresATokenByDefault(t *testing.T) {
	clearEnv(t)
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with no token; it must refuse to become an open relay")
	}
}

func TestLoadRejectsWeakOrUnusableTokens(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"too short", "short"},
		{"contains a slash", "abcdefghijklmnop/qrst"},
		{"contains a question mark", "abcdefghijklmnop?qrs"},
		{"contains a space", "abcdefghijklmnop qrs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("GHP_TOKEN", tc.token)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted token %q", tc.token)
			}
		})
	}
}

func TestLoadRejectsAnonymousTogetherWithToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("GHP_TOKEN", goodToken)
	t.Setenv("GHP_ALLOW_ANONYMOUS", "1")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted a contradictory anonymous+token configuration")
	}
}

func TestLoadAllowsExplicitAnonymous(t *testing.T) {
	clearEnv(t)
	t.Setenv("GHP_ALLOW_ANONYMOUS", "1")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AllowAnonymous {
		t.Error("AllowAnonymous = false, want true")
	}
}

func TestLoadReadsTokenFromFile(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(goodToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHP_TOKEN_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != goodToken {
		t.Errorf("Token = %q, want %q (trailing newline should be trimmed)", cfg.Token, goodToken)
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("GHP_TOKEN", goodToken)
	t.Setenv("GHP_PREFIX", "ivanghproxy")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Prefix != "/ivanghproxy/" {
		t.Errorf("Prefix = %q, want %q", cfg.Prefix, "/ivanghproxy/")
	}
	if cfg.SizeLimit != 0 {
		t.Errorf("SizeLimit = %d, want 0 (unlimited)", cfg.SizeLimit)
	}
	if cfg.MaxRedirects != 5 {
		t.Errorf("MaxRedirects = %d, want 5", cfg.MaxRedirects)
	}
	if cfg.CORS || cfg.LogTargets {
		t.Error("CORS and LogTargets must default to off")
	}
	if !cfg.RedirectHosts["objects.githubusercontent.com"] {
		t.Error("default redirect hosts must include the release asset backend")
	}
	if cfg.RedirectHosts["example.com"] {
		t.Error("default redirect hosts must not include arbitrary hosts")
	}
}

func TestLoadCustomRedirectHostsReplaceDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("GHP_TOKEN", goodToken)
	t.Setenv("GHP_REDIRECT_HOSTS", "codeload.github.com, Objects.GitHubUserContent.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.RedirectHosts["objects.githubusercontent.com"] {
		t.Error("host list should be lowercased")
	}
	if cfg.RedirectHosts["media.githubusercontent.com"] {
		t.Error("an explicit list must replace the defaults, not extend them")
	}
}

func TestLoadParsesAccessLists(t *testing.T) {
	clearEnv(t)
	t.Setenv("GHP_TOKEN", goodToken)
	t.Setenv("GHP_ALLOW_LIST", "octocat, someone/thing")
	t.Setenv("GHP_DENY_LIST", "*/secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AllowList.Match("octocat", "anything") {
		t.Error("allow list should cover every repo of octocat")
	}
	if !cfg.DenyList.Match("anyone", "secret") {
		t.Error("deny list should cover */secret")
	}
}

func TestLoadParsesDefaultHosts(t *testing.T) {
	clearEnv(t)
	t.Setenv("GHP_TOKEN", goodToken)
	t.Setenv("GHP_DEFAULT_HOST", "GitHub.com, raw.githubusercontent.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"github.com", "raw.githubusercontent.com"}
	if len(cfg.DefaultHosts) != len(want) {
		t.Fatalf("DefaultHosts = %v, want %v", cfg.DefaultHosts, want)
	}
	for i, h := range want {
		if cfg.DefaultHosts[i] != h {
			t.Errorf("DefaultHosts[%d] = %q, want %q (order decides which host wins)", i, cfg.DefaultHosts[i], h)
		}
	}
}

func TestLoadRejectsUnproxyableDefaultHost(t *testing.T) {
	for _, h := range []string{"gitlab.com", "api.github.com", "127.0.0.1", "github.com.evil.example"} {
		t.Run(h, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("GHP_TOKEN", goodToken)
			t.Setenv("GHP_DEFAULT_HOST", h)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted GHP_DEFAULT_HOST=%q", h)
			}
		})
	}
}

func TestLoadLeavesDefaultHostsUnsetByDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("GHP_TOKEN", goodToken)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.DefaultHosts) != 0 {
		t.Errorf("DefaultHosts = %v, want empty: the short form is opt-in", cfg.DefaultHosts)
	}
}

// clearEnv removes every GHP_ variable so a test starts from a known state.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GHP_LISTEN", "GHP_ADMIN_LISTEN", "GHP_PREFIX", "GHP_TOKEN", "GHP_TOKEN_FILE",
		"GHP_ALLOW_ANONYMOUS", "GHP_ALLOW_LIST", "GHP_DENY_LIST", "GHP_UPSTREAM_TOKEN",
		"GHP_UPSTREAM_TOKEN_FILE", "GHP_REDIRECT_HOSTS", "GHP_MAX_REDIRECTS",
		"GHP_SIZE_LIMIT", "GHP_CORS", "GHP_LOG_TARGETS", "GHP_LOG_LEVEL",
		"GHP_DEFAULT_HOST",
	} {
		t.Setenv(k, "")
	}
}

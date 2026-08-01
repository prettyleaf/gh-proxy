package ghurl

import "testing"

func TestParseAccepts(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		kind    Kind
		owner   string
		repo    string
		wantURL string
	}{
		{
			name:    "release asset",
			in:      "https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz",
			kind:    KindRelease,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz",
		},
		{
			name:    "branch archive",
			in:      "https://github.com/cli/cli/archive/refs/heads/trunk.zip",
			kind:    KindRelease,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/archive/refs/heads/trunk.zip",
		},
		{
			name:    "blob is rewritten to raw",
			in:      "https://github.com/cli/cli/blob/trunk/README.md",
			kind:    KindBlob,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/raw/trunk/README.md",
		},
		{
			name:    "raw stays raw",
			in:      "https://github.com/cli/cli/raw/trunk/README.md",
			kind:    KindBlob,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/raw/trunk/README.md",
		},
		{
			name:    "git info refs",
			in:      "https://github.com/cli/cli/info/refs?service=git-upload-pack",
			kind:    KindGit,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/info/refs?service=git-upload-pack",
		},
		{
			name:    "git upload pack on a .git repo",
			in:      "https://github.com/cli/cli.git/git-upload-pack",
			kind:    KindGit,
			owner:   "cli",
			repo:    "cli.git",
			wantURL: "https://github.com/cli/cli.git/git-upload-pack",
		},
		{
			name:    "tags",
			in:      "https://github.com/cli/cli/tags",
			kind:    KindTags,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/tags",
		},
		{
			name:    "raw host",
			in:      "https://raw.githubusercontent.com/cli/cli/trunk/README.md",
			kind:    KindRaw,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://raw.githubusercontent.com/cli/cli/trunk/README.md",
		},
		{
			name:    "gist",
			in:      "https://gist.githubusercontent.com/octocat/6cad326836d38bd3a7ae/raw/file.txt",
			kind:    KindGist,
			owner:   "octocat",
			repo:    "",
			wantURL: "https://gist.githubusercontent.com/octocat/6cad326836d38bd3a7ae/raw/file.txt",
		},
		{
			name:    "scheme collapsed by a normalizing front end",
			in:      "https:/github.com/cli/cli/releases/download/v1/f.zip",
			kind:    KindRelease,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/releases/download/v1/f.zip",
		},
		{
			name:    "no scheme at all",
			in:      "github.com/cli/cli/releases/download/v1/f.zip",
			kind:    KindRelease,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/releases/download/v1/f.zip",
		},
		{
			name:    "http is upgraded to https",
			in:      "http://github.com/cli/cli/releases/download/v1/f.zip",
			kind:    KindRelease,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/releases/download/v1/f.zip",
		},
		{
			name:    "www is normalized away",
			in:      "https://www.github.com/cli/cli/releases/download/v1/f.zip",
			kind:    KindRelease,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/releases/download/v1/f.zip",
		},
		{
			name:    "percent encoding is preserved",
			in:      "https://github.com/cli/cli/releases/download/v1/my%20file%2Bx.zip",
			kind:    KindRelease,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/releases/download/v1/my%20file%2Bx.zip",
		},
		{
			name:    "credentials embedded in the target are dropped",
			in:      "https://someuser:hunter2@github.com/cli/cli/releases/download/v1/f.zip",
			kind:    KindRelease,
			owner:   "cli",
			repo:    "cli",
			wantURL: "https://github.com/cli/cli/releases/download/v1/f.zip",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("Parse(%q) returned error %v, want a match", tc.in, err)
			}
			if got.Kind != tc.kind {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.kind)
			}
			if got.Owner != tc.owner {
				t.Errorf("Owner = %q, want %q", got.Owner, tc.owner)
			}
			if got.Repo != tc.repo {
				t.Errorf("Repo = %q, want %q", got.Repo, tc.repo)
			}
			if u := got.URL.String(); u != tc.wantURL {
				t.Errorf("URL = %q, want %q", u, tc.wantURL)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	// Everything here must be refused: the matcher is the anti-SSRF boundary,
	// so anything it lets through is a host this service will connect to.
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"other forge", "https://gitlab.com/cli/cli/releases/download/v1/f.zip"},
		{"suffix lookalike host", "https://github.com.evil.example/cli/cli/releases/download/v1/f.zip"},
		{"prefix lookalike host", "https://evilgithub.com/cli/cli/releases/download/v1/f.zip"},
		{"subdomain lookalike", "https://github.com.evil.example/x"},
		{"internal address", "http://169.254.169.254/latest/meta-data/"},
		{"loopback", "http://127.0.0.1:8899/admin"},
		{"file scheme", "file:///etc/passwd"},
		{"github api", "https://api.github.com/user"},
		{"github html page", "https://github.com/cli/cli"},
		{"owner only", "https://github.com/cli"},
		{"unsupported github path", "https://github.com/cli/cli/issues/1"},
		{"raw without ref path", "https://raw.githubusercontent.com/cli/cli"},
		{"raw with empty file", "https://raw.githubusercontent.com/cli/cli/trunk/"},
		{"gist without file", "https://gist.githubusercontent.com/octocat"},
		{"path traversal into another host", "https://github.com/../../etc/passwd"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Parse(tc.in); err == nil {
				t.Fatalf("Parse(%q) accepted %s %s/%s (%s), want rejection",
					tc.in, got.Kind, got.Owner, got.Repo, got.URL)
			}
		})
	}
}

func TestRepairScheme(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/a/b", "https://github.com/a/b"},
		{"https:/github.com/a/b", "https://github.com/a/b"},
		{"https:github.com/a/b", "https://github.com/a/b"},
		{"http:/github.com/a/b", "http://github.com/a/b"},
		{"github.com/a/b", "https://github.com/a/b"},
		{"HTTPS://github.com/a/b", "HTTPS://github.com/a/b"},
		{"https%3A%2F%2Fgithub.com/a/b", "https://github.com/a/b"},
		{"  https://github.com/a/b  ", "https://github.com/a/b"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := RepairScheme(tc.in); got != tc.want {
			t.Errorf("RepairScheme(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseIsCaseInsensitiveOnHost(t *testing.T) {
	got, err := Parse("https://GitHub.COM/cli/cli/releases/download/v1/f.zip")
	if err != nil {
		t.Fatalf("Parse returned %v, want a match", err)
	}
	if want := "https://github.com/cli/cli/releases/download/v1/f.zip"; got.URL.String() != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
}

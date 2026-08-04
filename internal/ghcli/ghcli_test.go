package ghcli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeGH writes a stand-in for the CLI. Anything the real gh does that matters
// here — printing a token, or failing with a message on stderr — is a two-line
// shell script.
func fakeGH(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	path := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTokenFromBinary(t *testing.T) {
	// The arguments matter: without --hostname, gh answers for whichever host
	// happens to be first in its config.
	bin := fakeGH(t, `echo "args:$*" >&2; echo gho_frombinary`)
	s := New(Options{Bin: bin, ConfigDir: t.TempDir(), Logger: discardLogger()})

	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Token(); got != "gho_frombinary" {
		t.Errorf("Token() = %q, want %q", got, "gho_frombinary")
	}
}

func TestBinaryFailureCarriesGhsOwnMessage(t *testing.T) {
	bin := fakeGH(t, `echo "no oauth token found for github.com" >&2; exit 1`)
	s := New(Options{Bin: bin, ConfigDir: t.TempDir(), Logger: discardLogger()})

	err := s.Load(context.Background())
	if err == nil {
		t.Fatal("Load succeeded against a logged-out gh")
	}
	if !strings.Contains(err.Error(), "no oauth token found") {
		t.Errorf("error = %v; it must repeat what gh said, or the operator cannot act on it", err)
	}
}

func TestEmptyTokenIsAFailure(t *testing.T) {
	// A silent success with no credential would turn every private-repo request
	// into an unexplained 404.
	bin := fakeGH(t, `echo ""`)
	s := New(Options{Bin: bin, ConfigDir: t.TempDir(), Logger: discardLogger()})

	if err := s.Load(context.Background()); err == nil {
		t.Fatal("Load accepted an empty token")
	}
}

func writeHosts(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// missingBin is a name no PATH lookup can resolve, which forces the file
// fallback — the only path available inside the scratch image.
const missingBin = "gh-does-not-exist-9d3f"

func TestTokenFromHostsFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "flat",
			content: "github.com:\n" +
				"    oauth_token: gho_flat\n" +
				"    user: octocat\n",
			want: "gho_flat",
		},
		{
			name: "nested under users, as gh 2.x writes it",
			content: "github.com:\n" +
				"    users:\n" +
				"        octocat:\n" +
				"            oauth_token: gho_nested\n" +
				"    git_protocol: https\n",
			want: "gho_nested",
		},
		{
			name:    "quoted",
			content: "github.com:\n    oauth_token: \"gho_quoted\"\n",
			want:    "gho_quoted",
		},
		{
			name: "the wrong host's token is not borrowed",
			content: "ghe.example.com:\n" +
				"    oauth_token: gho_enterprise\n" +
				"github.com:\n" +
				"    oauth_token: gho_dotcom\n",
			want: "gho_dotcom",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(Options{Bin: missingBin, ConfigDir: writeHosts(t, tc.content), Logger: discardLogger()})
			if err := s.Load(context.Background()); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := s.Token(); got != tc.want {
				t.Errorf("Token() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHostsFileWithoutTheHostFails(t *testing.T) {
	dir := writeHosts(t, "ghe.example.com:\n    oauth_token: gho_enterprise\n")
	s := New(Options{Bin: missingBin, ConfigDir: dir, Logger: discardLogger()})

	if err := s.Load(context.Background()); err == nil {
		t.Fatal("Load succeeded with no entry for github.com")
	}
}

func TestKeyringOnlyFileIsReportedAsSuch(t *testing.T) {
	// gh stores nothing but the account name when the credential lives in the
	// system keyring. The error has to name that case: the file is present and
	// looks fine, so "not found" alone would send the operator hunting.
	dir := writeHosts(t, "github.com:\n    user: octocat\n    git_protocol: https\n")
	s := New(Options{Bin: missingBin, ConfigDir: dir, Logger: discardLogger()})

	err := s.Load(context.Background())
	if err == nil {
		t.Fatal("Load succeeded with a hosts.yml that holds no token")
	}
	if !strings.Contains(err.Error(), "keyring") {
		t.Errorf("error = %v, want a mention of the keyring", err)
	}
}

func TestBinaryWinsOverTheFile(t *testing.T) {
	bin := fakeGH(t, `echo gho_frombinary`)
	dir := writeHosts(t, "github.com:\n    oauth_token: gho_fromfile\n")
	s := New(Options{Bin: bin, ConfigDir: dir, Logger: discardLogger()})

	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The binary can read a keyring the file knows nothing about, so when both
	// are available it is the authority.
	if got := s.Token(); got != "gho_frombinary" {
		t.Errorf("Token() = %q, want the binary's answer", got)
	}
}

func TestStoreReportsChanges(t *testing.T) {
	s := New(Options{Logger: discardLogger()})
	if !s.store("gho_one") {
		t.Error("store of a first credential reported no change")
	}
	if s.store("gho_one") {
		t.Error("store of an unchanged credential reported a change")
	}
	if !s.store("gho_two") {
		t.Error("store of a rotated credential reported no change")
	}
	if got := s.Token(); got != "gho_two" {
		t.Errorf("Token() = %q, want the newest credential", got)
	}
}

func TestKindLeaksOnlyThePrefix(t *testing.T) {
	if got := kind("gho_16C7e42F292c6912E7710c838347Ae178B4a"); got != "gho" {
		t.Errorf("kind() = %q, want %q", got, "gho")
	}
	if got := kind("40charsofhexwithnounderscoreanywhere"); got != "opaque" {
		t.Errorf("kind() = %q, want %q", got, "opaque")
	}
}

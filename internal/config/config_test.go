package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	want := &Config{
		Repo:     "owner/name",
		Provider: "github",
		Mirror:   "~/mirror.git",
		Queue:    "~/queue",
		Base:     "main",
		Merge:    "when-green",
	}
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "owner/name" || got.Provider != "github" || got.Remote != "company" {
		t.Fatalf("unexpected config: %+v", got)
	}
	if got.NetworkTimeout != "90s" || got.NetworkTimeoutValue != 90*time.Second {
		t.Fatalf("network timeout not defaulted: %q %v", got.NetworkTimeout, got.NetworkTimeoutValue)
	}
	home, _ := os.UserHomeDir()
	if got.Mirror != filepath.Join(home, "mirror.git") {
		t.Fatalf("mirror not expanded: %s", got.Mirror)
	}
	if got.Queue != filepath.Join(home, "queue") {
		t.Fatalf("queue not expanded: %s", got.Queue)
	}
}

func TestValidate(t *testing.T) {
	cases := []Config{
		{Repo: "nope", Provider: "gitea", API: "https://x/api/v1", Mirror: "/m", Queue: "/q", Base: "main", Merge: "never"},
		{Repo: "o/r", Provider: "bitbucket", Mirror: "/m", Queue: "/q", Base: "main", Merge: "never"},
		{Repo: "o/r", Provider: "gitea", Mirror: "/m", Queue: "/q", Base: "main", Merge: "sometimes"},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

func TestNetworkTimeout(t *testing.T) {
	base := Config{Repo: "o/r", Provider: "github", Mirror: "/m", Queue: "/q", Base: "main", Merge: "never", MergeStrategy: "merge"}
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 90 * time.Second, false},
		{"45s", 45 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"nope", 0, true},
		{"0s", 0, true},
		{"-1m", 0, true},
	}
	for _, tc := range cases {
		c := base
		c.NetworkTimeout = tc.in
		err := c.Validate()
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q: expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if c.NetworkTimeoutValue != tc.want {
			t.Fatalf("%q: got %v, want %v", tc.in, c.NetworkTimeoutValue, tc.want)
		}
		if c.NetworkTimeout == "" {
			t.Fatalf("%q: NetworkTimeout was not normalised", tc.in)
		}
	}
}

func TestLoadNetworkTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)
	body := "repo = \"o/r\"\nprovider = \"github\"\nmirror = \"/m\"\nqueue = \"/q\"\nbase = \"main\"\nmerge = \"never\"\nnetwork_timeout = \"5m\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.NetworkTimeoutValue != 5*time.Minute {
		t.Fatalf("got %v, want 5m", got.NetworkTimeoutValue)
	}
}

func TestSlug(t *testing.T) {
	if got := Slug("owner/My.Repo"); got != "owner-My.Repo" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveWalksUp(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, Filename)
	if err := os.WriteFile(path, []byte("repo = \"o/r\"\nprovider = \"github\"\nmirror = \"/m\"\nqueue = \"/q\"\nbase = \"main\"\nmerge = \"never\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, Filename) {
		t.Fatalf("got %q", got)
	}
}

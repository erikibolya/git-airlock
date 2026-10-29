package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestDefaultMergePolicyIsNever(t *testing.T) {
	var c Config
	c.applyDefaults()
	if c.Merge != "never" {
		t.Fatalf("default merge = %q, want never", c.Merge)
	}
}

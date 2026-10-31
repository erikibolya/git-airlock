package configlist

import (
	"os"
	"path/filepath"
	"testing"
)

// An unreachable config (a mapped drive that is not mounted yet at autostart) must stay
// in the list: the desktop app used to drop it, show "No configs yet.", and then save the
// reduced list back, losing the repository until it was added again.
func TestLoadKeepsUnreachablePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.json")
	unreachable := filepath.Join(t.TempDir(), "not-mounted", ".airlock.toml")
	if err := Save(path, []string{unreachable, `Z:\repos\Acme\.airlock.windows.toml`}); err != nil {
		t.Fatal(err)
	}

	got := Load(path)
	want := []string{unreachable, `Z:\repos\Acme\.airlock.windows.toml`}
	if len(got) != len(want) {
		t.Fatalf("Load = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Load[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadLegacySingleConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.json")
	if err := os.WriteFile(path, []byte(`{"config":"Z:\\repos\\Acme\\.airlock.toml"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if len(got) != 1 || got[0] != `Z:\repos\Acme\.airlock.toml` {
		t.Fatalf("Load = %v, want the legacy single config", got)
	}
}

func TestLoadMissingOrMalformedIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := Load(filepath.Join(dir, "absent.json")); got != nil {
		t.Fatalf("missing file: Load = %v, want nil", got)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(bad); got != nil {
		t.Fatalf("malformed file: Load = %v, want nil", got)
	}
}

func TestSaveCreatesDirectoryAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "git-airlock", "desktop.json")
	paths := []string{`Z:\repos\A\.airlock.windows.toml`, `Z:\repos\B\.airlock.windows.toml`}
	if err := Save(path, paths); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if len(got) != len(paths) {
		t.Fatalf("Load = %v, want %v", got, paths)
	}
	for i := range paths {
		if got[i] != paths[i] {
			t.Fatalf("Load[%d] = %q, want %q", i, got[i], paths[i])
		}
	}
}

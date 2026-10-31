package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.6.3", "1.6.3", 0},
		{"v1.7.0", "1.6.3", 1},
		{"1.6.3", "1.7.0", -1},
		{"1.10.0", "1.9.9", 1},
		{"1.6", "1.6.0", 0},
		{"1.8.7-dev.1", "1.8.6", 1},
		{"1.8.6", "1.8.7-dev.1", -1},
		{"1.8.7", "1.8.7-dev.5", 1},
		{"1.8.7-dev.5", "1.8.7", -1},
		{"1.8.7-dev.10", "1.8.7-dev.2", 1},
		{"1.8.7-dev.2", "1.8.7-dev.10", -1},
		{"1.8.7-dev.2", "1.8.7-dev.2", 0},
		{"1.8.7-dev.2+abc", "1.8.7-dev.2", 0},
		{"1.8.7-beta", "1.8.7-dev", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Fatalf("Compare(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAssetFor(t *testing.T) {
	m := Manifest{
		Version:     "1.8.7",
		Windows:     Asset{URL: "windows", SHA256: "s", Signature: "g"},
		Linux:       Asset{URL: "linux", SHA256: "s", Signature: "g"},
		LinuxARM64:  Asset{URL: "linux-arm64", SHA256: "s", Signature: "g"},
		Darwin:      Asset{URL: "darwin", SHA256: "s", Signature: "g"},
		DarwinARM64: Asset{URL: "darwin-arm64", SHA256: "s", Signature: "g"},
	}
	cases := []struct {
		goos, goarch, want string
		ok                 bool
	}{
		{"windows", "amd64", "windows", true},
		{"linux", "amd64", "linux", true},
		{"linux", "arm64", "linux-arm64", true},
		{"darwin", "amd64", "darwin", true},
		{"darwin", "arm64", "darwin-arm64", true},
		{"linux", "386", "", false},
		{"freebsd", "amd64", "", false},
	}
	for _, c := range cases {
		a, ok := m.AssetFor(c.goos, c.goarch)
		if ok != c.ok || a.URL != c.want {
			t.Fatalf("AssetFor(%s/%s) = %q,%v want %q,%v", c.goos, c.goarch, a.URL, ok, c.want, c.ok)
		}
	}
}

func TestFetchLinuxOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{
			Version: "1.8.7",
			Linux:   Asset{URL: "http://" + r.Host + "/airlock-linux-amd64.tar.gz", SHA256: "sha", Signature: "sig"},
		})
	}))
	defer srv.Close()
	m, err := Fetch(context.Background(), srv.URL+"/latest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.AssetFor("linux", "amd64"); !ok {
		t.Fatal("linux asset not found")
	}
	if _, ok := m.AssetFor("windows", "amd64"); ok {
		t.Fatal("windows asset should be missing")
	}
}

func TestSignVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sha := "abc123"
	sig := Sign(priv, "v1.7.0", sha)
	if err := Verify(pub, "1.7.0", sha, sig); err != nil {
		t.Fatal(err)
	}
	if err := Verify(pub, "1.7.1", sha, sig); err == nil {
		t.Fatal("accepted a signature for another version")
	}
	if err := Verify(pub, "1.7.0", "def456", sig); err == nil {
		t.Fatal("accepted a signature for another hash")
	}
}

func TestFetchAndDownload(t *testing.T) {
	data := []byte("installer")
	sum := sha256.Sum256(data)
	hexsum := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			_ = json.NewEncoder(w).Encode(Manifest{
				Version: "1.7.0",
				Windows: Asset{URL: "http://" + r.Host + "/setup.exe", SHA256: hexsum, Signature: "sig"},
			})
		case "/setup.exe":
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	m, err := Fetch(context.Background(), srv.URL+"/latest.json")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "1.7.0" {
		t.Fatalf("version = %q", m.Version)
	}
	path := filepath.Join(t.TempDir(), "setup.exe")
	got, err := Download(context.Background(), m.Windows.URL, path)
	if err != nil {
		t.Fatal(err)
	}
	if got != hexsum {
		t.Fatalf("sha = %s want %s", got, hexsum)
	}
	if b, _ := os.ReadFile(path); string(b) != string(data) {
		t.Fatal("downloaded content differs")
	}
}

func TestParseKeys(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePublicKey(base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePrivateKey(base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		t.Fatal(err)
	}
}

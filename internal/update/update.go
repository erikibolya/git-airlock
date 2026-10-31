// Package update implements the signed release manifest used by the desktop
// updater. The manifest is public; the Ed25519 signature binds the version and
// the installer hash to a key embedded in the app.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Asset describes one downloadable installer or archive.
type Asset struct {
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

// Valid reports whether the asset has all required fields.
func (a Asset) Valid() bool {
	return a.URL != "" && a.SHA256 != "" && a.Signature != ""
}

// Manifest is latest.json from a release.
type Manifest struct {
	Version     string `json:"version"`
	Notes       string `json:"notes,omitempty"`
	Windows     Asset  `json:"windows,omitempty"`
	Linux       Asset  `json:"linux,omitempty"`
	LinuxARM64  Asset  `json:"linux_arm64,omitempty"`
	Darwin      Asset  `json:"darwin,omitempty"`
	DarwinARM64 Asset  `json:"darwin_arm64,omitempty"`
}

// AssetFor returns the signed asset for a GOOS/GOARCH pair.
func (m *Manifest) AssetFor(goos, goarch string) (Asset, bool) {
	var a Asset
	switch goos + "/" + goarch {
	case "windows/amd64":
		a = m.Windows
	case "linux/amd64":
		a = m.Linux
	case "linux/arm64":
		a = m.LinuxARM64
	case "darwin/amd64":
		a = m.Darwin
	case "darwin/arm64":
		a = m.DarwinARM64
	default:
		return Asset{}, false
	}
	return a, a.Valid()
}

// HasAssets reports whether the manifest contains at least one complete asset.
func (m *Manifest) HasAssets() bool {
	return m.Windows.Valid() || m.Linux.Valid() || m.LinuxARM64.Valid() || m.Darwin.Valid() || m.DarwinARM64.Valid()
}

// PublicKey is the Ed25519 key that signs release manifests.
const PublicKey = "Acgkri/7VClnySjUZCHXEKZducmObsLKc6mgcfgiJWQ="

// ManifestURL is the stable signed release manifest.
const ManifestURL = "https://github.com/erikibolya/git-airlock/releases/latest/download/latest.json"

// NormalizeVersion drops a leading v and whitespace.
func NormalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// Compare returns -1, 0 or 1 for semantic versions. A pre-release sorts before
// the matching release (1.8.7-dev.2 < 1.8.7), and pre-release identifiers are
// compared segment by segment, so dev builds can move forward and a stable
// release can supersede them. Build metadata after "+" is ignored.
func Compare(a, b string) int {
	ac, apre := parseVersion(a)
	bc, bpre := parseVersion(b)
	for i := 0; i < len(ac); i++ {
		if ac[i] < bc[i] {
			return -1
		}
		if ac[i] > bc[i] {
			return 1
		}
	}
	switch {
	case len(apre) == 0 && len(bpre) == 0:
		return 0
	case len(apre) == 0:
		return 1
	case len(bpre) == 0:
		return -1
	}
	for i := 0; i < len(apre) && i < len(bpre); i++ {
		if c := compareIdentifier(apre[i], bpre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(apre) < len(bpre):
		return -1
	case len(apre) > len(bpre):
		return 1
	}
	return 0
}

func parseVersion(v string) ([3]int, []string) {
	var core [3]int
	v = NormalizeVersion(v)
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	var pre []string
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre = strings.Split(v[i+1:], ".")
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	for i := 0; i < len(core) && i < len(parts); i++ {
		n, _ := strconv.Atoi(strings.TrimSpace(parts[i]))
		core[i] = n
	}
	return core, pre
}

func compareIdentifier(a, b string) int {
	an, aerr := strconv.Atoi(a)
	bn, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		if an < bn {
			return -1
		}
		if an > bn {
			return 1
		}
		return 0
	case aerr == nil:
		// Numeric identifiers have lower precedence than alphanumeric ones.
		return -1
	case berr == nil:
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// Payload is what gets signed: version and lower-case sha256 on separate lines.
func Payload(version, sha string) []byte {
	return []byte(NormalizeVersion(version) + "\n" + strings.ToLower(strings.TrimSpace(sha)))
}

// Sign returns a base64 Ed25519 signature.
func Sign(priv ed25519.PrivateKey, version, sha string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, Payload(version, sha)))
}

// Verify checks a manifest signature.
func Verify(pub ed25519.PublicKey, version, sha, signature string) error {
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("signature base64: %w", err)
	}
	if !ed25519.Verify(pub, Payload(version, sha), raw) {
		return fmt.Errorf("update signature does not match version %s", NormalizeVersion(version))
	}
	return nil
}

// ParsePublicKey decodes a base64 Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("public key base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// ParsePrivateKey decodes a base64 seed or full private key.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("private key base64: %w", err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	default:
		return nil, fmt.Errorf("private key must be %d or %d bytes, got %d", ed25519.SeedSize, ed25519.PrivateKeySize, len(raw))
	}
}

// Fetch downloads and decodes the manifest.
func Fetch(ctx context.Context, url string) (*Manifest, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "git-airlock-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update manifest: HTTP %d", resp.StatusCode)
	}
	var m Manifest
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Version == "" || !m.HasAssets() {
		return nil, fmt.Errorf("manifest is missing required fields")
	}
	return &m, nil
}

// Download writes url to path and returns the lower-case SHA256.
func Download(ctx context.Context, url, path string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "git-airlock-updater")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

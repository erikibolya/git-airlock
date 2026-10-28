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

// Asset describes one downloadable installer.
type Asset struct {
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

// Manifest is latest.json from a release.
type Manifest struct {
	Version string `json:"version"`
	Notes   string `json:"notes,omitempty"`
	Windows Asset  `json:"windows"`
}

// NormalizeVersion drops a leading v and whitespace.
func NormalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// Compare returns -1, 0 or 1 for semantic versions. Pre-release suffixes are
// ignored; this project does not publish them as latest.
func Compare(a, b string) int {
	as := strings.Split(NormalizeVersion(a), ".")
	bs := strings.Split(NormalizeVersion(b), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		ai, bi := 0, 0
		if i < len(as) {
			ai = atoiSegment(as[i])
		}
		if i < len(bs) {
			bi = atoiSegment(bs[i])
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

func atoiSegment(s string) int {
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	n, _ := strconv.Atoi(s)
	return n
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
	if m.Version == "" || m.Windows.URL == "" || m.Windows.SHA256 == "" || m.Windows.Signature == "" {
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

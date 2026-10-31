// Command updatesign creates the signed update manifest and its signing key.
//
//	updatesign keygen --private-key-file ~/.config/git-airlock/update-signing-key
//	updatesign manifest --version 1.7.0 --installer Setup.exe --url https://... --out latest.json
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/erikibolya/git-airlock/internal/update"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "manifest":
		err = manifest(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "updatesign:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `updatesign - signed update manifest tool

  updatesign keygen --private-key-file PATH
  updatesign manifest --version V --out PATH [--notes TEXT] [--key-file PATH]
                      [--installer PATH --url URL]
                      [--linux PATH --linux-url URL]
                      [--linux-arm64 PATH --linux-arm64-url URL]
                      [--darwin PATH --darwin-url URL]
                      [--darwin-arm64 PATH --darwin-arm64-url URL]

The manifest command also reads the base64 seed from UPDATE_SIGNING_KEY.
Each asset pair is optional, but at least one is required.
`)
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	path := fs.String("private-key-file", "", "where to write the base64 private seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("--private-key-file is required")
	}
	if _, err := os.Stat(*path); err == nil {
		return fmt.Errorf("%s already exists", *path)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*path), 0o700); err != nil {
		return err
	}
	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	if err := os.WriteFile(*path, []byte(seed+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("private-key-file: %s\n", *path)
	fmt.Printf("public-key: %s\n", base64.StdEncoding.EncodeToString(pub))
	return nil
}

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	version := fs.String("version", "", "release version, e.g. 1.7.0")
	installer := fs.String("installer", "", "path to the Windows installer")
	installerURL := fs.String("url", "", "public Windows installer URL")
	linux := fs.String("linux", "", "path to the linux/amd64 archive")
	linuxURL := fs.String("linux-url", "", "public linux/amd64 archive URL")
	linuxARM64 := fs.String("linux-arm64", "", "path to the linux/arm64 archive")
	linuxARM64URL := fs.String("linux-arm64-url", "", "public linux/arm64 archive URL")
	darwin := fs.String("darwin", "", "path to the darwin/amd64 archive")
	darwinURL := fs.String("darwin-url", "", "public darwin/amd64 archive URL")
	darwinARM64 := fs.String("darwin-arm64", "", "path to the darwin/arm64 archive")
	darwinARM64URL := fs.String("darwin-arm64-url", "", "public darwin/arm64 archive URL")
	out := fs.String("out", "latest.json", "output manifest")
	notes := fs.String("notes", "", "release notes shown by the app")
	keyFile := fs.String("key-file", "", "base64 private seed file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return fmt.Errorf("--version is required")
	}

	key := os.Getenv("UPDATE_SIGNING_KEY")
	if *keyFile != "" {
		data, err := os.ReadFile(*keyFile)
		if err != nil {
			return err
		}
		key = string(data)
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("no signing key: pass --key-file or set UPDATE_SIGNING_KEY")
	}
	priv, err := update.ParsePrivateKey(key)
	if err != nil {
		return err
	}

	ver := update.NormalizeVersion(*version)
	m := update.Manifest{Version: ver, Notes: *notes}
	pairs := []struct {
		name, pathFlag, urlFlag string
		path, url               string
		set                     func(*update.Manifest, update.Asset)
	}{
		{"windows", "installer", "url", *installer, *installerURL, func(m *update.Manifest, a update.Asset) { m.Windows = a }},
		{"linux/amd64", "linux", "linux-url", *linux, *linuxURL, func(m *update.Manifest, a update.Asset) { m.Linux = a }},
		{"linux/arm64", "linux-arm64", "linux-arm64-url", *linuxARM64, *linuxARM64URL, func(m *update.Manifest, a update.Asset) { m.LinuxARM64 = a }},
		{"darwin/amd64", "darwin", "darwin-url", *darwin, *darwinURL, func(m *update.Manifest, a update.Asset) { m.Darwin = a }},
		{"darwin/arm64", "darwin-arm64", "darwin-arm64-url", *darwinARM64, *darwinARM64URL, func(m *update.Manifest, a update.Asset) { m.DarwinARM64 = a }},
	}
	any := false
	for _, p := range pairs {
		if p.path == "" && p.url == "" {
			continue
		}
		if p.path == "" || p.url == "" {
			return fmt.Errorf("--%s and --%s must be used together", p.pathFlag, p.urlFlag)
		}
		any = true
		asset, sha, err := signedAsset(priv, ver, p.path, p.url)
		if err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
		p.set(&m, asset)
		fmt.Printf("%-12s sha256: %s\n", p.name, sha)
	}
	if !any {
		return fmt.Errorf("at least one asset is required")
	}

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		return err
	}
	fmt.Printf("manifest: %s\nversion:  %s\n", *out, ver)
	return nil
}

func signedAsset(priv ed25519.PrivateKey, version, path, url string) (update.Asset, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return update.Asset{}, "", err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	return update.Asset{
		URL:       url,
		SHA256:    sha,
		Signature: update.Sign(priv, version, sha),
	}, sha, nil
}

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
  updatesign manifest --version V --installer PATH --url URL --out PATH
                      [--notes TEXT] [--key-file PATH]

The manifest command also reads the base64 seed from UPDATE_SIGNING_KEY.
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
	url := fs.String("url", "", "public installer URL")
	out := fs.String("out", "latest.json", "output manifest")
	notes := fs.String("notes", "", "release notes shown by the app")
	keyFile := fs.String("key-file", "", "base64 private seed file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || *installer == "" || *url == "" {
		return fmt.Errorf("--version, --installer and --url are required")
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
	data, err := os.ReadFile(*installer)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	ver := update.NormalizeVersion(*version)
	m := update.Manifest{
		Version: ver,
		Notes:   *notes,
		Windows: update.Asset{
			URL:       *url,
			SHA256:    sha,
			Signature: update.Sign(priv, ver, sha),
		},
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		return err
	}
	fmt.Printf("manifest: %s\nversion:  %s\nsha256:   %s\n", *out, ver, sha)
	return nil
}

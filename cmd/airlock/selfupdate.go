package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/erikibolya/git-airlock/internal/install"
	"github.com/erikibolya/git-airlock/internal/update"
)

func cmdSelfUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("self-update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	manifestURL := fs.String("manifest", update.ManifestURL, "signed update manifest URL")
	check := fs.Bool("check", false, "only check for a newer version")
	force := fs.Bool("force", false, "update even from a dev build or an equal/newer version")
	var restart stringList
	fs.Var(&restart, "restart-service", "systemd user service to restart after updating (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	m, err := update.Fetch(ctx, *manifestURL)
	if err != nil {
		return err
	}
	pub, err := update.ParsePublicKey(update.PublicKey)
	if err != nil {
		return err
	}
	asset, ok := m.AssetFor(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("release %s has no asset for %s/%s", m.Version, runtime.GOOS, runtime.GOARCH)
	}
	if err := update.Verify(pub, m.Version, asset.SHA256, asset.Signature); err != nil {
		return fmt.Errorf("update manifest signature is invalid: %w", err)
	}

	current := versionString()
	latest := update.NormalizeVersion(m.Version)
	if *check {
		status := "up to date"
		if update.Compare(m.Version, current) > 0 {
			status = "update available"
		}
		if current == "dev" && !*force {
			status = "update available (dev build; pass --force to install)"
		}
		fmt.Printf("current: %s\nlatest:  %s\nasset:   %s/%s\nstatus:  %s\n", current, latest, runtime.GOOS, runtime.GOARCH, status)
		return nil
	}

	if runtime.GOOS == "windows" {
		return errors.New("self-update is for Linux and macOS; on Windows use the desktop app or the installer")
	}
	if current == "dev" && !*force {
		return fmt.Errorf("current build is a dev build; pass --force to replace it with %s", latest)
	}
	if !*force && update.Compare(m.Version, current) <= 0 {
		fmt.Printf("git-airlock %s is already up to date\n", current)
		return nil
	}

	fmt.Printf("updating git-airlock %s -> %s\n", current, latest)
	newPath, err := downloadBinary(ctx, asset, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(newPath))

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := installBinary(newPath, exe, latest); err != nil {
		return err
	}
	fmt.Printf("installed %s\n", exe)
	for _, svc := range restart {
		if strings.TrimSpace(svc) == "" {
			continue
		}
		cmd := exec.Command("systemctl", "--user", "try-restart", svc)
		cmd.Env = os.Environ()
		if os.Getenv("XDG_RUNTIME_DIR") == "" {
			cmd.Env = append(cmd.Env, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "airlock: warning: could not restart %s: %v: %s\n", svc, err, strings.TrimSpace(string(out)))
		} else {
			fmt.Printf("restarted %s\n", svc)
		}
	}
	return nil
}

func cmdInstallUpdateTask(args []string) error {
	fs := flag.NewFlagSet("install-update-task", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	manifestURL := fs.String("manifest", update.ManifestURL, "signed update manifest URL")
	printOnly := fs.Bool("print", false, "print the units instead of installing them")
	var restart stringList
	fs.Var(&restart, "restart-service", "systemd user service to restart after updating (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return install.InstallUpdateTask(install.UpdateOptions{
		ExePath:         exe,
		ManifestURL:     *manifestURL,
		RestartServices: restart,
		Print:           *printOnly,
	})
}

func downloadBinary(ctx context.Context, asset update.Asset, goos, goarch string) (string, error) {
	dir, err := os.MkdirTemp("", "airlock-update-")
	if err != nil {
		return "", err
	}
	archive := filepath.Join(dir, "release.tar.gz")
	sha, err := update.Download(ctx, asset.URL, archive)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if !strings.EqualFold(sha, strings.TrimSpace(asset.SHA256)) {
		os.RemoveAll(dir)
		return "", fmt.Errorf("downloaded archive checksum %s does not match the signed manifest", sha)
	}
	path, err := extractBinary(archive, dir, goos, goarch)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

func extractBinary(archive, dir, goos, goarch string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	wanted := "airlock-" + goos + "-" + goarch
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(h.Name)
		if base != wanted && base != "airlock" {
			continue
		}
		out := filepath.Join(dir, "airlock.new")
		w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return "", err
		}
		if err := w.Close(); err != nil {
			return "", err
		}
		if err := os.Chmod(out, 0o755); err != nil {
			return "", err
		}
		return out, nil
	}
	return "", fmt.Errorf("archive does not contain %s", wanted)
}

func installBinary(src, target, wantVersion string) error {
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("installed binary %s: %w", target, err)
	}
	mode := info.Mode().Perm() | 0o755
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".airlock-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}
	in, err := os.Open(src)
	if err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		in.Close()
		tmp.Close()
		cleanup()
		return err
	}
	in.Close()
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return err
	}
	out, err := exec.Command(tmpName, "version").CombinedOutput()
	if err != nil {
		cleanup()
		return fmt.Errorf("new binary failed to start: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if want := update.NormalizeVersion(wantVersion); want != "" && !strings.Contains(string(out), want) {
		cleanup()
		return fmt.Errorf("new binary reports %q, want version %s", strings.TrimSpace(string(out)), want)
	}

	backup := target + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(target, backup); err != nil {
		cleanup()
		return fmt.Errorf("back up %s: %w", target, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Rename(backup, target)
		cleanup()
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

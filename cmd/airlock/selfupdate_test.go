package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "release.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	entries := map[string]string{
		"README.md":           "readme",
		"airlock-linux-amd64": "binary",
	}
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	path, err := extractBinary(archive, dir, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary" {
		t.Fatalf("got %q", got)
	}
}

func TestInstallBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "airlock")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "new")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho 'git-airlock v9.9.9'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installBinary(src, target, "9.9.9"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "old" {
		t.Fatal("target was not replaced")
	}
	backup, err := os.ReadFile(target + ".old")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "old" {
		t.Fatalf("backup = %q", backup)
	}
}

#!/usr/bin/env python3
"""Render Homebrew, Scoop and winget manifests from release assets.

Usage: python3 packaging/render.py <version> [assets-dir]

Missing assets are downloaded from the GitHub release. The rendered files are
written to packaging/out/.
"""

import hashlib
import os
import sys
import urllib.request

ROOT = os.path.dirname(os.path.abspath(__file__))
BASE = "https://github.com/erikibolya/git-airlock/releases/download/v{version}/{name}"


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def asset(version, assets_dir, name):
    path = os.path.join(assets_dir, name)
    if not os.path.exists(path):
        url = BASE.format(version=version, name=name)
        print("downloading", url)
        urllib.request.urlretrieve(url, path)
    return path


def render(template, values):
    with open(os.path.join(ROOT, template), encoding="utf-8") as f:
        text = f.read()
    for key, value in values.items():
        text = text.replace("@@" + key + "@@", value)
    return text


def write(path, text):
    full = os.path.join(ROOT, "out", path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w", encoding="utf-8", newline="\n") as f:
        f.write(text)
    print("wrote", os.path.relpath(full, ROOT))


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    version = sys.argv[1].lstrip("v")
    assets = sys.argv[2] if len(sys.argv) > 2 else os.path.join(ROOT, "assets", version)
    os.makedirs(assets, exist_ok=True)

    names = {
        "SHA_LINUX_AMD64": "airlock-linux-amd64.tar.gz",
        "SHA_LINUX_ARM64": "airlock-linux-arm64.tar.gz",
        "SHA_DARWIN_AMD64": "airlock-darwin-amd64.tar.gz",
        "SHA_DARWIN_ARM64": "airlock-darwin-arm64.tar.gz",
        "SHA_WINDOWS_ZIP": "airlock-windows-amd64.zip",
        "SHA_INSTALLER": "GitAirlockSetup-" + version + ".exe",
    }
    values = {"VERSION": version}
    for key, name in names.items():
        values[key] = sha256(asset(version, assets, name))

    write("git-airlock.rb", render("homebrew/git-airlock.rb.tmpl", values))
    write("git-airlock.json", render("scoop/git-airlock.json.tmpl", values))
    write(
        "winget/ErikIbolya.GitAirlock.yaml",
        render("winget/ErikIbolya.GitAirlock.yaml.tmpl", values),
    )
    write(
        "winget/ErikIbolya.GitAirlock.installer.yaml",
        render("winget/ErikIbolya.GitAirlock.installer.yaml.tmpl", values),
    )
    write(
        "winget/ErikIbolya.GitAirlock.locale.en-US.yaml",
        render("winget/ErikIbolya.GitAirlock.locale.en-US.yaml.tmpl", values),
    )
    print("\nSHA256")
    for key, name in names.items():
        print(f"  {name}: {values[key]}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
# Copyright (C) 2026 Jeecabs and interragate contributors
# SPDX-License-Identifier: AGPL-3.0-only
"""Package binaries and matching source from a clean, committed checkout."""

import argparse
import gzip
import hashlib
import io
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parent.parent
TARGETS = ("linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64")


def run(*args, cwd=ROOT, **kwargs):
    return subprocess.run(args, cwd=cwd, check=True, **kwargs)


def archive(directory, output, epoch):
    # Fix ownership and timestamps so the same build inputs give the same archive.
    with output.open("wb") as raw:
        with gzip.GzipFile(fileobj=raw, mode="wb", filename="", mtime=epoch) as gz:
            with tarfile.open(fileobj=gz, mode="w") as tar:
                for path in [directory, *sorted(directory.rglob("*"))]:
                    name = path.relative_to(directory.parent).as_posix()
                    info = tar.gettarinfo(str(path), arcname=name)
                    info.uid = info.gid = 0
                    info.uname = info.gname = ""
                    info.mtime = epoch
                    if path.is_file():
                        with path.open("rb") as content:
                            tar.addfile(info, content)
                    else:
                        tar.addfile(info)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", help="a semantic version, for example v0.1.0")
    parser.add_argument("--out", type=Path, default=ROOT / "dist")
    parser.add_argument("--target", action="append", choices=TARGETS)
    args = parser.parse_args()
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", args.version):
        parser.error("version must look like v0.1.0 or v0.1.0-rc.1")
    if run("git", "status", "--porcelain", stdout=subprocess.PIPE).stdout:
        parser.error("commit changes before packaging so source matches the binaries")

    args.out.mkdir(parents=True, exist_ok=True)
    out = args.out.resolve()
    epoch = int(run("git", "log", "-1", "--format=%ct", stdout=subprocess.PIPE).stdout)
    produced = []
    with tempfile.TemporaryDirectory(prefix="interragate-release-") as tmp:
        tmp = Path(tmp)
        source = tmp / f"interragate_{args.version}_source"
        source.mkdir()
        snapshot = run("git", "archive", "--format=tar", "HEAD", stdout=subprocess.PIPE).stdout
        with tarfile.open(fileobj=io.BytesIO(snapshot)) as tar:
            tar.extractall(source, filter="data")
        run("go", "mod", "vendor", cwd=source)
        revision = run("git", "rev-parse", "HEAD", stdout=subprocess.PIPE).stdout.decode().strip()
        toolchain = run("go", "env", "GOVERSION", stdout=subprocess.PIPE).stdout.decode().strip()
        (source / "BUILD-INFO.txt").write_text(
            f"interragate {args.version}\nSource commit: {revision}\nGo toolchain: {toolchain}\n",
            encoding="utf-8",
        )

        destination = out / f"{source.name}.tar.gz"
        archive(source, destination, epoch)
        produced.append(destination)

        notices = tmp / "notices"
        shutil.copytree(source / "third_party", notices)
        for path in sorted((source / "vendor").rglob("*")):
            if path.is_file() and re.match(r"(?i)^(licen[cs]e|notice|copying|copyright|authors|patents)(?:[._-].*)?$", path.name):
                target = notices / "go" / path.relative_to(source / "vendor")
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(path, target)
        shutil.copy2(source / "vendor/modules.txt", notices / "go/modules.txt")
        goroot = Path(run("go", "env", "GOROOT", stdout=subprocess.PIPE).stdout.decode().strip())
        (notices / "go/stdlib").mkdir(parents=True, exist_ok=True)
        for name in ("LICENSE", "PATENTS"):
            shutil.copy2(goroot / name, notices / "go/stdlib" / name)

        for target in args.target or TARGETS:
            goos, goarch = target.split("/")
            package = tmp / f"interragate_{args.version}_{goos}_{goarch}"
            package.mkdir()
            env = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch)
            run("go", "build", "-mod=vendor", "-trimpath", "-buildvcs=false",
                "-ldflags", f"-s -w -X main.version={args.version}",
                "-o", str(package / "interragate"), "./cmd/interragate", cwd=source, env=env)
            for name in ("LICENSE", "COPYRIGHT", "LICENSING.md", "README.md", "BUILD-INFO.txt"):
                shutil.copy2(source / name, package / name)
            shutil.copytree(notices, package / "third_party")
            destination = out / f"{package.name}.tar.gz"
            archive(package, destination, epoch)
            produced.append(destination)

    checksums = out / "checksums.txt"
    with checksums.open("w", encoding="utf-8", newline="\n") as f:
        for path in sorted(produced):
            with path.open("rb") as content:
                digest = hashlib.file_digest(content, "sha256").hexdigest()
            f.write(f"{digest}  {path.name}\n")
            print(path)
    print(checksums)


if __name__ == "__main__":
    main()

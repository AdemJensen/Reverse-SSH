#!/usr/bin/env python3
"""Publish the portable desktop packages for an existing version tag."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from zipfile import ZipFile


VERSION_TAG = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
ASSETS = {
    "ReverseSSH-macOS-Universal.zip": "Reverse SSH.app/Contents/MacOS/ReverseSSH",
    "ReverseSSH-Windows-x64.zip": "Reverse SSH.exe",
    "ReverseSSH-Windows-ARM64.zip": "Reverse SSH.exe",
}


def version_tuple(tag):
    match = VERSION_TAG.fullmatch(tag)
    return tuple(map(int, match.groups())) if match else None


def gh(*args):
    return subprocess.run(["gh", *args], check=True, text=True, stdout=subprocess.PIPE).stdout


def github_json(*args):
    return json.loads(gh("api", *args))


def list_releases(repository):
    pages = github_json("--paginate", "--slurp", f"repos/{repository}/releases?per_page=100")
    return [release for page in pages for release in page]


def verify_tag(repository, tag, expected_commit):
    namespace = f"repos/{repository}/git"
    target = github_json(f"{namespace}/ref/tags/{tag}")["object"]
    while target["type"] == "tag":
        target = github_json(f"{namespace}/tags/{target['sha']}")["object"]
    if target["type"] != "commit" or target["sha"] != expected_commit:
        raise ValueError(f"Tag {tag} no longer points to the build commit")


def validate_archives(directory):
    expected = {}
    for name, executable in ASSETS.items():
        path = directory / name
        with ZipFile(path) as archive:
            if executable not in archive.namelist() or archive.testzip() is not None:
                raise ValueError(f"Missing executable or corrupt data in {path}")
        with path.open("rb") as data:
            digest = f"sha256:{hashlib.file_digest(data, 'sha256').hexdigest()}"
        expected[name] = (path.stat().st_size, digest)
    return expected


def verify_assets(release, expected, verify_contents):
    actual = {asset["name"]: asset for asset in release["assets"]}
    for name, (size, digest) in expected.items():
        asset = actual.get(name)
        if not asset or asset["state"] != "uploaded" or not asset["size"]:
            raise ValueError(f"Release asset {name} was not uploaded")
        if verify_contents and (asset["size"] != size or asset.get("digest") not in (None, "", digest)):
            raise ValueError(f"Release asset {name} differs from the verified ZIP")


def should_make_latest(releases, version):
    published = (release for release in releases if not release["draft"] and not release["prerelease"])
    versions = (version_tuple(release["tag_name"]) for release in published)
    return not any(other is not None and other > version for other in versions)


def publish(repository, tag, commit, directory):
    version = version_tuple(tag)
    if version is None or not re.fullmatch(r"[a-f0-9]{40}", commit):
        raise ValueError("Expected a stable vX.Y.Z tag and its source commit")
    expected = validate_archives(directory)
    verify_tag(repository, tag, commit)
    releases = list_releases(repository)
    release = next((item for item in releases if item["tag_name"] == tag), None)
    if release is not None and not release["draft"]:
        verify_assets(release, expected, verify_contents=False)
        print(f"Release {tag} is already published with all three assets")
        return

    repo_flag = ("--repo", repository)
    if release is None:
        print(gh("release", "create", tag, "--draft", "--verify-tag", "--title", f"Reverse SSH {tag}",
                 "--generate-notes", *repo_flag).strip())

    # A failed job can leave a draft. Only a draft's release assets are replaced.
    release = next((item for item in list_releases(repository) if item["tag_name"] == tag), None)
    if release is None or not release["draft"]:
        raise ValueError(f"Expected a draft release for {tag}")
    asset_paths = [str((directory / name).resolve()) for name in ASSETS]
    gh("release", "upload", tag, *asset_paths, "--clobber", *repo_flag)
    release = github_json(f"repos/{repository}/releases/{release['id']}")
    verify_assets(release, expected, verify_contents=True)
    if release["draft"]:
        verify_tag(repository, tag, commit)
        latest = should_make_latest(list_releases(repository), version)
        print(gh("release", "edit", tag, "--draft=false", "--prerelease=false",
                 f"--latest={str(latest).lower()}", *repo_flag).strip())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("commit")
    parser.add_argument("directory", type=Path)
    arguments = parser.parse_args()
    try:
        publish(os.environ["GITHUB_REPOSITORY"], arguments.tag, arguments.commit, arguments.directory)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))


if __name__ == "__main__":
    main()

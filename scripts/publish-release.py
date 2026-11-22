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
from urllib.parse import urlencode
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


def positive_id(value):
    if type(value) is not int or value <= 0:
        raise ValueError("Expected a positive numeric GitHub release or asset ID")
    return value


def list_releases(repository):
    pages = github_json("--paginate", "--slurp", f"repos/{repository}/releases?per_page=100")
    return [release for page in pages for release in page]


def checked_release(release, tag, release_id):
    if positive_id(release["id"]) != release_id or release["tag_name"] != tag:
        raise ValueError(f"Release {release_id} is not associated with {tag}")
    return release


def load_release(repository, tag, release_id):
    release = github_json(f"repos/{repository}/releases/{release_id}")
    return checked_release(release, tag, release_id)


def find_release(repository, tag):
    releases = [release for release in list_releases(repository) if release["tag_name"] == tag]
    published = [release for release in releases if not release["draft"]]
    matches = published or releases
    if len(matches) > 1:
        raise ValueError(f"Multiple releases match {tag}; specify the draft ID for recovery")
    if matches:
        return load_release(repository, tag, positive_id(matches[0]["id"]))

    # GitHub's release list may not expose a draft, even though its tag lookup can find it.
    try:
        result = json.loads(gh("release", "view", tag, "--repo", repository, "--json", "databaseId,tagName"))
    except subprocess.CalledProcessError:
        return None
    if result["tagName"] != tag:
        raise ValueError(f"GitHub returned a release for a different tag while looking up {tag}")
    return load_release(repository, tag, positive_id(result["databaseId"]))


def create_draft(repository, tag, commit):
    try:
        release = github_json("--method", "POST", f"repos/{repository}/releases",
                              "-f", f"tag_name={tag}", "-f", f"name=Reverse SSH {tag}",
                              "-f", f"target_commitish={commit}", "-F", "draft=true",
                              "-F", "generate_release_notes=true")
    except subprocess.CalledProcessError:
        # A concurrent request or a lost response can leave a valid release behind.
        existing = find_release(repository, tag)
        if existing is None:
            raise
        return existing
    release_id = positive_id(release["id"])
    checked_release(release, tag, release_id)
    if not release["draft"]:
        raise ValueError(f"GitHub did not create a draft for {tag}")
    return release


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


def upload_asset(repository, tag, release, path, expected):
    release_id = release["id"]
    asset_url = f"https://uploads.github.com/repos/{repository}/releases/{release_id}/assets"
    name = path.name
    size, digest = expected
    for attempt in range(2):
        if not release["draft"]:
            raise ValueError(f"Release {tag} became public during asset upload")
        assets = [asset for asset in release["assets"] if asset["name"] == name]
        if len(assets) > 1:
            raise ValueError(f"Multiple draft assets are named {name}")
        if assets:
            asset = assets[0]
            if asset["state"] == "uploaded" and asset["size"] == size and asset.get("digest") == digest:
                return release
            gh("api", "--method", "DELETE", f"repos/{repository}/releases/assets/{positive_id(asset['id'])}")
        try:
            github_json("--method", "POST", "--input", str(path), "--header", "Content-Type: application/zip",
                        f"{asset_url}?{urlencode({'name': name})}")
        except subprocess.CalledProcessError:
            if attempt == 1:
                raise
            # Failed uploads can leave an empty starter asset with the same name.
            release = load_release(repository, tag, release_id)
        else:
            release = load_release(repository, tag, release_id)
            verify_assets(release, {name: expected}, verify_contents=True)
            return release


def should_make_latest(releases, version):
    published = (release for release in releases if not release["draft"] and not release["prerelease"])
    versions = (version_tuple(release["tag_name"]) for release in published)
    return not any(other is not None and other > version for other in versions)


def publish(repository, tag, commit, directory, draft_id=None):
    version = version_tuple(tag)
    if version is None or not re.fullmatch(r"[a-f0-9]{40}", commit):
        raise ValueError("Expected a stable vX.Y.Z tag and its source commit")
    expected = validate_archives(directory)
    verify_tag(repository, tag, commit)
    if draft_id is None:
        release = find_release(repository, tag)
    else:
        release = load_release(repository, tag, positive_id(draft_id))
        if not release["draft"]:
            raise ValueError(f"Release {draft_id} for {tag} is already public")
    if release is None:
        release = create_draft(repository, tag, commit)
    if not release["draft"]:
        verify_assets(release, expected, verify_contents=False)
        print(f"Release {tag} is already published with all three assets")
        return

    release_id = release["id"]
    for name, metadata in expected.items():
        release = upload_asset(repository, tag, release, directory / name, metadata)
    verify_assets(release, expected, verify_contents=True)
    if release["draft"]:
        verify_tag(repository, tag, commit)
        latest = should_make_latest(list_releases(repository), version)
        release = github_json("--method", "PATCH", f"repos/{repository}/releases/{release_id}",
                              "-F", "draft=false", "-F", "prerelease=false",
                              "-f", f"make_latest={str(latest).lower()}")
        checked_release(release, tag, release_id)
        if release["draft"] or release["prerelease"]:
            raise ValueError(f"GitHub did not publish the stable release for {tag}")
        verify_assets(release, expected, verify_contents=True)
    print(release["html_url"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("commit")
    parser.add_argument("directory", type=Path)
    parser.add_argument("--draft-id", type=int, help="resume a specific GitHub draft release")
    arguments = parser.parse_args()
    try:
        publish(os.environ["GITHUB_REPOSITORY"], arguments.tag, arguments.commit, arguments.directory,
                arguments.draft_id)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))


if __name__ == "__main__":
    main()

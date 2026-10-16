#!/usr/bin/env python3
"""Package verified native builds; attach them only to the triggering release.

prepare is entirely offline. publish uses the runner's gh CLI and short-lived
GH_TOKEN; it never creates a release/tag or overwrites a valid existing asset.
"""
import argparse
import gzip
import hashlib
import io
import json
import re
import struct
import subprocess
import sys
import tarfile
from pathlib import Path
from urllib.parse import quote, urlsplit

ARCHES = {"amd64": 62, "arm64": 183}
PROVIDER = "cmdcode-go"
MAX_FILE_BYTES = 256 * 1024 * 1024


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def json_bytes(value):
    return (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()


def read_file(path):
    require(path.is_file() and not path.is_symlink(), f"missing/non-regular file: {path}")
    require(path.stat().st_size <= MAX_FILE_BYTES, f"file exceeds size limit: {path.name}")
    return path.read_bytes()


def context(event, commit, repository):
    require(re.fullmatch(r"[0-9a-f]{40}", commit), "expected a full commit SHA")
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "invalid repository")
    require(event.get("action") == "published", "only release.published is supported")
    require(event.get("repository", {}).get("full_name") == repository, "event repository mismatch")
    release = event.get("release", {})
    require(type(release.get("id")) is int and release["id"] > 0, "invalid release ID")
    require(release.get("draft") is False, "release must already be published")
    require(type(release.get("prerelease")) is bool, "missing prerelease state")
    require(re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?", release.get("tag_name", "")), "tag must be vX.Y.Z or vX.Y.Z-prerelease")
    return {"repository": repository, "release_id": release["id"], "tag": release["tag_name"], "prerelease": release["prerelease"], "commit": commit}


def validate_tag(version, tag, prerelease):
    require(re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version), "plugin version must retain its numeric ABI format")
    base = "v" + version
    require(tag == base or (prerelease and tag.startswith(base + "-")), "release tag does not match internal/buildinfo version (suffixes require a prerelease)")


def validate_build(directory, arch, ctx):
    release = json.loads(read_file(directory / "plugin-metadata.json"))
    provider, version, library = release["provider"], release["version"], release["filename"]
    require(provider == PROVIDER, "unexpected plugin provider")
    validate_tag(version, ctx["tag"], ctx["prerelease"])
    require(library == f"{provider}-v{version}.so", "library name disagrees with buildinfo")
    names = {"LICENSE", "build-info.txt", "build-metadata.json", "elf-report.txt", "plugin-metadata.json", "SHA256SUMS", library}
    require(not directory.is_symlink() and {p.name for p in directory.iterdir()} == names, f"unexpected/incomplete {arch} artifact contents")
    data = {name: read_file(directory / name) for name in names}
    recorded = {}
    for line in data["SHA256SUMS"].decode().splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_.-]+)", line)
        require(match is not None, "invalid artifact checksum entry")
        digest, name = match.groups()
        require(name not in recorded, "duplicate artifact checksum")
        recorded[name] = digest
    require(set(recorded) == names - {"SHA256SUMS"}, "artifact checksum coverage mismatch")
    require(all(sha256(data[name]) == digest for name, digest in recorded.items()), "artifact checksum mismatch")
    metadata = json.loads(data["build-metadata.json"])
    for key, expected in {"commit": ctx["commit"], "repository": ctx["repository"], "architecture": arch, "os": "linux", "plugin": provider, "plugin_version": version, "library": library, "library_sha256": sha256(data[library]), "elf_machine": ARCHES[arch], "cgo_enabled": True}.items():
        require(metadata.get(key) == expected, f"{arch} build metadata mismatch: {key}")
    binary = data[library]
    require(len(binary) >= 20 and binary[:6] == b"\x7fELF\x02\x01" and struct.unpack_from("<HH", binary, 16) == (3, ARCHES[arch]), f"incorrect {arch} ELF")
    info = data["build-info.txt"].decode()
    require(f"vcs.revision={ctx['commit']}" in info and "vcs.modified=false" in info, "build is not from the exact clean revision")
    # A rerun of the same workflow has the same source/run URL. Attempt numbers
    # are execution metadata, not package identity. Keep all durable provenance.
    metadata.pop("run_attempt", None)
    data["build-metadata.json"] = json_bytes(metadata)
    data["SHA256SUMS"] = "".join(f"{sha256(data[name])}  {name}\n" for name in sorted(names - {"SHA256SUMS"})).encode()
    return release, metadata, data


def archive_bytes(data):
    buffer = io.BytesIO()
    with gzip.GzipFile(filename="", fileobj=buffer, mode="wb", mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w") as archive:
            for name, body in sorted(data.items()):
                info = tarfile.TarInfo(name)
                info.size, info.mode, info.mtime = len(body), 0o644, 0
                archive.addfile(info, io.BytesIO(body))
    return buffer.getvalue()


def prepare(inputs, output, event, commit, repository):
    ctx = context(event, commit, repository)
    # Validate the whole pair before writing any release package.
    builds = {arch: validate_build(inputs / arch, arch, ctx) for arch in ARCHES}
    releases = [build[0] for build in builds.values()]
    require(releases[0] == releases[1], "architecture plugin metadata differs")
    require(builds["amd64"][1]["tree"] == builds["arm64"][1]["tree"], "architecture source trees differ")
    require(not output.exists() or not any(output.iterdir()), "release output directory must be empty")
    output.mkdir(parents=True, exist_ok=True)
    provider, version = releases[0]["provider"], releases[0]["version"]
    assets = []
    for arch, (_, metadata, data) in builds.items():
        name = f"{provider}-{ctx['tag']}-linux-{arch}.tar.gz"
        body = archive_bytes(data)
        (output / name).write_bytes(body)
        assets.append({"name": name, "sha256": sha256(body), "size": len(body), "architecture": arch, "library_sha256": metadata["library_sha256"]})
    checksum_name = f"{provider}-{ctx['tag']}-SHA256SUMS"
    checksums = "".join(f"{asset['sha256']}  {asset['name']}\n" for asset in assets).encode()
    (output / checksum_name).write_bytes(checksums)
    assets.append({"name": checksum_name, "sha256": sha256(checksums), "size": len(checksums)})
    manifest = {"schema_version": 1, "complete": True, **ctx, "plugin": provider, "plugin_version": version, "assets": assets}
    path = output / f"{provider}-{ctx['tag']}-release.json"
    path.write_bytes(json_bytes(manifest))
    return path


class GitHub:
    """Only release-asset API operations; no create/edit-release or tag writes."""
    def __init__(self, repository):
        self.repository = repository

    def api(self, endpoint, *args, binary=False):
        result = subprocess.run(["gh", "api", "--hostname", "github.com", "-H", "X-GitHub-Api-Version: 2022-11-28", endpoint, *args], check=True, stdout=subprocess.PIPE)
        if binary:
            return result.stdout
        return json.loads(result.stdout) if result.stdout else None

    def release(self, release_id):
        return self.api(f"repos/{self.repository}/releases/{release_id}")

    def tag_commit(self, tag):
        obj = self.api(f"repos/{self.repository}/git/ref/tags/{quote(tag, safe='')}")["object"]
        for _ in range(8):
            if obj["type"] == "commit":
                return obj["sha"]
            require(obj["type"] == "tag", "release tag does not resolve to a commit")
            obj = self.api(f"repos/{self.repository}/git/tags/{obj['sha']}")["object"]
        raise ValueError("release tag indirection exceeds limit")

    def assets(self, release_id):
        pages = self.api(f"repos/{self.repository}/releases/{release_id}/assets?per_page=100", "--paginate", "--slurp")
        return [asset for page in pages for asset in page]

    def digest(self, asset):
        if str(asset.get("digest", "")).startswith("sha256:"):
            return asset["digest"][7:]
        return sha256(self.api(f"repos/{self.repository}/releases/assets/{asset['id']}", "-H", "Accept: application/octet-stream", binary=True))

    def delete_starter(self, asset):
        require(asset.get("state") == "starter" and asset.get("size") == 0, "only empty failed-upload starters may be deleted")
        self.api(f"repos/{self.repository}/releases/assets/{asset['id']}", "--method", "DELETE")

    def upload(self, release, path):
        base = release["upload_url"].split("{", 1)[0]
        url = urlsplit(base)
        expected_path = f"/repos/{self.repository}/releases/{release['id']}/assets"
        require(url.scheme == "https" and url.netloc == "uploads.github.com" and url.path == expected_path and not url.query and not url.fragment, "unexpected release upload endpoint")
        return self.api(base + "?name=" + quote(path.name, safe=""), "--method", "POST", "-H", "Content-Type: application/octet-stream", "--input", str(path))


def validate_remote(client, ctx):
    release = client.release(ctx["release_id"])
    require(release.get("id") == ctx["release_id"] and release.get("tag_name") == ctx["tag"], "triggering release identity changed")
    require(release.get("draft") is False and release.get("prerelease") == ctx["prerelease"], "release publication state changed")
    require(release.get("immutable") is False, "published release is immutable or mutability is unknown; assets cannot be attached after publication")
    require(client.tag_commit(ctx["tag"]) == ctx["commit"], "release tag moved away from the triggering commit")
    return release


def asset_map(client, release_id):
    result = {}
    for asset in client.assets(release_id):
        require(asset["name"] not in result, "duplicate remote asset name")
        result[asset["name"]] = asset
    return result


def matches(client, remote, expected):
    return remote.get("state") == "uploaded" and remote.get("size") == expected["size"] and client.digest(remote) == expected["sha256"]


def publish(manifest_path, event, commit, repository, client):
    ctx = context(event, commit, repository)
    manifest = json.loads(read_file(manifest_path))
    require(manifest.get("schema_version") == 1 and manifest.get("complete") is True, "invalid completion manifest")
    require(all(manifest.get(key) == value for key, value in ctx.items()), "manifest disagrees with release event")
    require(manifest["plugin"] == PROVIDER, "unexpected plugin provider")
    validate_tag(manifest["plugin_version"], ctx["tag"], ctx["prerelease"])
    prefix = f"{manifest['plugin']}-{ctx['tag']}"
    require(manifest_path.name == prefix + "-release.json", "unexpected manifest filename")
    assets = manifest["assets"]
    expected_names = [prefix + f"-linux-{arch}.tar.gz" for arch in ARCHES] + [prefix + "-SHA256SUMS"]
    require([item["name"] for item in assets] == expected_names, "manifest must contain both architectures and checksums in order")
    require([item.get("architecture") for item in assets[:2]] == list(ARCHES), "manifest architecture mismatch")
    assets = [*assets, {"name": manifest_path.name, "sha256": sha256(read_file(manifest_path)), "size": manifest_path.stat().st_size}]
    for expected in assets:
        body = read_file(manifest_path.parent / expected["name"])
        require(sha256(body) == expected["sha256"] and len(body) == expected["size"], "local release asset changed")
    validate_remote(client, ctx)
    existing = asset_map(client, ctx["release_id"])
    # Conflicts fail before the first mutation. Never use destructive --clobber.
    for expected in assets:
        remote = existing.get(expected["name"])
        if remote and not (remote.get("state") == "starter" and remote.get("size") == 0):
            require(matches(client, remote, expected), f"existing asset differs; refusing overwrite: {expected['name']}")
    for expected in assets:
        release = validate_remote(client, ctx)
        remote = asset_map(client, ctx["release_id"]).get(expected["name"])
        if remote and matches(client, remote, expected):
            continue
        if remote:
            client.delete_starter(remote)
        # The completion manifest is last. All earlier uploads must be confirmed
        # before it becomes visible. API uploads are not an atomic transaction.
        if expected["name"] == manifest_path.name:
            verified = asset_map(client, ctx["release_id"])
            require(all(name in verified and matches(client, verified[name], item) for item in assets[:-1] for name in [item["name"]]), "release assets incomplete before manifest upload")
        uploaded = client.upload(release, manifest_path.parent / expected["name"])
        require(uploaded.get("name") == expected["name"] and matches(client, uploaded, expected), "uploaded asset failed digest verification")
    validate_remote(client, ctx)
    final = asset_map(client, ctx["release_id"])
    require(all(item["name"] in final and matches(client, final[item["name"]], item) for item in assets), "release asset verification incomplete")
    print(f"Verified complete release assets for {ctx['tag']} at {ctx['commit']}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["prepare", "publish"])
    parser.add_argument("--event", type=Path, required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--inputs", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--manifest", type=Path)
    args = parser.parse_args()
    event = json.loads(read_file(args.event))
    if args.command == "prepare":
        require(args.inputs is not None and args.output is not None, "prepare requires --inputs and --output")
        print(prepare(args.inputs, args.output, event, args.sha, args.repository))
    else:
        require(args.manifest is not None, "publish requires --manifest")
        publish(args.manifest, event, args.sha, args.repository, GitHub(args.repository))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f"Release assets failed: {error}", file=sys.stderr)
        sys.exit(1)

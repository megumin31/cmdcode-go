#!/usr/bin/env python3
"""Offline packaging/publication contract tests, with no GitHub API calls.

The ELF fixtures contain only synthetic headers. They exercise packaging checks;
they are not real plugins and do not replace either architecture's native gate.
"""
import copy
import hashlib
import io
import json
import struct
import tarfile
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch

import release_assets as ra


SHA = "a" * 40
TREE = "b" * 40
REPOSITORY = "example/plugin"
LIBRARY = "cmdcode-go-v0.1.0.so"


def digest(body):
    return hashlib.sha256(body).hexdigest()


def encode(value):
    return (json.dumps(value, sort_keys=True) + "\n").encode()


def event_fixture():
    return {"ref": "refs/tags/v0.1.0", "after": SHA, "deleted": False,
            "repository": {"full_name": REPOSITORY}}


class FakeGitHub:
    """In-memory release assets; no network or real publication operations."""
    def __init__(self, event):
        ctx = ra.context(event, SHA, REPOSITORY)
        self.remote_release = {"id": 321, "tag_name": ctx["tag"], "draft": True, "prerelease": ctx["prerelease"]}
        self.created = 0
        self.published = 0
        self.absent = False
        self.remote_release.update(immutable=False, upload_url=(
            f"https://uploads.github.com/repos/{REPOSITORY}/releases/321/assets{{?name,label}}"))
        self.commit = SHA
        self.remote_assets = []
        self.bodies = {}
        self.uploads = []
        self.deletions = []
        self.fail_name = None
        self.corrupt_name = None
        self.next_id = 1

    def find_release(self, tag):
        return None if self.absent else copy.deepcopy(self.remote_release)

    def create_draft(self, ctx):
        self.created += 1
        self.absent = False
        return copy.deepcopy(self.remote_release)

    def publish_draft(self, release_id):
        self.published += 1
        self.remote_release["draft"] = False
        return copy.deepcopy(self.remote_release)

    def release(self, release_id):
        if release_id != 321:
            raise ValueError("unexpected release ID")
        return copy.deepcopy(self.remote_release)

    def tag_commit(self, tag):
        return self.commit

    def assets(self, release_id):
        if release_id != 321:
            raise ValueError("unexpected release ID")
        return copy.deepcopy(self.remote_assets)

    def digest(self, asset):
        return digest(self.bodies[asset["id"]])

    def seed(self, name, body, state="uploaded"):
        asset = {"id": self.next_id, "name": name, "state": state,
                 "size": len(body), "digest": "sha256:" + digest(body)}
        self.next_id += 1
        self.remote_assets.append(asset)
        self.bodies[asset["id"]] = body
        return copy.deepcopy(asset)

    def delete_starter(self, asset):
        if asset["state"] != "starter" or asset["size"] != 0:
            raise ValueError("only empty starters may be removed")
        self.deletions.append(asset["name"])
        self.remote_assets = [item for item in self.remote_assets if item["id"] != asset["id"]]
        self.bodies.pop(asset["id"])

    def upload(self, release, path):
        if release["id"] != 321:
            raise ValueError("upload targeted another release")
        if path.name == self.fail_name:
            raise OSError("simulated upload failure")
        if any(item["name"] == path.name for item in self.remote_assets):
            raise ValueError("attempted to overwrite an existing asset")
        body = path.read_bytes()
        if path.name == self.corrupt_name:
            body = body[:-1] + bytes([body[-1] ^ 1])
        self.uploads.append(path.name)
        return self.seed(path.name, body)


class FixtureCase(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.inputs = self.root / "inputs"
        self.output = self.root / "output"
        self.event = event_fixture()
        for arch in ra.ARCHES:
            self.write_build(arch)

    def write_build(self, arch):
        directory = self.inputs / arch
        directory.mkdir(parents=True, exist_ok=True)
        # Synthetic header only: this is deliberately not a loadable library.
        binary = bytearray(128)
        binary[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<HH", binary, 16, 3, ra.ARCHES[arch])
        binary[32:32 + len(arch)] = arch.encode()
        metadata = {"repository": REPOSITORY, "commit": SHA, "tree": TREE,
                    "plugin": "cmdcode-go", "plugin_version": "0.1.0", "build_tag": "v0.1.0",
                    "library": LIBRARY, "library_sha256": digest(binary),
                    "os": "linux", "architecture": arch,
                    "elf_machine": ra.ARCHES[arch], "cgo_enabled": True,
                    "run_url": f"https://github.com/{REPOSITORY}/actions/runs/7",
                    "run_attempt": "1", "runner_image_version": "fixture-image"}
        files = {LIBRARY: bytes(binary), "LICENSE": b"Synthetic license fixture\n",
                 "plugin-metadata.json": encode({"provider": "cmdcode-go",
                                                "version": "0.1.0", "filename": LIBRARY, "build_tag": "v0.1.0"}),
                 "build-metadata.json": encode(metadata),
                 "build-info.txt": f"build vcs.revision={SHA}\nbuild vcs.modified=false\n".encode(),
                 "elf-report.txt": b"Synthetic ELF report; not native verification\n"}
        for name, body in files.items():
            (directory / name).write_bytes(body)
        self.refresh_checksums(arch)

    def refresh_checksums(self, arch):
        directory = self.inputs / arch
        checksums = "".join(f"{digest(path.read_bytes())}  {path.name}\n"
                            for path in sorted(directory.iterdir())
                            if path.name != "SHA256SUMS" and path.is_file())
        (directory / "SHA256SUMS").write_text(checksums)

    def change_json(self, arch, name, **changes):
        path = self.inputs / arch / name
        value = json.loads(path.read_bytes())
        value.update(changes)
        path.write_bytes(encode(value))
        self.refresh_checksums(arch)

    def prepare(self, output=None):
        return ra.prepare(self.inputs, output or self.output, self.event, SHA, REPOSITORY)

    def assert_prepare_rejected(self):
        with self.assertRaises((ValueError, OSError, KeyError)):
            self.prepare()
        self.assertFalse(self.output.exists(), "invalid pair must fail before producing packages")


class PrepareTests(FixtureCase):
    def test_dual_archives_preserve_library_and_all_checksums(self):
        manifest_path = self.prepare()
        manifest = json.loads(manifest_path.read_bytes())
        self.assertEqual(manifest["commit"], SHA)
        self.assertNotIn("release_id", manifest)
        self.assertIs(manifest["complete"], True)
        self.assertEqual([item.get("architecture") for item in manifest["assets"][:2]],
                         ["amd64", "arm64"])
        self.assertEqual(len(list(self.output.iterdir())), 4)
        for asset in manifest["assets"]:
            body = (self.output / asset["name"]).read_bytes()
            self.assertEqual(digest(body), asset["sha256"])
            self.assertEqual(len(body), asset["size"])
            if "architecture" not in asset:
                continue
            self.assertEqual(body[4:8], b"\0" * 4, "gzip timestamp must be stable")
            with tarfile.open(fileobj=io.BytesIO(body), mode="r:gz") as archive:
                members = archive.getmembers()
                self.assertEqual([item.name for item in members], sorted(item.name for item in members))
                self.assertTrue(all(item.isfile() and item.uid == item.gid == item.mtime == 0
                                    and item.uname == item.gname == "" for item in members))
                files = {item.name: archive.extractfile(item).read() for item in members}
            self.assertIn(LIBRARY, files)
            self.assertEqual(struct.unpack_from("<H", files[LIBRARY], 18)[0],
                             ra.ARCHES[asset["architecture"]])
            packed_metadata = json.loads(files["build-metadata.json"])
            self.assertNotIn("run_attempt", packed_metadata)
            self.assertEqual(packed_metadata["run_url"],
                             f"https://github.com/{REPOSITORY}/actions/runs/7")
            checksums = dict(line.split("  ")[::-1] for line in files["SHA256SUMS"].decode().splitlines())
            self.assertEqual(set(checksums), set(files) - {"SHA256SUMS"})
            for name, expected in checksums.items():
                self.assertEqual(digest(files[name]), expected)
        external = (self.output / manifest["assets"][-1]["name"]).read_text()
        self.assertEqual(external, "".join(f"{item['sha256']}  {item['name']}\n"
                                          for item in manifest["assets"][:2]))

    def test_repeat_and_changed_attempt_are_byte_identical(self):
        self.prepare()
        for arch in ra.ARCHES:
            self.change_json(arch, "build-metadata.json", run_attempt="2")
        second = self.root / "second"
        self.prepare(second)
        self.assertEqual({p.name: p.read_bytes() for p in self.output.iterdir()},
                         {p.name: p.read_bytes() for p in second.iterdir()})

    def test_wrong_metadata_architecture_rejected(self):
        self.change_json("arm64", "build-metadata.json", architecture="amd64")
        self.assert_prepare_rejected()

    def test_wrong_elf_architecture_rejected_even_with_matching_hash(self):
        path = self.inputs / "arm64" / LIBRARY
        binary = bytearray(path.read_bytes())
        struct.pack_into("<H", binary, 18, ra.ARCHES["amd64"])
        path.write_bytes(binary)
        self.change_json("arm64", "build-metadata.json", library_sha256=digest(binary))
        self.assert_prepare_rejected()

    def test_wrong_metadata_sha_rejected(self):
        self.change_json("arm64", "build-metadata.json", commit="c" * 40)
        self.assert_prepare_rejected()

    def test_wrong_embedded_revision_report_rejected(self):
        path = self.inputs / "arm64" / "build-info.txt"
        path.write_text(path.read_text().replace(SHA, "c" * 40))
        self.refresh_checksums("arm64")
        self.assert_prepare_rejected()

    def test_dirty_build_report_rejected(self):
        path = self.inputs / "arm64" / "build-info.txt"
        path.write_text(path.read_text().replace("modified=false", "modified=true"))
        self.refresh_checksums("arm64")
        self.assert_prepare_rejected()

    def test_source_tree_mismatch_rejected(self):
        self.change_json("arm64", "build-metadata.json", tree="d" * 40)
        self.assert_prepare_rejected()

    def test_tag_version_mismatch_rejected(self):
        self.event["ref"] = "refs/tags/v9.9.9"
        self.assert_prepare_rejected()

    def test_prerelease_suffix_keeps_numeric_plugin_version(self):
        self.event["ref"] = "refs/tags/v0.1.0-rc.1"
        for arch in ra.ARCHES:
            self.change_json(arch, "plugin-metadata.json", build_tag="v0.1.0-rc.1")
            self.change_json(arch, "build-metadata.json", build_tag="v0.1.0-rc.1")
        manifest = json.loads(self.prepare().read_bytes())
        self.assertEqual(manifest["plugin_version"], "0.1.0")
        self.assertTrue(manifest["prerelease"])
        self.assertIn("v0.1.0-rc.1", manifest["assets"][0]["name"])

    def test_wrong_full_build_tag_rejected(self):
        self.change_json("arm64", "plugin-metadata.json", build_tag="v0.1.0-rc.1")
        self.assert_prepare_rejected()

    def test_wrong_provenance_build_tag_rejected(self):
        self.change_json("arm64", "build-metadata.json", build_tag="dev")
        self.assert_prepare_rejected()

    def test_missing_architecture_file_rejected(self):
        (self.inputs / "arm64" / LIBRARY).unlink()
        self.assert_prepare_rejected()

    def test_unlisted_extra_file_rejected(self):
        (self.inputs / "arm64" / "secret.txt").write_text("must not be packaged")
        self.assert_prepare_rejected()

    def test_symlink_file_rejected(self):
        path = self.inputs / "arm64" / "LICENSE"
        path.unlink()
        path.symlink_to(self.inputs / "amd64" / "LICENSE")
        self.assert_prepare_rejected()

    def test_provider_path_injection_rejected(self):
        self.change_json("arm64", "plugin-metadata.json", provider="../cmdcode-go")
        self.assert_prepare_rejected()

    def test_wrong_well_formed_provider_rejected(self):
        self.change_json("arm64", "plugin-metadata.json", provider="another-plugin")
        self.assert_prepare_rejected()

    def test_library_path_injection_rejected(self):
        self.change_json("arm64", "plugin-metadata.json", filename="../" + LIBRARY)
        self.assert_prepare_rejected()

    def test_tag_path_injection_rejected(self):
        self.event["ref"] = "refs/tags/v0.1.0/../../escape"
        self.assert_prepare_rejected()

    def test_checksum_path_injection_rejected(self):
        path = self.inputs / "arm64" / "SHA256SUMS"
        path.write_text(path.read_text() + "0" * 64 + "  ../escape\n")
        self.assert_prepare_rejected()

    def test_tampered_library_rejected(self):
        with (self.inputs / "arm64" / LIBRARY).open("ab") as stream:
            stream.write(b"tampered")
        self.assert_prepare_rejected()

    def test_duplicate_checksum_rejected(self):
        path = self.inputs / "arm64" / "SHA256SUMS"
        path.write_text(path.read_text() + path.read_text().splitlines()[0] + "\n")
        self.assert_prepare_rejected()

    def test_existing_output_is_not_overwritten(self):
        self.prepare()
        before = {p.name: p.read_bytes() for p in self.output.iterdir()}
        with self.assertRaises(ValueError):
            self.prepare()
        self.assertEqual(before, {p.name: p.read_bytes() for p in self.output.iterdir()})

    def test_invalid_event_context_rejected(self):
        cases = [("ref", "refs/heads/main"), ("repository", {"full_name": "other/repo"}),
                 ("deleted", True), ("after", "bad"), ("ref", "refs/tags/v01.0.0")]
        for key, value in cases:
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                ra.context({**self.event, key: value}, SHA, REPOSITORY)
        with self.assertRaises(ValueError):
            ra.context(self.event, SHA[:7], REPOSITORY)


class PublishTests(FixtureCase):
    def setUp(self):
        super().setUp()
        self.manifest_path = self.prepare()
        self.manifest = json.loads(self.manifest_path.read_bytes())
        self.names = [item["name"] for item in self.manifest["assets"]] + [self.manifest_path.name]
        self.client = FakeGitHub(self.event)

    def publish(self):
        with redirect_stdout(io.StringIO()):
            ra.publish(self.manifest_path, self.event, SHA, REPOSITORY, self.client)

    def assert_no_mutations(self):
        self.assertEqual(self.client.uploads, [])
        self.assertEqual(self.client.deletions, [])

    def test_success_uploads_both_archives_checksums_then_manifest(self):
        self.publish()
        self.assertEqual(self.client.uploads, self.names)
        self.assertEqual({asset["name"] for asset in self.client.assets(321)}, set(self.names))

    def test_new_draft_created_and_published_only_after_validation(self):
        self.client.absent = True
        self.publish()
        self.assertEqual(self.client.created, 1)
        self.assertEqual(self.client.published, 1)
        self.assertFalse(self.client.remote_release["draft"])

    def test_published_incomplete_release_is_never_modified(self):
        self.client.remote_release["draft"] = False
        with self.assertRaisesRegex(ValueError, "published release"):
            self.publish()
        self.assert_no_mutations()

    def test_failed_upload_keeps_draft_unpublished(self):
        self.client.fail_name = self.names[1]
        with self.assertRaises(OSError):
            self.publish()
        self.assertTrue(self.client.remote_release["draft"])
        self.assertEqual(self.client.published, 0)

    def test_identical_rerun_performs_no_mutations(self):
        self.publish()
        self.client.uploads.clear()
        self.publish()
        self.assert_no_mutations()

    def test_conflicting_last_asset_rejects_before_any_upload(self):
        self.client.seed(self.manifest_path.name, b"different existing completion")
        with self.assertRaisesRegex(ValueError, "refusing overwrite"):
            self.publish()
        self.assert_no_mutations()

    def test_empty_starter_is_cleaned_and_completed(self):
        self.client.seed(self.names[1], b"", state="starter")
        self.publish()
        self.assertEqual(self.client.deletions, [self.names[1]])
        self.assertEqual(self.client.uploads, self.names)

    def test_nonempty_starter_is_not_deleted(self):
        self.client.seed(self.names[0], b"not empty", state="starter")
        with self.assertRaises(ValueError):
            self.publish()
        self.assert_no_mutations()

    def test_failed_upload_has_no_completion_and_rerun_fills_only_missing(self):
        self.client.fail_name = self.names[1]
        with self.assertRaises(OSError):
            self.publish()
        self.assertEqual(self.client.uploads, self.names[:1])
        self.assertNotIn(self.manifest_path.name, {item["name"] for item in self.client.assets(321)})
        self.client.fail_name = None
        self.publish()
        self.assertEqual(self.client.uploads, self.names)
        self.assertEqual(self.client.uploads[-1], self.manifest_path.name)

    def test_lost_archive_response_is_reconciled_by_non_destructive_rerun(self):
        original_upload = self.client.upload

        def lose_response(release, path):
            uploaded = original_upload(release, path)
            if path.name == self.names[1]:
                raise OSError("server accepted archive but response was lost")
            return uploaded

        with patch.object(self.client, "upload", side_effect=lose_response):
            with self.assertRaises(OSError):
                self.publish()
        self.assertEqual(self.client.uploads, self.names[:2])
        self.assertNotIn(self.manifest_path.name, self.client.uploads)
        self.publish()
        self.assertEqual(self.client.uploads, self.names)
        self.assertEqual(self.client.deletions, [])

    def test_lost_manifest_response_can_leave_complete_set_despite_job_failure(self):
        original_upload = self.client.upload

        def lose_response(release, path):
            uploaded = original_upload(release, path)
            if path.name == self.manifest_path.name:
                raise OSError("server accepted manifest but response was lost")
            return uploaded

        with patch.object(self.client, "upload", side_effect=lose_response):
            with self.assertRaises(OSError):
                self.publish()
        self.assertEqual(self.client.uploads, self.names)
        self.client.uploads.clear()
        self.publish()
        self.assert_no_mutations()

    def test_tag_moved_during_upload_stops_remaining_assets(self):
        original_upload = self.client.upload

        def move_tag(release, path):
            uploaded = original_upload(release, path)
            self.client.commit = "e" * 40
            return uploaded

        with patch.object(self.client, "upload", side_effect=move_tag):
            with self.assertRaisesRegex(ValueError, "tag moved"):
                self.publish()
        self.assertEqual(self.client.uploads, self.names[:1])
        self.assertNotIn(self.manifest_path.name, self.client.uploads)

    def test_corrupt_upload_fails_before_completion(self):
        self.client.corrupt_name = self.names[1]
        with self.assertRaisesRegex(ValueError, "digest verification"):
            self.publish()
        self.assertNotIn(self.manifest_path.name, self.client.uploads)

    def test_immutable_release_rejected_without_changes(self):
        self.client.remote_release["immutable"] = True
        with self.assertRaisesRegex(ValueError, "immutable"):
            self.publish()
        self.assert_no_mutations()

    def test_moved_tag_rejected_without_changes(self):
        self.client.commit = "e" * 40
        with self.assertRaisesRegex(ValueError, "tag moved"):
            self.publish()
        self.assert_no_mutations()

    def test_changed_release_identity_or_state_rejected(self):
        cases = {"id": 999, "tag_name": "v0.2.0", "draft": None, "prerelease": True}
        for key, value in cases.items():
            with self.subTest(key=key):
                self.client = FakeGitHub(self.event)
                self.client.remote_release[key] = value
                with self.assertRaises(ValueError):
                    self.publish()
                self.assert_no_mutations()

    def test_changed_local_asset_rejected_before_mutations(self):
        (self.output / self.names[0]).write_bytes(b"tampered archive")
        with self.assertRaisesRegex(ValueError, "local release asset changed"):
            self.publish()
        self.assert_no_mutations()

    def test_manifest_asset_path_injection_rejected(self):
        self.manifest["assets"][0]["name"] = "../escape"
        self.manifest_path.write_bytes(encode(self.manifest))
        with self.assertRaises(ValueError):
            self.publish()
        self.assert_no_mutations()

    def test_manifest_provider_path_injection_rejected(self):
        self.manifest["plugin"] = "../cmdcode-go"
        self.manifest_path.write_bytes(encode(self.manifest))
        with self.assertRaises(ValueError):
            self.publish()
        self.assert_no_mutations()

    def test_manifest_wrong_well_formed_provider_rejected(self):
        self.manifest["plugin"] = "another-plugin"
        self.manifest_path.write_bytes(encode(self.manifest))
        with self.assertRaises(ValueError):
            self.publish()
        self.assert_no_mutations()

    def test_manifest_reordered_architectures_rejected(self):
        self.manifest["assets"][:2] = reversed(self.manifest["assets"][:2])
        self.manifest_path.write_bytes(encode(self.manifest))
        with self.assertRaises(ValueError):
            self.publish()
        self.assert_no_mutations()

    def test_unrelated_asset_is_preserved(self):
        existing = self.client.seed("user-notes.txt", b"preserve this")
        self.publish()
        self.assertEqual(self.client.bodies[existing["id"]], b"preserve this")
        self.assertEqual(self.client.deletions, [])

    def test_duplicate_remote_name_rejected_before_mutations(self):
        self.client.seed("duplicate.txt", b"one")
        self.client.seed("duplicate.txt", b"two")
        with self.assertRaisesRegex(ValueError, "duplicate"):
            self.publish()
        self.assert_no_mutations()

    def test_completion_is_not_uploaded_if_earlier_asset_disappears(self):
        original_assets = self.client.assets

        def disappearing_assets(release_id):
            assets = original_assets(release_id)
            if len(self.client.uploads) == 3:
                return [item for item in assets if item["name"] != self.names[0]]
            return assets

        with patch.object(self.client, "assets", side_effect=disappearing_assets):
            with self.assertRaisesRegex(ValueError, "incomplete before manifest"):
                self.publish()
        self.assertNotIn(self.manifest_path.name, self.client.uploads)


class GitHubAdapterTests(unittest.TestCase):
    def setUp(self):
        self.client = ra.GitHub(REPOSITORY)

    def response(self, body):
        if not isinstance(body, bytes):
            body = encode(body)
        return ra.subprocess.CompletedProcess(args=[], returncode=0, stdout=body)

    def assert_safe_command(self, call):
        command = call.args[0]
        self.assertIsInstance(command, list)
        self.assertEqual(command[:4], ["gh", "api", "--hostname", "github.com"])
        self.assertNotIn("shell", call.kwargs)
        self.assertIs(call.kwargs.get("check"), True)
        self.assertEqual(call.kwargs.get("stdout"), ra.subprocess.PIPE)
        return command

    def test_find_release_paginates_and_includes_drafts(self):
        draft = {"id": 321, "tag_name": "v0.1.0", "draft": True}
        with patch.object(ra.subprocess, "run", return_value=self.response([[draft], []])) as run:
            self.assertEqual(self.client.find_release("v0.1.0"), draft)
            command = self.assert_safe_command(run.call_args)
            self.assertIn("--paginate", command)
            self.assertIn("--slurp", command)
            self.assertNotIn("--method", command)

    def test_create_draft_and_publish_use_explicit_boolean_fields(self):
        ctx = ra.context(event_fixture(), SHA, REPOSITORY)
        with patch.object(ra.subprocess, "run", return_value=self.response({"id":321})) as run:
            self.client.create_draft(ctx)
            command = self.assert_safe_command(run.call_args)
            self.assertIn("draft=true", command)
            self.assertIn("prerelease=false", command)
            self.assertIn("target_commitish=" + SHA, command)
            self.client.publish_draft(321)
            command = self.assert_safe_command(run.call_args)
            self.assertIn("PATCH", command)
            self.assertIn("draft=false", command)

    def test_release_get_uses_numeric_id_without_write_method_or_shell(self):
        release = {"id": 321, "draft": True, "tag_name": "v0.1.0"}
        with patch.object(ra.subprocess, "run", return_value=self.response(release)) as run:
            self.assertEqual(self.client.release(321), release)
            run.assert_called_once()
            command = self.assert_safe_command(run.call_args)
            self.assertIn(f"repos/{REPOSITORY}/releases/321", command)
            self.assertNotIn("--method", command)

    def test_tag_dereference_gets_use_only_expected_repository_paths(self):
        replies = [self.response({"object": {"type": "tag", "sha": "d" * 40}}),
                   self.response({"object": {"type": "commit", "sha": SHA}})]
        with patch.object(ra.subprocess, "run", side_effect=replies) as run:
            self.assertEqual(self.client.tag_commit("v0.1.0"), SHA)
            commands = [self.assert_safe_command(call) for call in run.call_args_list]
            self.assertEqual(len(commands), 2)
            self.assertIn(f"repos/{REPOSITORY}/git/ref/tags/v0.1.0", commands[0])
            self.assertIn(f"repos/{REPOSITORY}/git/tags/{'d' * 40}", commands[1])
            self.assertTrue(all("--method" not in command for command in commands))

    def test_digest_null_fallback_uses_binary_get_command(self):
        body = b"\0binary\xffasset\n"
        with patch.object(ra.subprocess, "run", return_value=self.response(body)) as run:
            self.assertEqual(self.client.digest({"id": 42, "digest": None}), digest(body))
            command = self.assert_safe_command(run.call_args)
            self.assertIn(f"repos/{REPOSITORY}/releases/assets/42", command)
            self.assertIn("Accept: application/octet-stream", command)
            self.assertNotIn("--method", command)

    def test_upload_command_uses_exact_id_url_post_input_and_no_shell(self):
        release = {"id": 321, "upload_url": (
            f"https://uploads.github.com/repos/{REPOSITORY}/releases/321/assets{{?name,label}}")}
        path = Path("directory with spaces/asset.tar.gz")
        with patch.object(ra.subprocess, "run", return_value=self.response({"id": 42})) as run:
            self.assertEqual(self.client.upload(release, path), {"id": 42})
            run.assert_called_once()
            command = self.assert_safe_command(run.call_args)
            self.assertIn(f"https://uploads.github.com/repos/{REPOSITORY}/releases/321/assets?name=asset.tar.gz", command)
            self.assertEqual(command[command.index("--method") + 1], "POST")
            self.assertEqual(command[command.index("--input") + 1], str(path))
            self.assertIn("Content-Type: application/octet-stream", command)

    def test_empty_starter_delete_uses_asset_id_and_delete_command(self):
        with patch.object(ra.subprocess, "run", return_value=self.response(b"")) as run:
            self.client.delete_starter({"id": 42, "state": "starter", "size": 0})
            run.assert_called_once()
            command = self.assert_safe_command(run.call_args)
            self.assertIn(f"repos/{REPOSITORY}/releases/assets/42", command)
            self.assertEqual(command[command.index("--method") + 1], "DELETE")

    def test_missing_digest_downloads_and_hashes_bytes(self):
        with patch.object(self.client, "api", return_value=b"asset bytes") as api:
            self.assertEqual(self.client.digest({"id": 42, "digest": None}), digest(b"asset bytes"))
            api.assert_called_once_with(f"repos/{REPOSITORY}/releases/assets/42", "-H",
                                        "Accept: application/octet-stream", binary=True)

    def test_digest_present_does_not_download(self):
        with patch.object(self.client, "api") as api:
            self.assertEqual(self.client.digest({"id": 42, "digest": "sha256:" + "c" * 64}), "c" * 64)
            api.assert_not_called()

    def test_annotated_tag_is_peeled_to_commit(self):
        replies = [{"object": {"type": "tag", "sha": "d" * 40}},
                   {"object": {"type": "commit", "sha": SHA}}]
        with patch.object(self.client, "api", side_effect=replies) as api:
            self.assertEqual(self.client.tag_commit("v0.1.0"), SHA)
            self.assertEqual(api.call_count, 2)

    def test_unexpected_upload_endpoint_rejected_without_api_call(self):
        for url in ["https://evil.example/assets", "http://uploads.github.com/assets",
                    f"https://uploads.github.com/repos/{REPOSITORY}/releases/999/assets",
                    f"https://uploads.github.com/repos/{REPOSITORY}/releases/321/assets?bad=1"]:
            with self.subTest(url=url), patch.object(ra.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    self.client.upload({"id": 321, "upload_url": url}, Path("asset.tar.gz"))
                run.assert_not_called()

    def test_starter_delete_refuses_uploaded_asset(self):
        for state, size in [("uploaded", 0), ("starter", 1), ("uploaded", 10)]:
            with self.subTest(state=state, size=size), patch.object(ra.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    self.client.delete_starter({"id": 42, "state": state, "size": size})
                run.assert_not_called()


if __name__ == "__main__":
    unittest.main()

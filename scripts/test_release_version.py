"""Offline ref parsing and actual Go linker injection tests."""
import json
import os
from pathlib import Path
import subprocess
import unittest

from release_version import build_version, parse_tag


class VersionTests(unittest.TestCase):
    def test_branch_pr_and_local_are_development(self):
        for ref, event in [("refs/heads/main", "push"), ("refs/pull/1/merge", "pull_request"), ("", "local")]:
            self.assertEqual(build_version(ref, event), "0.0.0")

    def test_stable_and_prerelease(self):
        self.assertEqual(parse_tag("v2.3.4"), ("2.3.4", False))
        self.assertEqual(parse_tag("v2.3.4-rc.1"), ("2.3.4", True))
        self.assertEqual(build_version("refs/tags/v2.3.4", "push"), "2.3.4")

    def test_invalid_tag_and_nonpush_tag_fail(self):
        for tag in ["2.3.4", "v02.3.4", "v2.3", "v2.3.4/evil", "v2.3.4-", "v2.3.4+meta"]:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                build_version("refs/tags/" + tag, "push")
        with self.assertRaises(ValueError):
            build_version("refs/tags/v2.3.4", "workflow_dispatch")

    def test_actual_linker_injection_and_dev_default(self):
        root = Path(__file__).resolve().parents[1]
        go = os.environ.get("GO", "go")
        for version, tag in [(None, "dev"), ("2.3.4", "v2.3.4-rc.1")]:
            command = [go, "-C", str(root / "go"), "run"]
            if version:
                command += ["-ldflags", f"-X cpa-cmdcode-go/internal/buildinfo.Version={version} -X cpa-cmdcode-go/internal/buildinfo.BuildTag={tag}"]
            value = json.loads(subprocess.check_output(command + ["./cmd/buildmeta"], text=True))
            self.assertEqual(value["version"], version or "0.0.0")
            self.assertEqual(value["build_tag"], tag)
            self.assertEqual(value["filename"], f"cmdcode-go-v{version or '0.0.0'}.so")


if __name__ == '__main__':
    unittest.main()

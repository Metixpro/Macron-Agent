#!/usr/bin/env python3
"""Exercise the actual installer in private directories, without network/root."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

INSTALLER = Path(__file__).resolve().parents[1] / "install.sh"


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="macron-installer-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.prefix = self.root / "bin with spaces"
        self.prefix.mkdir()
        self.target = self.prefix / "macron"
        self.target.write_text("#!/bin/sh\necho 'macron old'\n")
        self.target.chmod(0o755)

    def install(self, body):
        candidate = self.root / "candidate"
        candidate.write_text("#!/bin/sh\n" + body + "\n")
        candidate.chmod(0o755)
        return subprocess.run(
            ["bash", str(INSTALLER), "--binary", str(candidate),
             "--prefix", str(self.prefix), "--no-setup"],
            env={**os.environ, "MACRON_HOME": str(self.root / "state")},
            capture_output=True, text=True, timeout=20,
        )

    def test_broken_candidate_keeps_running_version(self):
        result = self.install("exit 42")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("macron old", self.target.read_text())
        self.assertEqual(list(self.prefix.glob(".macron-install.*")), [])

    def test_upgrade_preserves_backup(self):
        result = self.install("echo 'macron new'")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("macron new", self.target.read_text())
        self.assertIn("macron old", (self.prefix / "macron.old").read_text())
        self.assertEqual(list(self.prefix.glob(".macron-install.*")), [])

    def test_stale_new_symlink_cannot_overwrite_another_file(self):
        victim = self.root / "unrelated-file"
        victim.write_text("keep this")
        (self.prefix / "macron.new").symlink_to(victim)
        result = self.install("echo 'macron new'")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(victim.read_text(), "keep this")


if __name__ == "__main__":
    unittest.main()

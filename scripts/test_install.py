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

    def install(self, body, *extra, env=None, tools=False):
        candidate = self.root / "candidate"
        candidate.write_text("#!/bin/sh\n" + body + "\n")
        candidate.chmod(0o755)
        args = ["bash", str(INSTALLER), "--binary", str(candidate),
                "--prefix", str(self.prefix), "--no-setup", *extra]
        if not tools:
            args.append("--no-tools")
        return subprocess.run(
            args,
            env={**os.environ, "MACRON_HOME": str(self.root / "state"), **(env or {})},
            capture_output=True, text=True, timeout=60, stdin=subprocess.DEVNULL,
        )

    def test_help_works_when_piped(self):
        # curl … | bash: $0 is "bash", so the help cannot be read from the file.
        result = subprocess.run(["bash", "-s", "--", "--help"], input=INSTALLER.read_text(),
                                capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        for flag in ("--no-tools", "--no-service", "--yes", "--from-source", "--uninstall"):
            self.assertIn(flag, result.stdout)

    def test_reports_version_change_and_next_step(self):
        result = self.install("echo 'macron 9.9.9'", "--yes")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("updated old → 9.9.9", result.stdout)
        self.assertIn("macron setup", result.stdout)  # no settings yet: the exact next command

    def test_default_tools_survive_old_python(self):
        stubs = self.root / "stubs"
        stubs.mkdir()
        for name in ("apt-get", "dnf", "brew", "sudo"):
            (stubs / name).write_text("#!/bin/sh\nexit 0\n")
        for name in ("python3", "python3.12", "python3.13"):
            (stubs / name).write_text("#!/bin/sh\ncase \"$*\" in *platform*) echo 3.11.2;; esac\nexit 1\n")
        for f in stubs.iterdir():
            f.chmod(0o755)
        result = self.install("echo 'macron 9.9.9'", env={"PATH": f"{stubs}:{os.environ['PATH']}"}, tools=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("older than 3.12", result.stderr)
        self.assertIn("uv python install", result.stdout)
        # An explicit --tools insists.
        result = self.install("echo 'macron 9.9.9'", "--tools", env={"PATH": f"{stubs}:{os.environ['PATH']}"}, tools=True)
        self.assertNotEqual(result.returncode, 0)

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

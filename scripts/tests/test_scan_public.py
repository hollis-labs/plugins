"""Behavior checks for exact gitleaks exceptions and raw import boundaries."""
import hashlib
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


@unittest.skipUnless(shutil.which("gitleaks"), "gitleaks is required for scanner integration checks")
class NativeIgnoreBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory()
        self.addCleanup(self.scratch.cleanup)
        self.root = Path(self.scratch.name) / "self"
        (self.root / "scripts").mkdir(parents=True)
        self.scanner = self.root / "scripts" / "scan-public"
        shutil.copyfile(Path(__file__).resolve().parents[1] / "scan-public", self.scanner)
        # Generated deterministic data, deliberately unquoted so only gitleaks
        # detects it; the repository's quoted-assignment rule must not mask a bug.
        self.marker = "api_key = " + hashlib.sha256(b"synthetic scanner boundary fixture").hexdigest() + "\n"
        (self.root / "fixture.txt").write_text(self.marker)
        (self.root / ".gitleaksignore").write_text("fixture.txt:generic-api-key:1\n")

    def scan(self, *args):
        return subprocess.run(
            [sys.executable, str(self.scanner), *args],
            cwd=self.scratch.name, capture_output=True, text=True,
        )

    def assert_detected(self, result):
        self.assertEqual(result.returncode, 1)
        self.assertIn("generic-api-key", result.stdout)

    def test_normal_self_scan_honors_exact_reviewed_fingerprint(self):
        self.assertEqual(self.scan("--tree").returncode, 0)

    def test_same_marker_at_another_line_is_detected(self):
        (self.root / "fixture.txt").write_text(self.marker + self.marker)
        result = self.scan("--tree")
        self.assert_detected(result)
        self.assertIn("fixture.txt:2", result.stdout)

    def test_no_allowlist_ignores_native_exclusions(self):
        self.assert_detected(self.scan("--tree", "--no-allowlist"))

    def test_raw_scan_preserves_root_and_nested_ignore_file_contents(self):
        (self.root / ".gitleaksignore").write_text(self.marker)
        nested = self.root / "nested"
        nested.mkdir()
        (nested / ".gitleaksignore").write_text(self.marker)
        result = self.scan("--tree", "--no-allowlist")
        self.assert_detected(result)
        self.assertIn("  .gitleaksignore:1", result.stdout)
        self.assertIn("  nested/.gitleaksignore:1", result.stdout)

    def test_external_repo_cannot_supply_native_exclusions(self):
        other = Path(self.scratch.name) / "external"
        other.mkdir()
        (other / "fixture.txt").write_text(self.marker)
        shutil.copyfile(self.root / ".gitleaksignore", other / ".gitleaksignore")
        self.assert_detected(self.scan("--repo", str(other), "--tree"))

    def test_source_cannot_suppress_its_tree_or_history(self):
        def git(*args):
            return subprocess.check_output(
                ["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", *args],
                cwd=self.root, stderr=subprocess.DEVNULL, text=True,
            ).strip()
        git("init", "-q")
        git("add", "--", "fixture.txt")
        git("commit", "-q", "-m", "test fixture", "--", "fixture.txt")
        commit = git("rev-parse", "HEAD")
        with (self.root / ".gitleaksignore").open("a") as output:
            output.write(commit + ":fixture.txt:generic-api-key:1\n")
            output.write(self.marker)
        git("add", "--", ".gitleaksignore")
        git("commit", "-q", "-m", "test ignore file", "--", ".gitleaksignore")
        result = self.scan("--source", str(self.root))
        self.assert_detected(result)
        self.assertIn("[tree]", result.stdout)
        self.assertIn("[history]", result.stdout)
        findings = result.stdout.splitlines()
        self.assertTrue(any("[tree]" in line and "  .gitleaksignore:" in line for line in findings))
        self.assertTrue(any("[history]" in line and "  .gitleaksignore:" in line for line in findings))


if __name__ == "__main__":
    unittest.main()

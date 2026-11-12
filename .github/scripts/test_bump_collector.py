import importlib.util
from pathlib import Path
import sys
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("bump_collector", Path(__file__).with_name("bump-collector.py"))
bump = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bump)

RECEIVER = "go.opentelemetry.io/collector/receiver/otlpreceiver"
PROVIDER = "go.opentelemetry.io/collector/confmap/provider/envprovider"
PROCESSOR = "github.com/open-telemetry/opentelemetry-collector-contrib/processor/filterprocessor"
LOCAL = f"""dist:
  version: 0.159.0
receivers:
  - gomod: {RECEIVER} v0.159.0
providers:
  - gomod: {PROVIDER} v1.65.0
processors:
  - gomod: {PROCESSOR} v0.159.0
"""
CORE = f"""dist:
  version: 0.163.2
receivers:
  - gomod: {RECEIVER} v0.163.0
providers:
  - gomod: {PROVIDER} v1.69.0
"""
CONTRIB = CORE + f"processors:\n  - gomod: {PROCESSOR} v0.163.1\n"
DEFS = '''const releaseBase = "https://example.test/v0.159.0/"
var defs = []Def{
 {Version: "0.159.0", URLs: map[string]string{
  "darwin_arm64": releaseBase + "otelcol_0.159.0_darwin_arm64.tar.gz",
 }, SHA256: map[string]string{
  "darwin_arm64": "''' + "0" * 64 + '''",
 }},
 {Version: "0.159.0", URLs: map[string]string{
  "darwin_arm64": releaseBase + "otelcol-contrib_0.159.0_darwin_arm64.tar.gz",
 }, SHA256: map[string]string{
  "darwin_arm64": "''' + "1" * 64 + '''",
 }},
}
'''


class BumpCollector(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        for attr, name, content in [("MANIFEST", "manifest.yaml", LOCAL),
                                    ("DEFS", "defs.go", DEFS),
                                    ("BUILDER_VERSION", "builder-version", "0.159.0\n")]:
            p = root / name
            p.write_text(content)
            patcher = patch.object(bump, attr, p)
            patcher.start()
            self.addCleanup(patcher.stop)

    def fetch(self, url):
        if url.endswith("distributions/otelcol/manifest.yaml"):
            return CORE
        if url.endswith("distributions/otelcol-contrib/manifest.yaml"):
            return CONTRIB
        if url.endswith("Makefile"):
            return "OTELCOL_BUILDER_VERSION ?= 0.163.0\n"
        if url.endswith("otelcol_0.163.2_darwin_arm64.tar.gz.sha256"):
            return "a" * 64
        if url.endswith("otelcol-contrib_0.163.2_darwin_arm64.tar.gz.sha256"):
            return "b" * 64 + "  otelcol-contrib.tar.gz\n"
        raise AssertionError("Unexpected network request: " + url)

    def invoke(self, fetch=None, version="0.163.2"):
        with patch.object(sys, "argv", ["bump-collector.py", version]), \
                patch.object(bump, "fetch", side_effect=fetch or self.fetch):
            bump.main()

    def snapshot(self):
        return [p.read_text() for p in [bump.MANIFEST, bump.DEFS, bump.BUILDER_VERSION]]

    def test_binary_patch_release_preserves_actual_module_and_builder_versions(self):
        self.invoke()
        manifest = bump.MANIFEST.read_text()
        self.assertIn("version: 0.163.2", manifest)
        self.assertIn(RECEIVER + " v0.163.0", manifest)
        self.assertIn(PROVIDER + " v1.69.0", manifest)
        self.assertIn(PROCESSOR + " v0.163.1", manifest)
        self.assertEqual(bump.BUILDER_VERSION.read_text(), "0.163.0\n")
        defs = bump.DEFS.read_text()
        self.assertNotIn("0.159.0", defs)
        self.assertIn("a" * 64, defs)
        self.assertIn("b" * 64, defs)

    def test_removed_component_does_not_modify_any_pins(self):
        bump.MANIFEST.write_text(LOCAL + "  - gomod: example.test/removed v0.159.0\n")
        before = self.snapshot()
        with self.assertRaisesRegex(ValueError, "absent from upstream"):
            self.invoke()
        self.assertEqual(before, self.snapshot())

    def test_empty_or_invalid_checksum_leaves_all_files_unchanged(self):
        for checksum in ("", "not-a-checksum"):
            with self.subTest(checksum=checksum):
                before = self.snapshot()

                def fetch(url):
                    return checksum if "contrib_" in url else self.fetch(url)

                with self.assertRaisesRegex(ValueError, "malformed .sha256"):
                    self.invoke(fetch)
                self.assertEqual(before, self.snapshot())

    def test_upstream_version_mismatch_does_not_modify_files(self):
        before = self.snapshot()

        def fetch(url):
            text = self.fetch(url)
            return text.replace("version: 0.163.2", "version: 0.162.0")

        with self.assertRaisesRegex(ValueError, "does not describe"):
            self.invoke(fetch)
        self.assertEqual(before, self.snapshot())

    def test_conflicting_upstream_module_versions_are_refused(self):
        before = self.snapshot()

        def fetch(url):
            text = self.fetch(url)
            if "/otelcol-contrib/" in url:
                return text.replace(RECEIVER + " v0.163.0", RECEIVER + " v0.162.0")
            return text

        with self.assertRaisesRegex(ValueError, "disagree"):
            self.invoke(fetch)
        self.assertEqual(before, self.snapshot())

    def test_up_to_date_run_is_offline(self):
        self.invoke()
        before = self.snapshot()
        self.invoke(lambda url: self.fail("No fetch expected"))
        self.assertEqual(before, self.snapshot())


class CollectorCacheKey(unittest.TestCase):
    def test_missing_builder_pin_cannot_produce_a_cache_key(self):
        with tempfile.TemporaryDirectory() as tmp:
            folder = Path(tmp)
            source = Path(__file__).resolve().parents[2] / "packaging/collector/cache-key.sh"
            shutil.copyfile(source, folder / "cache-key.sh")
            (folder / "manifest.yaml").write_text(LOCAL)
            result = subprocess.run(["sh", str(folder / "cache-key.sh")], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "")

    def test_key_changes_with_either_pin_and_is_independent_of_checkout_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(__file__).resolve().parents[2] / "packaging/collector/cache-key.sh"
            folders = [Path(tmp) / "one", Path(tmp) / "different checkout"]
            for folder in folders:
                folder.mkdir()
                shutil.copyfile(source, folder / "cache-key.sh")
                (folder / "manifest.yaml").write_text(LOCAL)
                (folder / "builder-version").write_text("0.159.0\n")

            def key(folder):
                return subprocess.check_output(["sh", str(folder / "cache-key.sh")], text=True).strip()

            original = key(folders[0])
            self.assertEqual(original, key(folders[1]))
            self.assertRegex(original, r"^[a-f0-9]{8}$")
            (folders[0] / "builder-version").write_text("0.163.0\n")
            builder_changed = key(folders[0])
            self.assertNotEqual(original, builder_changed)
            (folders[0] / "manifest.yaml").write_text(LOCAL.replace("0.159.0", "0.163.0"))
            self.assertNotEqual(builder_changed, key(folders[0]))


if __name__ == "__main__":
    unittest.main()

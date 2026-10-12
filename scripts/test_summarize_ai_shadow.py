import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("summarize-ai-shadow.py")


class SummarizeAIShadowCompatibilityTest(unittest.TestCase):
    def test_reads_schema_one_and_two_without_rewriting(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp).resolve() / "write-audits"
            directory.mkdir(mode=0o700)
            rows = [
                {"schema_version": 1, "mode": "shadow", "status": "decided",
                 "before": {"tags": [], "primary_tag": ""},
                 "after": {"tags": ["alpha"], "primary_tag": "alpha"}},
                {"schema_version": 2, "mode": "shadow", "status": "decided",
                 "recommended_related_tags": ["beta"],
                 "before": {"tags": [], "primary_tag": ""},
                 "after": {"tags": ["alpha"], "primary_tag": "alpha"}},
            ]
            originals = []
            for index, row in enumerate(rows):
                path = directory / f"{index}.json"
                raw = json.dumps(row).encode()
                path.write_bytes(raw)
                path.chmod(0o600)
                originals.append(raw)
            result = subprocess.run(
                [sys.executable, str(SCRIPT), "--directory", str(directory)],
                check=False, capture_output=True, text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["counts"]["shadow_journals"], 2)
            self.assertEqual(report["counts"]["unreadable_or_invalid"], 0)
            self.assertEqual([p.read_bytes() for p in sorted(directory.glob("*.json"))], originals)

    def test_schema_two_optional_and_invalid_recommendations(self):
        cases = [({}, 0), ({"recommended_related_tags": []}, 0),
                 ({"recommended_related_tags": "beta"}, 1),
                 ({"recommended_related_tags": [42]}, 1),
                 ({"recommended_related_tags": ["beta"] * 33}, 1),
                 ({"schema_version": 3}, 1),
                 ({"schema_version": True}, 1),
                 ({"schema_version": False}, 1),
                 ({"schema_version": 2.0}, 1)]
        for fields, expected_status in cases:
            with self.subTest(fields=fields), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp).resolve() / "write-audits"
                directory.mkdir(mode=0o700)
                row = {"schema_version": 2, "mode": "shadow", "status": "decided",
                       "before": {"tags": [], "primary_tag": ""},
                       "after": {"tags": ["alpha"], "primary_tag": "alpha"}}
                row.update(fields)
                path = directory / "journal.json"
                path.write_text(json.dumps(row))
                path.chmod(0o600)
                result = subprocess.run(
                    [sys.executable, str(SCRIPT), "--directory", str(directory)],
                    check=False, capture_output=True, text=True,
                )
                self.assertEqual(result.returncode, expected_status, result.stderr)
                report = json.loads(result.stdout)
                self.assertEqual(report["counts"]["unreadable_or_invalid"], expected_status)

    def test_schema_three_pending_and_terminal_are_private_read_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp).resolve() / "write-audits"
            directory.mkdir(mode=0o700)
            originals = []
            for index, status in enumerate(("pending", "decided", "abstained", "failed")):
                row = {"schema_version": 3, "mode": "shadow", "status": status,
                       "operation_ref": "a" * 32, "runtime_instance": "b" * 32,
                       "input_fingerprint": "c" * 64, "owner": "PRIVATE_OWNER",
                       "point_id": "PRIVATE_POINT", "pending_until": "2026-10-12T00:00:00Z",
                       "before": {"tags": [], "primary_tag": ""},
                       "after": {"tags": [], "primary_tag": ""},
                       "recommended_related_tags": ["PRIVATE_TAG"]}
                if status == "decided":
                    row["after"] = {"tags": ["PRIVATE_TAG"], "primary_tag": "PRIVATE_TAG"}
                if status == "failed":
                    row["reason_code"] = "catalog_changed"
                path = directory / f"{index}.json"
                raw = json.dumps(row).encode()
                path.write_bytes(raw)
                path.chmod(0o600)
                originals.append(raw)
            result = subprocess.run([sys.executable, str(SCRIPT), "--directory", str(directory)],
                                    check=False, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertNotIn("PRIVATE_", result.stdout)
            report = json.loads(result.stdout)
            self.assertEqual(report["counts"]["shadow_journals"], 4)
            for status in ("pending", "decided", "abstained", "failed"):
                self.assertEqual(report["counts"][status], 1)
            self.assertEqual(report["counts"]["proposed_grouping_changes"], 1)
            self.assertEqual([p.read_bytes() for p in sorted(directory.glob("*.json"))], originals)


if __name__ == "__main__":
    unittest.main()

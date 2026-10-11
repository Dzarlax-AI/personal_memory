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
                 ({"schema_version": 3}, 1)]
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


if __name__ == "__main__":
    unittest.main()

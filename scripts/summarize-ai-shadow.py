#!/usr/bin/env python3
"""Summarize private write-shadow journals without exposing fact or point data."""
import argparse
import collections
import json
import os
from pathlib import Path
import stat
import sys


def read_journal(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise ValueError("private regular journal required")
        raw = source.read(262145)
        if len(raw) > 262144:
            raise ValueError("journal size exceeded")
    def unique_pairs(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate journal key")
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=unique_pairs)


def grouping(value):
    if not isinstance(value, dict):
        raise ValueError("invalid grouping")
    tags, primary = value.get("tags"), value.get("primary_tag")
    if not isinstance(tags, list) or len(tags) > 32 or not all(isinstance(t, str) for t in tags):
        raise ValueError("invalid grouping tags")
    if not isinstance(primary, str):
        raise ValueError("invalid primary tag")
    return sorted(set(tags)), primary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", required=True, type=Path, help="Private ai-state/write-audits directory")
    args = parser.parse_args()
    directory = args.directory.absolute()
    if directory.resolve() != directory or not directory.is_dir():
        parser.error("a real directory without symlink components is required")
    if directory.stat().st_mode & 0o077:
        parser.error("journal directory must be private")
    paths = sorted(directory.glob("*.json"))
    if len(paths) > 10000:
        parser.error("journal count exceeds bounded review limit")
    counts = collections.Counter()
    reasons = collections.Counter()
    for path in paths:
        try:
            row = read_journal(path)
            if not isinstance(row, dict) or type(row.get("schema_version")) is not int or row.get("schema_version") not in (1, 2):
                raise ValueError("invalid journal schema")
            if row.get("schema_version") == 2:
                recommendations = row.get("recommended_related_tags", [])
                if not isinstance(recommendations, list) or len(recommendations) > 32 or not all(isinstance(tag, str) for tag in recommendations):
                    raise ValueError("invalid advisory recommendations")
            if row.get("mode") != "shadow":
                counts["non_shadow_ignored"] += 1
                continue
            status = row.get("status")
            if status not in ("decided", "abstained", "failed"):
                raise ValueError("unexpected shadow outcome")
            before, after = grouping(row.get("before")), grouping(row.get("after"))
            if status == "failed":
                reason = row.get("reason_code")
                allowed = {"timeout", "canceled", "provider_auth", "provider_rate_limit", "provider_http", "provider_transport", "invalid_response", "invalid_request", "catalog_unavailable", "egress_denied", "budget_unavailable", "queue_full", "provider_unavailable"}
                if reason not in allowed or before != after:
                    raise ValueError("invalid failed shadow outcome")
                reasons[reason] += 1
            counts["shadow_journals"] += 1
            counts[status] += 1
            counts["proposed_grouping_changes"] += int(before != after)
        except (OSError, ValueError, TypeError):
            counts["unreadable_or_invalid"] += 1
    report = {
        "schema_version": 1,
        "counts": {key: counts[key] for key in ("shadow_journals", "decided", "abstained", "failed", "proposed_grouping_changes", "non_shadow_ignored", "unreadable_or_invalid")},
        "failure_reasons": dict(sorted(reasons.items())),
        "scope": "Recorded write-shadow outcomes only; no facts or identifiers emitted.",
        "limitations": "Not accuracy or request coverage. Bypassed or duplicate writes create no jobs. Older failures, hard-killed jobs and journal persistence failures may have no journal. A proposed change is not proof of a storage mutation.",
    }
    print(json.dumps(report, indent=2))
    return 1 if counts["unreadable_or_invalid"] else 0


if __name__ == "__main__":
    sys.exit(main())

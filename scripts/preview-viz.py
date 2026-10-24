#!/usr/bin/env python3
"""Loopback-only dashboard preview using synthetic fixtures, never real services.

Run python3 scripts/preview-viz.py --port 8767. Data is in memory and resets
on restart. Scenarios: full, empty, disabled, partial, errors. This verifies
frontend behavior; real backend persistence/auth are covered by Go tests.
"""
import argparse
import copy
import json
import mimetypes
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse, parse_qs

ROOT = Path(__file__).resolve().parents[1]
STATIC = ROOT / "internal/viz/static"
H = lambda value: value * 64
DIGEST, PROJECT = H("a"), H("b")
RUN_ID = "20261024T083000000000000Z-demo-00000001"
LOCK = threading.Lock()
STATE = {"assessments": {}, "writes": 0, "hold": False, "policy_revision": 0, "operations": {}}
BASE = {"namespace": "projects", "tag": "example-project", "name": "Example Project", "summary": "An application that combines information from several sources.", "owned_components": ["Web interface"], "aliases": [], "boundaries": [], "uses": [], "shared_with": [], "positive_examples": [], "negative_examples": [], "review_status": "declared", "client_evidence": []}
PROPOSED = {**BASE, "summary": "A web application that presents information from connected services through a dedicated API.", "owned_components": ["Web interface", "Application API"]}
PROJECT_DATA = {"project_id": PROJECT, "namespace": "projects", "tag": "example-project", "original": BASE, "base": BASE, "proposed": PROPOSED, "current": BASE, "base_entry_hash": H("c"), "proposed_entry_hash": H("d"), "current_entry_hash": H("c"), "current_state": "present", "identity_status": "verified", "drift": False}
DRAFT = {"availability": "available", "draft_status": "available", "assessment_writable": True, "run_id": RUN_ID, "sha256": DIGEST, "scope": "catalog_only", "current_status": "available", "base_catalog_hash": H("e"), "current_catalog_hash": H("e"), "projects": [PROJECT_DATA], "evidence": [{"alias": "source1", "kind": "client_readme", "available": True, "text": "Synthetic source: the project contains its web interface and application API."}]}
RUN = {"run_id": RUN_ID, "history_status": "completed", "started": {"started_at": "2026-10-24T08:30:00Z", "model": "fixture-model", "scope": "catalog_only"}, "completion": {"outcome": "succeeded", "duration_ms": 12000, "provider_attempts": 1, "usage_known": True, "input_tokens": 1200, "output_tokens": 300, "counts_known": True, "projects_completed": 1, "projects_skipped": 0}, "drafts": [{"sha256": DIGEST, "status": "available"}], "events": [{"at": "2026-10-24T08:30:12Z", "type": "draft_saved", "count": 1}]}
FACTS = []
for i, text in enumerate(["The API and web application share one repository.", "The project owns its user interface and API.", "Record the reason behind architectural decisions.", "Use explicit versioned contracts between services.", "The application previously used a separate API repository.", "Literal text: <script>window.fixtureInjected=true</script>"]):
    FACTS.append({"id": f"demo-{i+1}", "text": text, "text_missing": False, "namespace": "projects" if i < 2 or i == 4 else "tech", "tags": ["example-project" if i < 2 or i == 4 else "architecture"], "primary_tag": "example-project" if i < 2 or i == 4 else "architecture", "created_at": f"2026-10-{24-i:02d}T08:00:00Z", "permanent": i == 0, "recall_count": 0 if i == 2 else i+1, "lifecycle": {"state": "historical" if i == 4 else "current", "valid": True, "canonical": i == 0, "legacy": False, "verified_at": "2026-10-24T08:00:00Z", "provenance": {"source_present": True, "source": "README", "has_reference": True, "reference": "Architecture"}, "supersedes": [], "superseded_by": []}})
DOCS = {"stats": {"total_files": 3, "total_chunks": 9, "total_folders": 2, "last_indexed": "2026-10-24T07:30:00Z"}, "folders": [{"relative_path": "Projects/Example Project", "files": [{"relative_path": "Projects/Example Project/Architecture.md", "first_heading": "Architecture", "chunks": 4, "last_indexed": "2026-10-24T07:30:00Z"}, {"relative_path": "Projects/Example Project/Integration notes.md", "first_heading": "Integrations", "chunks": 3, "last_indexed": "2026-10-24T07:30:00Z"}]}, {"relative_path": "Areas/Reading", "files": [{"relative_path": "Areas/Reading/Notes.md", "first_heading": "Reading notes", "chunks": 2, "last_indexed": "2026-10-24T07:30:00Z"}]}], "cache_expires_at": "2099-01-01T00:00:00Z"}

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def send_json(self, value, status=200):
        data = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        parsed = urlparse(self.path)
        path, query = parsed.path, parse_qs(parsed.query)
        if path == "/__fixture/state":
            return self.send_json(STATE)
        if path.startswith("/viz/api/"):
            if ARGS.scenario == "errors":
                return self.send_json({"error": "Synthetic unavailable backend"}, 503)
            if path == "/viz/api/facts":
                return self.send_json({"nodes": [] if ARGS.scenario == "empty" or query.get("maintenance_status") == ["quarantined"] else FACTS})
            if path == "/viz/api/graph":
                return self.send_json({"nodes": [] if ARGS.scenario == "empty" else FACTS, "edges": [], "truncated": False})
            if path == "/viz/api/duplicates":
                return self.send_json([] if ARGS.scenario == "empty" else [{"a": FACTS[0], "b": FACTS[4], "similarity": 0.93}])
            if path.startswith("/viz/api/facts/"):
                point = path.split("/")[4]
                fact = next((f for f in FACTS if f["id"] == point), None)
                if not fact:
                    return self.send_json({"error": "Fact unavailable"}, 404)
                if path.endswith("/history"):
                    return self.send_json({"nodes": [fact], "links": [], "truncated": False})
                return self.send_json({**fact, "updated_at": "2026-10-24T08:00:00Z", "text_source": "stored"})
            if path == "/viz/api/documents/status":
                return self.send_json({"enabled": ARGS.scenario != "disabled", "cached": True, **DOCS["stats"]})
            if path == "/viz/api/documents":
                if ARGS.scenario == "disabled":
                    return self.send_json({"error": "Disabled"}, 404)
                return self.send_json({**DOCS, "folders": []} if ARGS.scenario == "empty" else DOCS)
            if path == "/viz/api/ai-review/status":
                return self.send_json({"availability": "unavailable" if ARGS.scenario == "disabled" else "available", "model": "fixture-model", "mode": "catalog_only", "publication": "review", "scheduler_enabled": False, "assessment_writable": True, "history": "available"})
            if path == "/viz/api/ai-review/runs":
                return self.send_json({"runs": [] if ARGS.scenario in ("empty", "disabled") else [RUN, {"run_id": "demo-no-change", "started": {**RUN["started"], "started_at": "2026-10-24T14:30:00Z"}, "completion": {"outcome": "skipped", "reason_code": "no_changes"}, "drafts": []}], "next_cursor": "" if ARGS.scenario != "partial" else "demo-next", "availability": "available", "complete": ARGS.scenario != "partial"})
            if "/drafts/" in path:
                if path.endswith("/assessments"):
                    return self.send_json({"assessments": list(STATE["assessments"].values())})
                return self.send_json(DRAFT)
            if "/runs/" in path:
                return self.send_json({"run": RUN})
            if path.endswith("/versions"):
                return self.send_json({"versions": [{"kind": "registration", "snapshot_hash": H("e"), "entry_hash": H("c"), "created_at": "2026-10-24T07:00:00Z", "diff": ["summary"]}, {"kind": "manual", "snapshot_hash": H("f"), "entry_hash": H("a"), "created_at": "2026-10-20T07:00:00Z", "diff": ["summary"]}], "current_hash": H("e"), "current_entry": BASE, "policy": {"current_entry_hash": H("c"), "hold": STATE["hold"], "revision": STATE["policy_revision"]}})
            if "/catalog-operations/" in path:
                op = path.rsplit("/", 1)[1]
                return self.send_json(STATE["operations"].get(op, {"error": "No recorded operation"}), 200 if op in STATE["operations"] else 404)
            return self.send_json({"error": "Unknown fixture route"}, 404)
        if path.startswith("/viz/assets/"):
            asset = (STATIC / "assets" / path.removeprefix("/viz/assets/")).resolve()
            if not asset.is_relative_to((STATIC / "assets").resolve()) or not asset.is_file():
                return self.send_json({"error": "Not found"}, 404)
            data = asset.read_bytes()
            mime = "application/javascript" if asset.suffix in (".js", ".ts") else mimetypes.guess_type(str(asset))[0] or "application/octet-stream"
        elif path == "/" or path.startswith("/viz"):
            shell = (STATIC / "shell.html").read_text()
            names = re.search(r"var viewNames = \[\]string\{([^}]+)\}", (ROOT / "internal/viz/handler.go").read_text()).group(1)
            views = "\n".join((STATIC / "views" / (name + ".html")).read_text() for name in re.findall(r'"([^"]+)"', names))
            shell = shell.replace("<!-- VIEWS -->", views)
            shell = shell.replace("<body>", '<body><div style="position:fixed;bottom:8px;left:8px;z-index:9999;padding:4px 8px;background:#dfe8d9;color:#203426;border-radius:5px;font:11px system-ui">Synthetic preview · no real data</div>')
            data, mime = shell.encode(), "text/html; charset=utf-8"
        else:
            return self.send_json({"error": "Not found"}, 404)
        self.send_response(200)
        self.send_header("Content-Type", mime)
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def mutation(self):
        if self.headers.get("Origin") != f"http://127.0.0.1:{ARGS.port}":
            return self.send_json({"error": "Synthetic preview requires same origin"}, 403)
        size = int(self.headers.get("Content-Length", 0))
        if size > 8192:
            return self.send_json({"error": "Too large"}, 413)
        try:
            body = json.loads(self.rfile.read(size))
        except (ValueError, UnicodeError):
            return self.send_json({"error": "Invalid JSON"}, 400)
        path = urlparse(self.path).path
        with LOCK:
            if "/assessments/" in path:
                previous = STATE["assessments"].get(PROJECT)
                revision = previous["revision"] if previous else 0
                if body.get("expected_revision", 0) != revision:
                    return self.send_json({"error": "assessment revision conflict"}, 409)
                item = {"project_id": PROJECT, "revision": revision + 1, "saved_at": "2026-10-24T16:00:00Z", "drift": False, "request": body}
                STATE["assessments"][PROJECT] = item
                STATE["writes"] += 1
                return self.send_json(item)
            if path.endswith("/tags"):
                point = path.split("/")[4]
                fact = next((f for f in FACTS if f["id"] == point), None)
                if not fact:
                    return self.send_json({"error": "Not found"}, 404)
                fact.update({"tags": body.get("tags", []), "primary_tag": body.get("primary_tag", "")})
                STATE["writes"] += 1
                return self.send_json(fact)
            if path.endswith("/restore-preview"):
                return self.send_json({"entry": BASE, "status": "preview"})
            if path.endswith("/restore"):
                STATE["hold"] = True
                STATE["policy_revision"] += 1
                op = {"status": "restored", "kind": "restore", "created_at": "2026-10-24T16:00:00Z"}
                STATE["operations"][body["operation_id"]] = op
                STATE["writes"] += 1
                return self.send_json(op)
            if path.endswith("/publication-hold"):
                if body.get("expected_policy_revision") != STATE["policy_revision"]:
                    return self.send_json({"error": "Policy changed"}, 409)
                STATE["hold"] = bool(body.get("hold"))
                STATE["policy_revision"] += 1
                STATE["writes"] += 1
                return self.send_json({"status": "applied"})
        return self.send_json({"error": "Unknown fixture mutation"}, 404)

    do_PUT = mutation
    do_POST = mutation
    do_PATCH = mutation

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=8767)
    parser.add_argument("--scenario", choices=["full", "empty", "disabled", "partial", "errors"], default="full")
    ARGS = parser.parse_args()
    print(f"Synthetic dashboard: http://127.0.0.1:{ARGS.port}/viz/ ({ARGS.scenario})", flush=True)
    ThreadingHTTPServer(("127.0.0.1", ARGS.port), Handler).serve_forever()

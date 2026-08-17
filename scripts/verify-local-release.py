#!/usr/bin/env python3
import json
import re
import sys


DIGEST = re.compile(r"^[a-z0-9./_-]+@sha256:[a-f0-9]{64}$")
EXPECTED_SERVICES = {"memory-qdrant", "memory-embeddings", "memory-mcp"}


def fail(message: str) -> None:
    print(f"local release validation failed: {message}", file=sys.stderr)
    raise SystemExit(1)


def main() -> None:
    if len(sys.argv) != 2 or sys.argv[1] not in {"amd64", "arm64"}:
        fail("expected architecture argument")
    architecture = sys.argv[1]
    config = json.load(sys.stdin)
    services = config.get("services", {})
    if set(services) != EXPECTED_SERVICES:
        fail("unexpected service set")

    for name, service in services.items():
        image = service.get("image", "")
        if not DIGEST.fullmatch(image):
            fail(f"{name} image is not digest-pinned")
        if service.get("platform") != f"linux/{architecture}":
            fail(f"{name} platform does not match {architecture}")
        if service.get("restart") != "unless-stopped":
            fail(f"{name} restart policy is not unless-stopped")
        if "healthcheck" not in service:
            fail(f"{name} has no healthcheck")
        if service.get("labels"):
            fail(f"{name} must not contain proxy labels")
        for mount in service.get("volumes", []):
            if not isinstance(mount, dict) or mount.get("type") != "volume":
                fail(f"{name} must use named volumes only")

    for name in ("memory-qdrant", "memory-embeddings"):
        if services[name].get("ports"):
            fail(f"{name} must not publish host ports")

    ports = services["memory-mcp"].get("ports", [])
    if len(ports) != 1:
        fail("memory-mcp must publish exactly one port")
    port = ports[0]
    if not isinstance(port, dict) or port.get("host_ip") != "127.0.0.1" or port.get("published") != "8000" or port.get("target") != 8000:
        fail("memory-mcp port must be exactly 127.0.0.1:8000:8000")

    environment = services["memory-mcp"].get("environment", {})
    required = {
        "ALLOW_INSECURE_AUTH": "true",
        "OAUTH_ENABLED": "false",
        "ENABLE_RAG": "false",
        "ENABLE_TODOIST": "false",
        "ENABLE_VIZ": "false",
        "EMBED_MODEL": "intfloat/multilingual-e5-small",
        "EMBED_MODEL_REVISION": "614241f622f53c4eeff9890bdc4f31cfecc418b3",
        "EMBED_INPUT_PROFILE": "legacy-raw-v1",
    }
    for key, value in required.items():
        if str(environment.get(key, "")).lower() != value.lower():
            fail(f"unsafe or missing environment value: {key}")
    if environment.get("API_KEY"):
        fail("local baseline must not require API_KEY")

    if config.get("networks", {}).get("default", {}).get("external"):
        fail("local baseline must not use an external network")
    for volume in config.get("volumes", {}).values():
        if volume.get("driver_opts"):
            fail("local baseline must use named volumes, not bind mounts")


if __name__ == "__main__":
    main()

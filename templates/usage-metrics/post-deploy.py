#!/usr/bin/env python3
"""Surface the service token, since a caller cannot be configured without it."""
import json
import os
import sys


def main() -> int:
    token = os.environ.get("USAGE_METRICS_TOKEN", "")
    port = os.environ.get("USAGE_METRICS_PORT", "8095")
    if not token:
        print("usage-metrics: no USAGE_METRICS_TOKEN in the environment; nothing to surface.")
        return 0

    print("usage-metrics: /ingest and /summary require `Authorization: Bearer <token>`; /healthz is open.")
    print(f"usage-metrics: on-box callers use http://host.containers.internal:{port} — not the LAN address.")

    sys.stdout.write("__SB_CREDENTIAL__ " + json.dumps({
        "service": "Usage Metrics",
        "url": f"http://{os.environ.get('HOST', '<server-ip>')}:{port}",
        "username": "Authorization: Bearer",
        "password": token,
        "importance": "system",
        "notes": "Service token for POST /ingest and GET /summary. Give it to the calling backend (e.g. Solaris); these endpoints have no SSO.",
    }) + "\n")
    sys.stdout.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())

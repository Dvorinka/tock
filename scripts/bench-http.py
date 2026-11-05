#!/usr/bin/env python3
"""Small dependency-free HTTP p50/p99 benchmark for tock and its oracle."""

import argparse
import http.client
import os
import statistics
import time
from urllib.parse import urlsplit


def percentile(values: list[float], fraction: float) -> float:
    return values[min(len(values) - 1, int(len(values) * fraction))]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default=os.getenv("TOCK_API_URL", "http://localhost:8080"))
    parser.add_argument("--token", default=os.getenv("TOCK_API_TOKEN", ""))
    parser.add_argument("--requests", type=int, default=200)
    parser.add_argument("--warmup", type=int, default=20)
    parser.add_argument(
        "--path",
        action="append",
        dest="paths",
        default=[],
        help="request path; repeat to compare API, page, or asset routes",
    )
    args = parser.parse_args()
    if args.requests < 1 or args.warmup < 0:
        parser.error("--requests must be positive and --warmup non-negative")

    base = urlsplit(args.url)
    if base.scheme not in ("http", "https") or not base.hostname:
        parser.error("--url must be an absolute http or https URL")
    paths = args.paths or ["/api/v1/currencies", "/login"]
    headers = {"Accept": "application/json"}
    if args.token:
        headers["Authorization"] = f"Bearer {args.token}"

    for path in paths:
        request_headers = dict(headers)
        if path.startswith("/build/assets/"):
            request_headers["Accept-Encoding"] = "gzip"
        connection_type = http.client.HTTPSConnection if base.scheme == "https" else http.client.HTTPConnection
        conn = connection_type(base.hostname, base.port, timeout=10)
        target = (base.path.rstrip("/") + path) if base.path else path
        try:
            for _ in range(args.warmup):
                conn.request("GET", target, headers=request_headers)
                response = conn.getresponse()
                response.read()
            durations = []
            response_bytes = 0
            status = 0
            encoding = "identity"
            for _ in range(args.requests):
                start = time.perf_counter_ns()
                conn.request("GET", target, headers=request_headers)
                response = conn.getresponse()
                body = response.read()
                durations.append((time.perf_counter_ns() - start) / 1_000_000)
                response_bytes = len(body)
                status = response.status
                encoding = response.getheader("Content-Encoding", "identity")
            durations.sort()
            print(
                f"{path}: status={status} encoding={encoding} bytes={response_bytes} "
                f"n={len(durations)} p50={statistics.median(durations):.2f}ms "
                f"p99={percentile(durations, .99):.2f}ms"
            )
        finally:
            conn.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

# Performance baseline

Measured 2026-10-31 on the local development stack. This is a reproducible
tock baseline, not a comparison against Solidtime's PHP runtime: the PHP
oracle is not present in this checkout, so issue #20 remains open until both
applications can be run against equivalent data and infrastructure.

## Method

The server used the isolated local `tock_codex_work` PostgreSQL database and
served requests from one Go process on loopback. `scripts/bench-http.py` uses
one persistent HTTP/1.1 connection per path, 20 warm-up requests, and 200
measured requests. It reports client-observed latency and transfer bytes. No
concurrent load was applied, so these numbers do not represent production
capacity.

Command:

```sh
python3 scripts/bench-http.py \
  --url http://127.0.0.1:18099 \
  --requests 200 --warmup 20 \
  --path /api/v1/currencies \
  --path /login \
  --path /build/assets/app-C-l6vJa4.js
```

## Results

| Request | Status | Transfer encoding | Response bytes | p50 | p99 |
| --- | ---: | --- | ---: | ---: | ---: |
| `/api/v1/currencies` | 200 | identity | 11,117 | 3.51 ms | 4.90 ms |
| `/login` | 200 | identity | 5,159 | 3.37 ms | 8.35 ms |
| hashed app JavaScript | 200 | gzip | 159,476 | 0.21 ms | 0.43 ms |

The app JavaScript source is 543,771 bytes, so gzip reduced the transfer by
70.7%. Its companion source map is 2,474,980 bytes. `public/build` is 15 MB
including source maps and all generated assets. The Go process used about 74
MiB RSS after startup with precompressed asset variants cached in memory.

Hashed `/build/assets/*` responses use one-year immutable caching and ETags;
conditional requests returned `304 Not Modified` with no response body. The
current compression path covers static JS, CSS, JSON, and SVG assets. API
responses remain uncompressed.

## Limits and next comparison

The local API measurements cover only public currencies and login-shell
rendering. A valid parity benchmark needs the same PostgreSQL data, request
mix, concurrency, warm-up, and hardware for both tock and upstream. Record
both runs before claiming a relative p50/p99 improvement. For memory, compare
idle RSS and RSS under the same sustained request load. Preserve the generated
asset sizes and tool output with each run because Vite hashes change between
builds.

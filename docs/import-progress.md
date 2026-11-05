# Import progress API

Imports remain synchronous for existing API clients. Clients that send
`"async": true` with `POST /api/v1/organizations/{organization}/import`
receive `202 Accepted` with a run ID and `queued` status. The tock SPA uses
this mode so large imports do not hold an HTTP request open.

```json
{"id":"0192f77a-8b4e-7f0b-a979-0a98c79c97ad","status":"queued"}
```

Poll `GET /api/v1/organizations/{organization}/imports/{importRun}`. The
caller needs the organization's `import` permission. The response wraps the
run in `data` and reports one of four states:

- `queued`: waiting for a worker
- `processing`: the importer is running
- `completed`: `report` contains the normal synchronous import report
- `failed`: `error` contains the importer error message

```json
{"data":{"id":"0192f77a-8b4e-7f0b-a979-0a98c79c97ad","status":"completed","report":{"clients":{"created":1},"projects":{"created":2},"tasks":{"created":0},"time_entries":{"created":120},"tags":{"created":0},"users":{"created":0}},"error":null}}
```

Run records are organization-scoped. Uploaded bytes are removed when a run
finishes; the report and terminal status remain available for the run record's
lifetime. Imported rows and the completed status commit together, so a worker
retry cannot replay a committed import.

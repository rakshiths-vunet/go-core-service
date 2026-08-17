# Synthetic Journey API Layer

HTTP control plane for the synthetic monitoring journey (`node journey.js`) that
continuously exercises the Distributed Trace Test Bed by clicking **Run Trace
Test** on a fixed interval.

The journey runs on the same host as go-core-service, so these endpoints launch
and terminate it directly (detached via `Setsid`, equivalent to
`nohup ... & disown`) rather than over SSH. The PID is tracked in
`~/synthetic-journey/journey.pid` and status is reported from a tail of
`~/synthetic-journey/journey.log`.

Implemented in:

- `internal/journey/manager.go` — process lifecycle + log tailing
- `cmd/server/main.go` — route wiring

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET`  | `/api/journey/status`  | Current state: running/stopped, PID, iteration, latency, last log line |
| `POST` | `/api/journey/start`   | Start the journey (idempotent; returns existing PID if already running) |
| `POST` | `/api/journey/stop`    | Stop the journey gracefully (SIGTERM → SIGKILL after 5s) |
| `POST` | `/api/journey/trigger` | CI hook — start the journey from a `{target}` payload (see deploy.yml) |

All routes live under `/api` (gin is mounted at `/api/` on the `:8090` mux), so
full paths are `/api/journey/*`.

## Quick reference

```bash
BASE=http://10.1.92.192:8090
```

### Status

```bash
curl $BASE/api/journey/status
```

```json
{
  "status": "running",
  "pid": 182712,
  "iteration": 19,
  "latencyMs": 141,
  "lastLog": "{\"ts\":\"2026-08-11T10:54:15.586Z\",\"level\":\"info\",\"msg\":\"trace_test_clicked\",\"iteration\":19,\"latencyMs\":141,\"httpStatus\":200,\"result\":\"...\"}",
  "message": "trace_test_clicked"
}
```

`status` is `"running"` or `"stopped"`. `iteration`/`latencyMs`/`lastLog` come
from the most recent line in `journey.log`.

### Start

```bash
curl -X POST $BASE/api/journey/start \
  -H 'Content-Type: application/json' \
  -d '{"clickIntervalMs":2000}'
```

```json
{"status":"running","pid":182712,"message":"journey started"}
```

Calling start while a journey is already running is harmless — it returns the
existing PID with `"message":"journey already running"`.

Start params (all optional):

| Field | Env var | Default | Description |
|---|---|---|---|
| `clickIntervalMs` | `CLICK_INTERVAL_MS` | `2000` | Delay between clicks |
| `iterations` | `ITERATIONS` | `0` | Max clicks; `0` = run forever |
| `targetUrl` | `TARGET_URL` | `http://10.1.92.192:8081/` | Page to open |
| `headless` | `HEADLESS` | `true` | Run browser headless |

### Stop

```bash
curl -X POST $BASE/api/journey/stop
```

```json
{"status":"stopped","pid":182712,"message":"journey stopped"}
```

The journey shuts down gracefully (logs `journey_stopped`, closes the browser).
If it is still alive after 5 seconds it is force-killed.

### Trigger (CI)

Matches the `deploy.yml` "Trigger synmon" step: `$SYNMON_URL/trigger` with a
`target` body. `target` maps to `targetUrl`; everything else uses defaults.

```bash
curl -X POST $BASE/api/journey/trigger \
  -H 'Content-Type: application/json' \
  -d '{"target":"http://10.1.92.192:8081/"}'
```

## Optional auth

Set `JOURNEY_API_TOKEN` in the go-core-service environment to require a token
on all journey endpoints. When set, requests must send either:

```bash
curl $BASE/api/journey/status -H 'Authorization: Bearer <token>'
# or
curl $BASE/api/journey/status -H 'X-Journey-Token: <token>'
```

Requests without a valid token get `401`. When the variable is unset, no auth
is required (default for the test bed).

## Configuration

Env vars read by the manager (defaults baked in):

| Env var | Default | Description |
|---|---|---|
| `JOURNEY_DIR` | `/home/vunet/synthetic-journey` | Working dir + `journey.out` |
| `JOURNEY_LOG_FILE` | `<JOURNEY_DIR>/journey.log` | Log tailed for status |
| `JOURNEY_PID_FILE` | `<JOURNEY_DIR>/journey.pid` | PID tracking |
| `JOURNEY_API_TOKEN` | *(unset)* | Optional auth token |
| `HTTP_ADDR` / `GRPC_ADDR` | `:8090` / `:9090` | Listen addresses |

## CI integration

The deploy workflow (`go-core-service/.github/workflows/deploy.yml`) already
contains a "Trigger synmon" step. To enable it, set the GitHub Actions variable
`SYNMON_URL` to:

```
http://10.1.92.192:8090/api/journey
```

so the workflow's `$SYNMON_URL/trigger` call resolves to
`POST /api/journey/trigger`. If `JOURNEY_API_TOKEN` is set on the service, also
store the token as an Actions secret and add
`-H "Authorization: Bearer ${{ secrets.JOURNEY_API_TOKEN }}"` to the step.

## Operational notes

- The journey process is detached (`Setsid`): it keeps running if go-core-service
  restarts, and keeps clicking after a deploy.
- Status falls back to an anchored `pgrep '^node journey.js$'` scan, so manually
  started journeys (e.g. the old `nohup` one-liner) are also visible/stoppable.
- The journey is currently **stopped** after the last E2E test; start it with
  the `start` or `trigger` curl above.

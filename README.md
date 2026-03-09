# cron-runner

A lightweight Go service that triggers scheduled pipeline jobs for [Court Vision](../README.md), a fantasy basketball analytics platform. It fires authenticated HTTP requests to the `data-platform` service on a schedule, covering three distinct game-day phases: pre-game, live stats ingestion, and post-game processing.

## How it fits into Court Vision

```
Railway cron schedule
       │
       ▼
  cron-runner  ──POST──►  data-platform  ──writes──►  PostgreSQL
                           (pipelines)                    │
                                                          ▼
                                                       backend  ──►  frontend
```

The cron-runner is intentionally dumb: it fires HTTP requests and exits (or loops). All scheduling intelligence—whether there are games today, when tip-off is, whether ESPN has updated yet—lives in the data-platform endpoints, not here. This is the **self-gating pattern** described below.

## Tech stack

- **Go 1.22**
- [`zerolog`](https://github.com/rs/zerolog) — structured JSON logging
- Standard library `net/http` — no framework
- Built as a static binary (`CGO_ENABLED=0`), deployed via `FROM scratch` Docker images

## Directory structure

```
cron-runner/
├── main.go                    # Entry point; dispatches to trigger/poll/loop modes
├── internal/
│   ├── config/
│   │   └── config.go          # Env var loading and validation
│   ├── pipeline/
│   │   └── client.go          # HTTP client: TriggerEndpoint, FetchEndpoint, TriggerAll
│   ├── retry/
│   │   └── retry.go           # Exponential backoff with Retry-After header support
│   └── logger/
│       └── logger.go          # zerolog setup (JSON or console)
├── Dockerfile                 # Default image (JOB=poll)
├── Dockerfile.trigger         # One-shot image (JOB=trigger)
├── Dockerfile.loop            # Long-running image (JOB=loop)
├── go.mod
└── go.sum
```

## Setup and building

**Prerequisites:** Go 1.22+

```bash
# Run locally (reads env vars from shell)
source .env
go run .

# Build binary
go build -o cron-runner .

# Build Docker image
docker build -f Dockerfile.trigger -t cron-runner:trigger .
docker build -f Dockerfile.loop    -t cron-runner:loop .
docker build -f Dockerfile         -t cron-runner:poll .
```

The `.env` file at the repo root contains local defaults. Source it before running locally.

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `BACKEND_URL` | yes | `https://api.courtvision.dev` | Base URL of the data-platform service (not the backend API) |
| `PIPELINE_API_TOKEN` | yes | — | Bearer token sent on every request (`Authorization: Bearer <token>`) |
| `JOB` | yes | `poll` | Job mode: `trigger`, `poll`, or `loop` |
| `ENDPOINT` | yes | — | Path to POST to, e.g. `/v1/internal/pipelines/live-stats` |
| `MAX_RETRIES` | no | `3` | Number of retries on network errors and 5xx responses |
| `INITIAL_BACKOFF` | no | `2s` | Starting backoff duration (Go duration string, e.g. `2s`) |
| `MAX_BACKOFF` | no | `30s` | Ceiling for exponential backoff |
| `BACKOFF_FACTOR` | no | `2.0` | Multiplier applied to backoff on each attempt |
| `REQUEST_TIMEOUT` | no | `30s` | Per-request HTTP timeout |
| `LOOP_INTERVAL` | no | `30s` | Delay between iterations in loop mode |
| `LOOP_MAX_DURATION` | no | `16h` | Safety timeout for loop mode; container exits after this |
| `LOOP_SCHEDULE_ENDPOINT` | no | — | If set, loop mode GETs this path first to check schedule and determine wake time |
| `POLL_INITIAL_INTERVAL` | no | `5s` | Starting polling interval for poll mode |
| `POLL_MAX_INTERVAL` | no | `30s` | Max polling interval for poll mode (grows with 1.5x backoff) |
| `POLL_MAX_WAIT_TIME` | no | `15m` | Timeout for poll mode to wait for job completion |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, or `error` |
| `LOG_JSON` | no | `true` | `true` for JSON (production), `false` for human-readable console output |

> **Note:** In Railway, `BACKEND_URL` should point to the **data-platform** service URL, not the backend API. The two are separate deployments.

## Job modes

The `JOB` env var selects one of three execution modes. Each mode uses the same `ENDPOINT` variable as its target path.

### `trigger` — one-shot fire and forget

POSTs to `ENDPOINT` once. On success (`2xx`), exits `0`. On failure after all retries, exits `1`.

Use for jobs where you just need to kick off work and don't need to wait for confirmation—pre-game pipeline triggers, post-game pipeline triggers.

```
POST $BACKEND_URL$ENDPOINT
  └─► retry on 5xx / network error
  └─► exit 0 (success) or exit 1 (failure)
```

### `loop` — long-running poller

Fires `POST $ENDPOINT` on a fixed interval until the response body contains `data.done == true`, or the `LOOP_MAX_DURATION` safety timeout is reached. Transient errors are logged and skipped—the loop continues.

If `LOOP_SCHEDULE_ENDPOINT` is configured, the runner first `GET`s that path to learn:
- `data.has_games` — if `false`, exits immediately (no work today)
- `data.wake_at_et` — if set (RFC3339), sleeps until that time before starting the polling loop

This lets the Railway cron fire the container at a fixed early time (e.g., 6 PM ET) while the container handles its own delayed start based on the actual tip-off schedule.

```
GET $LOOP_SCHEDULE_ENDPOINT
  ├─► has_games == false → exit 0
  └─► sleep until wake_at_et
      └─► loop every $LOOP_INTERVAL:
            POST $ENDPOINT
              ├─► data.done == true → exit 0
              ├─► transient error   → log warn, continue
              └─► timeout ($LOOP_MAX_DURATION) → exit 0
```

### `poll` — start job, poll for completion

POSTs to `ENDPOINT` to create a background job, extracts a `job_id` from the response, then polls `GET /v1/internal/pipelines/jobs/{job_id}` until the job reaches `completed` or `failed` status. Used when you need to confirm all pipelines in a batch succeeded before declaring success.

```
POST $ENDPOINT → job_id
  └─► poll GET /pipelines/jobs/{job_id} (with 1.5x backoff, up to $POLL_MAX_WAIT_TIME)
        ├─► status == "completed" && failures == 0 → exit 0
        └─► status == "failed" or timeout          → exit 1
```

## Retry behavior

All HTTP calls go through `internal/retry`. The retry logic:

- Retries on network errors and `5xx` responses (`500`, `502`, `503`, `504`, `429`)
- Uses exponential backoff: `initial_backoff * factor^attempt`, capped at `max_backoff`
- Respects `Retry-After` headers on `429` responses
- Does **not** retry `4xx` errors (bad request, auth failure, etc.)

In `loop` mode, a failed trigger iteration logs a warning and continues the loop rather than propagating the error. The loop only stops on `data.done == true` or timeout.

## Self-gating pattern

Endpoints on the data-platform are self-gating: they inspect game schedules, scoring periods, and time windows internally and return early if there is nothing to do. The cron-runner fires on a schedule but defers all "should I actually run?" logic to the downstream service.

This means:
- No hardcoded game times in the cron-runner
- No race conditions from cron firing too early or too late
- The data-platform endpoint is the authoritative source of "is it time yet?"

## Railway deployments

Three Railway services run from this repository, each using a different Dockerfile and cron schedule.

### pre-game (`Dockerfile.trigger`)

| Setting | Value |
|---|---|
| `JOB` | `trigger` |
| `ENDPOINT` | `/v1/internal/pipelines/pre-game` (or equivalent) |
| Railway cron | `0/15 14-23,0-2 * * *` (every 15 min, 2 PM–2 AM ET) |

Fires the pre-game pipeline on a 15-minute cadence throughout the afternoon and evening. The data-platform endpoint is self-gating: it skips work outside the relevant window and deduplicates runs so that firing every 15 minutes is safe.

### live (`Dockerfile.loop`)

| Setting | Value |
|---|---|
| `JOB` | `loop` |
| `ENDPOINT` | `/v1/internal/pipelines/live-stats` |
| `LOOP_SCHEDULE_ENDPOINT` | `/v1/live/today` (returns `has_games`, `wake_at_et`) |
| `LOOP_INTERVAL` | `60s` |
| `LOOP_MAX_DURATION` | `6h` |
| Railway cron | `0 18 * * *` (once at 6 PM ET each day) |

Starts once per evening. GETs the schedule endpoint to check if there are games and when to wake up, sleeps until 30 minutes before the first tip-off, then POSTs to the live-stats endpoint every 60 seconds. When all games are final, the endpoint returns `data.done = true` and the container exits. The 6-hour `LOOP_MAX_DURATION` prevents zombie containers if something goes wrong.

### post-game (`Dockerfile.trigger`)

| Setting | Value |
|---|---|
| `JOB` | `trigger` |
| `ENDPOINT` | `/v1/internal/pipelines/post-game` (or equivalent) |
| Railway cron | `0/15 14-23,0-2 * * *` (same cadence as pre-game) |

Fires the post-game pipeline on the same 15-minute cadence. The data-platform endpoint gates on all games being final and on ESPN's `latestScoringPeriod` advancing (with a 2:30 AM ET fallback), so firing frequently is safe—work only happens once per game night when conditions are met.

## Logging

All output is structured JSON by default (`LOG_JSON=true`), intended for log aggregation pipelines. Each log line includes:

```json
{
  "level": "info",
  "service": "cron-runner",
  "component": "pipeline-client",
  "time": "2026-03-09T22:00:01Z",
  "endpoint": "/v1/internal/pipelines/live-stats",
  "status_code": 200,
  "attempts": 1,
  "duration": "245ms",
  "message": "endpoint triggered successfully"
}
```

Set `LOG_JSON=false` for human-readable console output during local development.

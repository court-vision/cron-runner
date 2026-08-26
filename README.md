# cron-runner

A small, always-on Go service that triggers [Court Vision](https://github.com/court-vision)'s scheduled data pipelines. It runs an in-process cron scheduler, and on each tick fires one authenticated `POST` at a `data-platform` pipeline endpoint, then reports the outcome back to the data-platform so the ops dashboard can show it.

## How it fits into Court Vision

```
 cron-runner (one Railway service, one Go process)
 ┌───────────────────────────────────────────────┐
 │ gocron scheduler (UTC)                        │
 │   pre-game ─┐                                 │
 │   live-stats│  POST /v1/internal/pipelines/*  │──►  data-platform  ──writes──►  PostgreSQL (nba.*)
 │   post-game ├─────────────────────────────────┤                                     │
 │   ...       │  POST /v1/internal/cron/job-runs│──►  (run history for the dashboard)  ▼
 │ HTTP :8082  ┘  GET /health, GET /status       │                                  backend ──► frontend
 └───────────────────────────────────────────────┘
```

The cron-runner is intentionally dumb: it knows *when to ask*, never *whether to run*. Whether there are games today, when tip-off is, whether every game is final, whether ESPN has updated yet — all of that lives in the data-platform endpoints, which return immediately when there is nothing to do (the **self-gating pattern** below). That is why every job below fires far more often, and over a wider window, than it strictly needs to.

## Tech stack

- **Go 1.22**, standard library `net/http`
- [`gocron/v2`](https://github.com/go-co-op/gocron) — in-process cron scheduler (robfig/cron syntax)
- [`zerolog`](https://github.com/rs/zerolog) — structured JSON logging
- Built as a static binary (`CGO_ENABLED=0`) into a `FROM scratch` image

## Directory structure

```
cron-runner/
├── main.go                    # Loads config, builds client + reporter, registers jobs, starts scheduler + HTTP server
├── internal/
│   ├── config/config.go       # Env var loading and validation (global settings only)
│   ├── jobs/registry.go       # THE job table — one JobDef per scheduled job
│   ├── scheduler/scheduler.go # gocron wrapper: UTC, singleton mode, per-job timeout, run history
│   ├── task/                  # Task interface + TriggerTask (fire-and-forget POST) and PollTask (start job, poll to completion)
│   ├── pipeline/client.go     # HTTP client: TriggerEndpoint, FetchEndpoint, TriggerAll (+ retries)
│   ├── retry/retry.go         # Exponential backoff with Retry-After support
│   ├── reporter/reporter.go   # Async POST of each run's outcome to /v1/internal/cron/job-runs (one retry)
│   ├── server/server.go       # GET /health (Railway healthcheck, version) and GET /status
│   └── logger/logger.go       # zerolog setup (JSON or console)
├── Dockerfile
├── .env.example               # Local defaults; copy to .env
├── go.mod
└── go.sum
```

## Jobs

All schedules are evaluated in **UTC** (`gocron.WithLocation(time.UTC)`). NBA game times are US/Eastern, which is UTC-4 (EDT) from mid-March to early November and UTC-5 (EST) otherwise, so every window is padded by an hour to hold across the DST switch. `live-stats` uses a six-field expression whose first field is seconds (`WithSeconds: true`); the others are standard five-field cron.

| Job | UTC schedule | Eastern equivalent | Endpoint (`POST`) | Data-platform gate |
|---|---|---|---|---|
| `pre-game` | `0/15 13-23,0-1 * * *` | every 15 min, 9:00 AM–9:45 PM EDT (8:00 AM–8:45 PM EST) | `/v1/internal/pipelines/pre-game` | No-op unless games today and now ≥ first tip − 150 min; per-pipeline dedup (once per NBA date) and concurrency check. Returns at once; pipelines run in the background. |
| `live-stats` | `*/30 * 16-23,0-7 * * *` (6-field) | every 30 s, noon–3:59 AM EDT (11:00 AM–2:59 AM EST) | `/v1/internal/pipelines/live-stats` | No-op unless games today and within 15 min of the first tip; runs in milliseconds otherwise. Falls back to the live scoreboard for playoff dates missing from `nba.games`. |
| `post-game` | `0/15 2-13 * * *` | every 15 min, 10:00 PM–9:45 AM EDT (9:00 PM–8:45 AM EST) | `/v1/internal/pipelines/post-game` | Only inside a window that opens 150 min after the latest tip and lasts 210 min, and only once every game is Final. Per-pipeline dedup; ESPN-gated pipelines wait for ESPN's scoring period to advance (2:30 AM CST fallback). |
| `schedule-sync` | `0 12 * * 1` | Mondays 8:00 AM EDT (7:00 AM EST) | `/v1/internal/pipelines/game-start-times?source=cdn` | None on the cron side — an idempotent upsert of future `nba.games` rows (tip-off times, moved/postponed games) from the NBA CDN feed. |
| `playoffs` | `0 6 * * *` | daily 2:00 AM EDT (1:00 AM EST) | `/v1/internal/pipelines/playoffs` | None on the cron side — upserts `nba.playoff_series` from NBA SeriesStandings. |
| `deploy` | `0 8 * * *` | daily 4:00 AM EDT (3:00 AM EST / 2:00 AM CST) | `/v1/internal/pipelines/deploy` | Fires a GitHub `repository_dispatch` (`nightly-deploy`) at the backend and data-platform repos; 503 if the GitHub deploy config is missing. The backend also auto-deploys on push, so in practice this is the data-platform's nightly release. |

Job-level settings that apply to all of the above:

- **Singleton** — every job runs in `LimitModeReschedule`: if the previous run is still in flight, the new tick is skipped.
- **Timeout** — every trigger job except `live-stats` cancels its request context after 5 minutes. The endpoints return immediately and do their work in the background, so this only bites if the data-platform is hung.
- **Reporting** — after every run, `TriggerTask` POSTs a `RunReport` (`job_name`, timings, HTTP status, attempts, error, response snippet) to `/v1/internal/cron/job-runs`. The `job_name` is the registry name, which is what the data-platform dashboard keys on.

All of this lives in one file: [`internal/jobs/registry.go`](internal/jobs/registry.go). The table above is asserted by `internal/jobs/registry_test.go`, so a schedule edit has to be made in both places.

## Self-gating pattern

Endpoints on the data-platform inspect game schedules, pipeline-run history and time windows internally and return `200` early when there is nothing to do. The cron-runner fires on a fixed schedule and defers every "should I actually run?" decision downstream. This means:

- No game times hardcoded here, and no code change when the NBA moves a tip-off
- Firing every 15 s/15 min is safe: a skipped tick costs one cheap request
- The whole registry can stay enabled through the off-season — every job returns "no games"

## Running locally

**Prerequisites:** Go 1.22+ and a `PIPELINE_API_TOKEN` for the data-platform you are pointing at (use a dev deployment, never production, unless you mean to trigger real pipelines).

```bash
cp .env.example .env      # then fill in PIPELINE_API_TOKEN
source .env
go run .                  # scheduler starts immediately; GET http://localhost:8082/status

go build ./... && go vet ./... && go test ./...
docker build -t cron-runner .
```

Jobs fire on their real UTC schedule, so a local run mostly idles. To exercise an endpoint by hand, `curl -X POST -H "Authorization: Bearer $PIPELINE_API_TOKEN" "$BACKEND_URL/v1/internal/pipelines/pre-game?force=true"` — the gates accept `?force=true` / `?date=YYYY-MM-DD` for manual runs and backfills.

## Environment variables

All defined in [`internal/config/config.go`](internal/config/config.go). Durations are Go `time.ParseDuration` strings (`30s`, `15m`, `2h`) — a bare number is silently ignored and the default is used.

| Variable | Required | Default | Description |
|---|---|---|---|
| `BACKEND_URL` | yes | `https://data.courtvision.dev` | Origin of the **data-platform** (not the backend API). Every job POSTs to `$BACKEND_URL/v1/internal/pipelines/*` and reports to `$BACKEND_URL/v1/internal/cron/job-runs`. |
| `PIPELINE_API_TOKEN` | yes | — | Bearer token sent on every request (`Authorization: Bearer <token>`). |
| `MAX_RETRIES` | no | `3` | Retries per trigger on network errors, `429`, and `5xx`. |
| `INITIAL_BACKOFF` | no | `2s` | First retry delay. |
| `MAX_BACKOFF` | no | `30s` | Ceiling for exponential backoff. |
| `BACKOFF_FACTOR` | no | `2.0` | Multiplier applied per attempt. |
| `REQUEST_TIMEOUT` | no | `30s` | Per-request HTTP timeout (independent of the job `Timeout`, which bounds the whole run including retries). |
| `POLL_INITIAL_INTERVAL` | no | `5s` | `PollTask` only: first status-poll interval. No registered job uses `PollTask` today. |
| `POLL_MAX_INTERVAL` | no | `30s` | `PollTask` only: poll interval ceiling (grows 1.5x per poll). |
| `POLL_MAX_WAIT_TIME` | no | `15m` | `PollTask` only: give up waiting for job completion. |
| `HTTP_PORT` | no | `$PORT`, else `8082` | Port for `/health` and `/status`. Falls back to `PORT` (which Railway injects and points its healthcheck at) before the default, so leave both unset on Railway. |
| `DRAIN_TIMEOUT` | no | `30s` | On `SIGTERM`/`SIGINT`, how long to wait for in-flight jobs before exiting. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, or `error`. |
| `LOG_JSON` | no | `true` | `true` for JSON lines (production), `false` for a human-readable console writer. |

Job-specific settings (endpoints, schedules, timeouts) are deliberately **not** environment variables — they live in the registry so a change is a reviewed commit, not a Railway variable edit.

## HTTP endpoints

The process listens on `HTTP_PORT`, falling back to `PORT` and then `8082`. Both routes are unauthenticated and read-only.

### `GET /health`

Railway health check and the target for an uptime monitor. Always `200` while the process is running — the scheduler has no external dependency to gate on (the data-platform's own `/health` covers the database).

```json
{"status":"ok","uptime":"3h12m5s","jobs":6,"version":"270eb2e"}
```

`version` is the first seven characters of `RAILWAY_GIT_COMMIT_SHA`, or `dev` outside Railway — the quickest way to confirm which commit a deployment is running.

### `GET /status`

Scheduler uptime plus the runtime state of every registered job — `next_run` from gocron, and the last result, error, duration, total run count and the last 20 runs from the scheduler's in-memory ring buffer. State resets on restart; durable history is in the data-platform's `cron_job_runs` table.

```json
{
  "scheduler": "running",
  "uptime": "3h12m5s",
  "jobs": [
    {
      "name": "live-stats",
      "schedule": "*/30 * 16-23,0-7 * * *",
      "last_run": "2026-11-01T02:15:30.412Z",
      "next_run": "2026-11-01T02:16:00Z",
      "last_result": "success",
      "last_duration": "212ms",
      "run_count": 1234,
      "recent_runs": [
        { "triggered_at": "2026-11-01T02:15:30.2Z", "duration_ms": 212, "result": "success" }
      ]
    },
    {
      "name": "post-game",
      "schedule": "0/15 2-13 * * *",
      "last_result": "never",
      "next_run": "2026-11-01T02:30:00Z",
      "run_count": 0
    }
  ]
}
```

`last_result` is one of `never`, `running`, `success`, `failure`; `last_error` is present only after a failure.

## Retry behavior

Every trigger goes through `internal/retry`:

- Retries on network errors and on `429`, `500`, `502`, `503`, `504` (any `5xx`)
- Exponential backoff `INITIAL_BACKOFF * BACKOFF_FACTOR^attempt`, capped at `MAX_BACKOFF`
- Honors `Retry-After` on `429`
- Does **not** retry other `4xx` (bad request, bad token, unknown route)
- Stops early if the job's context is cancelled (timeout or shutdown)

A run that exhausts its retries is reported as `failure` with the last HTTP status and error, and the next scheduled tick tries again from scratch.

The run report itself (`POST /v1/internal/cron/job-runs`) is sent asynchronously and retried once, after 2 s, on a transport error or `5xx`; a `4xx` is logged and dropped. A lost report never fails the job, but a lost *failure* report is a missed alert (see below), which is why it gets one retry.

## Alerting

The cron-runner does not alert on its own and carries no Sentry SDK. Two layers cover it:

- **Job failures** are alerted by the data-platform, not here. Every run's `RunReport` lands in `nba.cron_job_runs`, and the data-platform's alert notifier fires on a *streak* of consecutive failures per job (thresholds are per job — e.g. three `live-stats` failures in a row, one `deploy` failure) and again on recovery. A single failed tick is noise by design, since the next tick retries; that is why the threshold is a streak and why the reporter retries a failed report once.
- **A crashed or restart-looping process** is caught by Railway: the project webhook (deploy crashed / failed) posts to the ops Discord channel, and the service's healthcheck path is `/health`. If the process is up, the scheduler is running.

For a manual look, `GET /health` says which commit is running and `GET /status` shows every job's last result, last error and next fire time. Every trigger logs a `correlation_id` that the data-platform echoes on its `http_request` line (see Logging), so a failed run can be followed into the request it made.

## Adding a job

1. Append a `scheduler.JobDef` to `RegisterAll` in [`internal/jobs/registry.go`](internal/jobs/registry.go):

   ```go
   {
       Name:      "my-job",
       Schedule:  "30 11 * * *",   // UTC; add WithSeconds: true for a 6-field expression
       Singleton: true,
       Timeout:   5 * time.Minute,
       Task:      trigger("my-job", "/v1/internal/pipelines/my-endpoint?some=param"),
   },
   ```

   `trigger(name, endpoint)` builds a `TriggerTask` that POSTs `$BACKEND_URL` + endpoint (query strings are preserved) and reports the run under `name`. For anything other than a fire-and-forget POST, implement `task.Task` (`Name()` + `Run(ctx) error`) and pass that instead.

2. Add a row to the `expected` table in `internal/jobs/registry_test.go`, and ideally a window-edge case in `TestScheduleNextFire`. `go test ./...` then proves the expression parses under gocron with the right seconds flag.

3. Add a row to the **Jobs** table above, and make sure the data-platform endpoint self-gates — the scheduler will call it whether or not there is work.

4. If the data-platform dashboard should attribute a pipeline to the new job, add the mapping to `PIPELINE_CRON_JOB_MAP` in the data-platform's `api/v1/dashboard.py`.

Nothing else changes: `main.go` registers whatever the registry returns, and `/status` picks the job up automatically.

## Railway deployment

One service, built from `Dockerfile`, always on (this is **not** a Railway cron-schedule service — the process schedules itself).

- Variables: `BACKEND_URL` (the data-platform's public origin), `PIPELINE_API_TOKEN` (must match the data-platform's), optionally `LOG_LEVEL`.
- Health check: `GET /health` on Railway's `PORT` (the service falls back to it when `HTTP_PORT` is unset). Always `200` while the process runs, so a failing check means the process is down, not degraded.
- Alerts: none from this service — see **Alerting**. Configure the project webhook for crashed/failed deploys.
- Restart policy: always. On `SIGTERM` the scheduler stops accepting ticks, in-flight runs get `DRAIN_TIMEOUT` to finish, then the HTTP server closes.
- A redeploy mid-window is safe: any tick lost during the restart is covered by the next one, and the endpoints dedup on their side.

## Logging

Structured JSON by default (`LOG_JSON=true`). Every line carries `service=cron-runner`, a `component` (`scheduler`, `pipeline-client`, `reporter`, `http-server`) and, for job output, `job=<name>`.

Every trigger generates a UUID `correlation_id`. It is attached to the pipeline-client lines (retries included) and to the `trigger_succeeded` / `trigger_failed` line, and is sent to the data-platform as `X-Correlation-ID`, where it appears on the matching `http_request` log line. To follow a failure across services: find the `trigger_failed` line here, copy its `correlation_id`, and grep for it in the data-platform's logs.

```json
{"level":"info","service":"cron-runner","component":"scheduler","job":"post-game","time":"2026-11-01T02:15:00Z","message":"job_started"}
{"level":"info","service":"cron-runner","component":"pipeline-client","correlation_id":"6f0c2b1e-4d1a-4c3b-9a7e-2f8d5c1b0a94","endpoint":"/v1/internal/pipelines/post-game","status_code":200,"attempts":1,"duration":"245ms","time":"2026-11-01T02:15:00Z","message":"endpoint triggered successfully"}
{"level":"info","service":"cron-runner","job":"post-game","correlation_id":"6f0c2b1e-4d1a-4c3b-9a7e-2f8d5c1b0a94","attempts":1,"status_code":200,"duration":"245ms","time":"2026-11-01T02:15:00Z","message":"trigger_succeeded"}
```

Set `LOG_JSON=false` for a human-readable console writer during local development.

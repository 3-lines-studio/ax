# Routines

A routine is deterministic code that runs on demand or on a schedule. It is
not a prompt and it does not call a model. The agent writes and debugs the
routine once; after that, running it costs no tokens and cannot drift with a
model update.

This follows the same split as Kody jobs: a schedule exists to run an owned
program, not to re-run an agent on a timer.

## Layout

Routines are definition paths; their runtime state is not.

```text
<bot>/
  routines/
    inbox-digest/
      routine.toml
      run.sh            # any executable: sh, python, go...
```

```toml
# routines/inbox-digest/routine.toml
description = "Resumen diario del inbox"
entry = "./run.sh"
schedule = "0 8 * * *"          # optional
timezone = "America/Argentina/Buenos_Aires"
timeout = "10m"
keep_runs = 50                  # run directories to keep; 0 keeps them all
enabled = false                 # test it before turning it on
```

- `entry` is required and must stay inside the routine directory.
- `schedule` is an optional five-field cron expression. The aliases `@hourly`,
  `@daily`, `@midnight`, `@weekly`, `@monthly`, `@yearly` and `@annually` are
  accepted. Names (`JAN`, `MON`) are not.
- `timezone` defaults to the host's local time; an IANA name is recommended.
- `timeout` defaults to `10m` and is capped at `1h`. On a timeout the runner
  kills the routine's whole process group, not just the entry script, so a
  script that spawned children does not leave them behind.
- `keep_runs` defaults to `50` and is capped at `1000`; `0` keeps every run.
  Pruning happens after each run, under the same lock, and deletes the oldest
  run directories first.
- `enabled` defaults to `false`. Loading a bot never runs a routine; the
  scheduler only considers enabled routines.

## Running

```sh
ax routine list                 # name, schedule, enabled, last run
ax routine run inbox-digest     # run now and print the output
ax routine runs inbox-digest    # recent runs, newest first
ax routine log inbox-digest     # output of the latest run
ax routine log inbox-digest 20261028T081500-ab12cd34
```

`run` works even when the routine is disabled: that is how you test it before
enabling the schedule. It prints the status, the log path and the log content;
a non-zero exit or a timeout exits non-zero. `runs` and `log` read the history
on disk, so they also show runs started by the scheduler.

The execution environment:

| Variable | Value |
| --- | --- |
| `BOT_ROOT` | the bot root |
| `BOT_DATA` | the runtime data root |
| `ROUTINE_NAME` | the routine name |
| `ROUTINE_DIR` | the routine definition directory |
| `ROUTINE_STATE` | `state/routines/<name>` |
| `ROUTINE_RUN_ID` | the id of this run |
| `ROUTINE_TRIGGER` | `manual` or `cron` |

The working directory is the bot workspace. State and cursors belong in
`ROUTINE_STATE`; they are not memory and not part of the bot definition. When a
host runs a channel, `BOT_ROOT` is the host bot root (unless the channel has
its own `bot.toml`) and `BOT_DATA` is the channel's workdir: every channel has
its own workspace, state and run history, even if the host process itself was
started with a different `BOT_DATA`. The runner creates:

```text
state/routines/<name>/
  lock
  runs/<run-id>/
    run.json          # id, trigger, status, exit code, timestamps, log path
    output.log        # stdout and stderr
```

A per-routine lock prevents overlapping runs; a second attempt returns
"already running" instead of racing. `run.json` is written atomically.

## Scheduling

`webi` is the always-on host and runs the scheduler. Every 30 seconds it loads
`routines/*/routine.toml` from the host bot root (or from a channel's own
`bot.toml`, when it has one); a channel with a `routines/` directory overrides
the host set. It checks the enabled cron schedules in their timezone and
launches the due routines. A run that is still active when the next tick
arrives is skipped by the lock.

Every channel keeps its own `state/routines/`, so two channels can run a
routine with the same name without sharing a schedule, a lock or a history.
The webi view lists every channel; to inspect one from the CLI, point
`ax -C <host bot root>` at the host and set `BOT_DATA=<channel workdir>`.

`boti` and a plain `ax` run do not schedule; use `ax routine run` from cron or
another scheduler when there is no `webi` process.

## Runs and results

Every run leaves its result on disk, whether it came from cron or from a
manual click:

```text
state/routines/<name>/runs/<run-id>/
  run.json       # id, trigger, status, exit code, error, timestamps, log path
  output.log     # stdout and stderr, truncated to the last 64 KiB in the UI
```

`status` is `ok`, `failed`, `timeout`, `interrupted` or `running`. A timeout
also records the error; a non-zero exit keeps its exit code. If the host dies
mid-run, the record stays `running`; the next run of that routine rewrites it
as `interrupted` instead of pretending it finished. The state directory is part
of the bot's runtime data (`BOT_DATA`), not the definition, so it is safe to
delete and never travels with `bot.toml`.

The webi UI has a **Routines** entry: it lists every routine with its channel,
schedule, last status and last run time; **Run now** executes one manually and
streams its output while it runs, **History** lists the recent runs, and
**Log** opens the output of the latest run or of a chosen one. A run that is
still going keeps updating its log view every 1.2 seconds until it finishes. The same data is available over HTTP behind the UI
token:

| Endpoint | Purpose |
| --- | --- |
| `GET /api/routines?channel=<id>` | routines, with their last run |
| `POST /api/routines/<name>/run?channel=<id>` | run now; `409` if already running |
| `GET /api/routines/<name>/current?channel=<id>` | the run in progress and its output so far |
| `GET /api/routines/<name>/runs?channel=<id>&limit=20` | recent runs |
| `GET /api/routines/<name>/log?channel=<id>` | latest run and its output |
| `GET /api/routines/<name>/runs/<run-id>?channel=<id>` | one run and its output |

Without `channel=` the API falls back to the bot root, which keeps a single-bot
setup working.

## What a routine is not

- Not a chat thread or a session; no model, no tools, no catalog.
- Not a place for procedures that need judgment; those belong in `skills/` or a
  normal `ax` session.
- Not a credential store; secrets come from the vault or the environment.
- Not memory; durable personal facts belong in [memory](memory.md).

Routines are trusted code and run with the bot's privileges. There is no
approval gate: test with `enabled = false`, keep the code small, and turn the
schedule on only after a manual run succeeds.

## Notifications

webi pushes every finished run over the same SSE stream as thread events
(`{"type":"routine", ...}`). When notifications are on, the PWA shows a local
notification — no push server, no VAPID, nothing that runs while the app is
closed:

- a failure, a timeout or an interrupted run always notifies;
- a success notifies only if the page is hidden or the run took longer than
  ten seconds;
- tapping it opens the Routines view on that run's log.

The same events keep an open Routines view current: a row's status changes
without a reload. The notification toggle is the bell in the sidebar; it is
the same permission and preference as thread run notifications.

Runs still land in `run.json`, `output.log`, `ax routine runs|log` and the
webi API; posting a result into a channel thread is not implemented on
purpose: thread messages belong to the ax session log, and a host must not
invent turns there.

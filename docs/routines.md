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
enabled = false                 # test it before turning it on
```

- `entry` is required and must stay inside the routine directory.
- `schedule` is an optional five-field cron expression. The aliases `@hourly`,
  `@daily`, `@midnight`, `@weekly`, `@monthly`, `@yearly` and `@annually` are
  accepted. Names (`JAN`, `MON`) are not.
- `timezone` defaults to the host's local time; an IANA name is recommended.
- `timeout` defaults to `10m` and is capped at `1h`.
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
`ROUTINE_STATE`; they are not memory and not part of the bot definition. The
runner creates:

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
`routines/*/routine.toml` for the bot root and for every channel workdir,
checks the enabled cron schedules in their timezone, and launches the due
routines. A run that is still active when the next tick arrives is skipped by
the lock.

A channel is a bot directory, so `routines/` and `state/routines/` are
per-channel: two channels can define a routine with the same name without
sharing a schedule, a lock or a history. The webi view lists every channel; to
inspect one from the CLI, point `ax -C <channel workdir>` at it.

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
schedule, last status and last run time; **Run now** executes one manually,
**History** lists the recent runs, and **Log** opens the output of the latest
run or of a chosen one. The same data is available over HTTP behind the UI
token:

| Endpoint | Purpose |
| --- | --- |
| `GET /api/routines?channel=<id>` | routines, with their last run |
| `POST /api/routines/<name>/run?channel=<id>` | run now; `409` if already running |
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

## Not implemented yet

Notifications. A run is visible in `run.json`, `output.log`, `ax routine
runs|log` and the webi Routines view; pushing a result to a channel is a later
layer.

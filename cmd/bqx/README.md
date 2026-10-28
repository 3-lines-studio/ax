# bqx

Read-only BigQuery tool for AX.

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- bqx
```

## Configure

```sh
export GOOGLE_CLOUD_PROJECT=my-project
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json
```

```toml
[[connection]]
id = "bqx"
kind = "local"
command = "bqx"
```

`GOOGLE_APPLICATION_CREDENTIALS` accepts either a path to a service-account JSON file or the inline JSON content itself (a JSON object).

## Protocol

```sh
bqx describe
printf '{"sql":"SELECT 1"}' | bqx run bigquery_query
```

Bqx performs a BigQuery dry run and only executes statements BigQuery identifies as `SELECT`. Results stop at 1,000 rows.

## Google Cloud Storage

Copy a private object with the same Google credentials:

```sh
bqx gcs-copy BUCKET OBJECT FILE
```

The destination is replaced atomically only after a non-empty object is downloaded.

## Test

```sh
go test ./cmd/bqx
```

# pgx

Read-only PostgreSQL tool for AX.

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- pgx
```

## Configure

```sh
export DATABASE_URL=postgres://user:password@host/database
```

```toml
[[connection]]
id = "pgx"
kind = "local"
command = "pgx"
```

The legacy `ALFRED_PICSEL_DATABASE_URL` name remains supported during migration.

## Protocol

```sh
pgx describe
printf '{"sql":"SELECT 1"}' | pgx run postgres_query
```

Every query runs in a PostgreSQL read-only transaction. Results stop at 1,000 rows.

## Test

```sh
go test ./cmd/pgx
```

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxRows = 1000

var forbidden = regexp.MustCompile(`\b(INSERT|UPDATE|DELETE|DROP|ALTER|TRUNCATE|CREATE|MERGE|GRANT|REVOKE|CALL|COPY)\b`)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 1 && args[0] == "describe" {
		fmt.Println(`{"name":"postgres_query","description":"Run a read-only PostgreSQL query","parameters":{"type":"object","properties":{"sql":{"type":"string","description":"SELECT or WITH query"}},"required":["sql"]}}`)
		return
	}
	if len(args) != 2 || args[0] != "run" || args[1] != "postgres_query" {
		fmt.Fprintln(os.Stderr, "usage: pgx describe | pgx run postgres_query")
		os.Exit(2)
	}
	var input struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&input); err != nil {
		fail(2, "invalid arguments: "+err.Error())
	}
	if err := validateReadOnly(input.SQL); err != nil {
		fail(2, err.Error())
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("ALFRED_PICSEL_DATABASE_URL")
	}
	if databaseURL == "" {
		fail(2, "DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := query(ctx, databaseURL, input.SQL)
	if err != nil {
		fail(1, err.Error())
	}
	fmt.Println(result)
}

func query(ctx context.Context, databaseURL, sql string) (string, error) {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return "", fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	result := make([]map[string]any, 0)
	truncated := false
	for rows.Next() {
		if len(result) == maxRows {
			truncated = true
			break
		}
		values, err := rows.Values()
		if err != nil {
			return "", fmt.Errorf("read row: %w", err)
		}
		row := make(map[string]any, len(fields))
		for i, field := range fields {
			row[string(field.Name)] = values[i]
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read rows: %w", err)
	}
	payload, err := json.Marshal(map[string]any{"rows": result, "truncated": truncated})
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(payload), nil
}

func validateReadOnly(sql string) error {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return errors.New("only SELECT and WITH queries are allowed")
	}
	if keyword := forbidden.FindString(upper); keyword != "" {
		return fmt.Errorf("query contains forbidden keyword: %s", keyword)
	}
	return nil
}

func fail(code int, message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	os.Exit(code)
}

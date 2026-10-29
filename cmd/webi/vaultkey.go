package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (s *Server) adoptVaultKey() {
	raw := strings.TrimSpace(os.Getenv("AX_VAULT_FD"))
	if raw == "" {
		return
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 {
		return
	}
	file := os.NewFile(uintptr(fd), "vault-key")
	if file == nil {
		return
	}
	defer func() { _ = file.Close() }()
	key := make([]byte, vaultKeyLen)
	if _, err := io.ReadFull(file, key); err != nil {
		slog.Warn("webi: read host vault key", "err", err)
		return
	}
	if err := s.vault.Adopt(key); err != nil {
		slog.Warn("webi: host vault key rejected", "err", err)
		return
	}
	slog.Info("webi: vault unlocked by the host key")
}

func (s *Server) shareVaultKey(forget bool) {
	sock := strings.TrimSpace(os.Getenv("AX_HOST_SOCK"))
	if sock == "" {
		return
	}
	method := http.MethodPut
	var body io.Reader
	if forget {
		method = http.MethodDelete
	} else {
		key := s.vault.Key()
		if len(key) != vaultKeyLen {
			return
		}
		body = strings.NewReader(fmt.Sprintf(`{"key":%q}`, hex.EncodeToString(key)))
	}
	req, err := http.NewRequest(method, "http://unix/vault-key", body)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("webi: share vault key", "err", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("webi: share vault key", "status", resp.Status)
	}
}

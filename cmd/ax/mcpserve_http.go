package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func serveMCPHTTP(addr string, tools []Tool) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{
		Handler:           mcpHTTPHandler(tools),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	fmt.Fprintf(os.Stderr, "ax mcp serve: http://%s\n", listener.Addr())
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func mcpHTTPHandler(tools []Tool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			httpError(w, http.StatusMethodNotAllowed, "use POST")
			return
		}
		if !mcpOriginAllowed(r) {
			httpError(w, http.StatusForbidden, "origin not allowed")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPMessage+1))
		if err != nil {
			httpError(w, http.StatusBadRequest, "read body: "+err.Error())
			return
		}
		if len(body) > maxMCPMessage {
			httpError(w, http.StatusRequestEntityTooLarge, "message exceeds 32 MiB")
			return
		}
		var request mcpRequest
		w.Header().Set("Content-Type", "application/json")
		if err := json.Unmarshal(body, &request); err != nil {
			writeRPC(w, rpcError(nil, -32700, "parse error"))
			return
		}
		result, rpcErr := handleMCPRequest(r.Context(), request, tools)
		if len(request.ID) == 0 || string(request.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if rpcErr != nil {
			writeRPC(w, map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": rpcErr})
			return
		}
		writeRPC(w, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	})
}

func mcpOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func writeRPC(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func httpError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

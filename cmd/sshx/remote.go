package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/3-lines-studio/ax/internal/filetext"
)

var temporaryID atomic.Uint64

type remote struct {
	cfg    config
	client *ssh.Client
	sftp   *sftp.Client
}

func dial(cfg config) (*remote, error) {
	auths, closeAgent, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	hostKey, err := knownhosts.New(cfg.knownHosts)
	if err != nil {
		return nil, fmt.Errorf("known_hosts %s: %w", cfg.knownHosts, err)
	}
	address, login := splitHost(cfg.host)
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            login,
		Auth:            auths,
		HostKeyCallback: hostKey,
		Timeout:         cfg.timeout,
	})
	if err != nil {
		closeAgent()
		return nil, fmt.Errorf("connect to %s: %w", cfg.host, err)
	}
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		closeAgent()
		return nil, fmt.Errorf("open sftp: %w", err)
	}
	return &remote{cfg: cfg, client: client, sftp: sftpClient}, nil
}

func (r *remote) Close() error {
	if r.sftp != nil {
		_ = r.sftp.Close()
	}
	return r.client.Close()
}

func splitHost(raw string) (address, login string) {
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		return raw[at+1:], raw[:at]
	}
	return raw, os.Getenv("USER")
}

func authMethods(cfg config) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	closeAgent := func() {}
	if cfg.keyFile != "" {
		signer, err := readKey(cfg.keyFile)
		if err != nil {
			return nil, closeAgent, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		connection, err := net.Dial("unix", socket)
		if err == nil {
			agentClient := agent.NewClient(connection)
			methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))
			closeAgent = func() { _ = connection.Close() }
		}
	}
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		home, err := os.UserHomeDir()
		if err != nil {
			break
		}
		path := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		signer, err := readKey(path)
		if err != nil {
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, closeAgent, errors.New("no SSH key: set SSHX_KEY_FILE, SSH_AUTH_SOCK or ~/.ssh/id_*")
	}
	return methods, closeAgent, nil
}

func readKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return nil, fmt.Errorf("key %s is encrypted; sshx does not prompt for a passphrase", path)
		}
		return nil, fmt.Errorf("parse key %s: %w", path, err)
	}
	return signer, nil
}

func (r *remote) resolve(input string) (string, error) {
	if input == "" {
		return "", errors.New("path is required")
	}
	if r.cfg.root == "" {
		return path.Clean(input), nil
	}
	cleaned := path.Clean(input)
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = path.Join(r.cfg.root, cleaned)
	}
	if cleaned == r.cfg.root || strings.HasPrefix(cleaned, r.cfg.root+"/") {
		return cleaned, nil
	}
	return "", errors.New("path is outside the remote root")
}

func (r *remote) read(args readArgs) (string, error) {
	target, err := r.resolve(args.Path)
	if err != nil {
		return "", err
	}
	file, err := r.sftp.Open(target)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", args.Path)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxInput))
	if err != nil {
		return "", err
	}
	if int64(len(content)) == maxInput && info.Size() > maxInput {
		return "", fmt.Errorf("%s is larger than %d bytes; read it with bash", args.Path, maxInput)
	}
	return filetext.Read(content, args.Offset, args.Limit)
}

func (r *remote) write(args writeArgs) (string, error) {
	target, err := r.resolve(args.Path)
	if err != nil {
		return "", err
	}
	if err := r.atomicWrite(target, []byte(args.Content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)", args.Path, len(args.Content)), nil
}

func (r *remote) edit(args editArgs) (string, error) {
	target, err := r.resolve(args.Path)
	if err != nil {
		return "", err
	}
	file, err := r.sftp.Open(target)
	if err != nil {
		return "", err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxInput))
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	edits := make([]filetext.Edit, 0, len(args.Edits))
	for _, item := range args.Edits {
		edits = append(edits, filetext.Edit{OldText: item.OldText, NewText: item.NewText})
	}
	updated, message, err := filetext.Apply(content, args.Path, edits)
	if err != nil {
		return "", err
	}
	if err := r.atomicWrite(target, updated); err != nil {
		return "", err
	}
	return message, nil
}

func (r *remote) atomicWrite(target string, content []byte) error {
	dir := path.Dir(target)
	if err := r.sftp.MkdirAll(dir); err != nil {
		return err
	}
	temporary := path.Join(dir, fmt.Sprintf(".sshx-%d-%d", os.Getpid(), temporaryID.Add(1)))
	file, err := r.sftp.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	defer func() { _ = r.sftp.Remove(temporary) }()
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	_ = file.Sync()
	if err := file.Close(); err != nil {
		return err
	}
	if err := r.sftp.PosixRename(temporary, target); err != nil {
		return r.sftp.Rename(temporary, target)
	}
	return nil
}

func (r *remote) bash(args bashArgs) (string, error) {
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return "", errors.New("command is required")
	}
	timeout := 120
	if args.Timeout != nil {
		timeout = *args.Timeout
	}
	if timeout < 1 || timeout > 600 {
		return "", errors.New("timeout must be between 1 and 600 seconds")
	}
	session, err := r.client.NewSession()
	if err != nil {
		return "", err
	}
	defer func() { _ = session.Close() }()
	var output limitedBuffer
	session.Stdout = &output
	session.Stderr = &output
	done := make(chan error, 1)
	go func() {
		done <- session.Run("cd " + shellQuote(r.rootOrHome()) + " && " + command)
	}()
	select {
	case err := <-done:
		if err != nil {
			if text := strings.TrimSpace(output.String()); text != "" {
				return text, nil
			}
			return "", fmt.Errorf("command failed: %w", err)
		}
		text := strings.TrimSpace(output.String())
		if text == "" {
			return "ok", nil
		}
		return text, nil
	case <-time.After(time.Duration(timeout) * time.Second):
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		return "", fmt.Errorf("command timed out after %d seconds; its side effects are unknown", timeout)
	}
}

func (r *remote) rootOrHome() string {
	if r.cfg.root != "" {
		return r.cfg.root
	}
	return "."
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

type limitedBuffer struct {
	data      []byte
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if len(b.data) > maxOutput {
		b.data = b.data[len(b.data)-maxOutput:]
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	if b == nil {
		return ""
	}
	text := string(b.data)
	if b.truncated {
		return "[output truncated to the last 16KB]\n" + text
	}
	return text
}

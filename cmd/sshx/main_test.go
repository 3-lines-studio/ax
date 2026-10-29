package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type testServer struct {
	dir        string
	host       string
	knownHosts string
	keyFile    string
}

func startTestServer(t *testing.T) *testServer {
	t.Helper()
	dir := t.TempDir()
	clientKey, clientPub := generateKey(t)
	hostKey, hostPub := generateKey(t)

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(clientPub.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown key")
		},
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go serveTestConn(connection, config, dir)
		}
	}()

	keyFile := filepath.Join(dir, "id_ed25519")
	block, err := ssh.MarshalPrivateKey(clientKey, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte(knownhosts.Line([]string{knownhosts.Normalize(listener.Addr().String())}, hostPub)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &testServer{dir: dir, host: listener.Addr().String(), knownHosts: knownHosts, keyFile: keyFile}
}

func generateKey(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	return private, public
}

func serveTestConn(connection net.Conn, config *ssh.ServerConfig, dir string) {
	defer func() { _ = connection.Close() }()
	server, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		return
	}
	defer func() { _ = server.Close() }()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go func() {
			for request := range channelRequests {
				switch request.Type {
				case "subsystem":
					var payload struct{ Name string }
					if err := ssh.Unmarshal(request.Payload, &payload); err != nil || payload.Name != "sftp" {
						_ = request.Reply(false, nil)
						continue
					}
					_ = request.Reply(true, nil)
					sftpServer, err := sftp.NewServer(channel)
					if err == nil {
						_ = sftpServer.Serve()
						_ = sftpServer.Close()
					}
					_ = channel.Close()
					return
				case "exec":
					var payload struct{ Command string }
					if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
						_ = request.Reply(false, nil)
						continue
					}
					_ = request.Reply(true, nil)
					cmd := exec.Command("/bin/sh", "-c", payload.Command)
					cmd.Dir = dir
					cmd.Stdout = channel
					cmd.Stderr = channel.Stderr()
					err := cmd.Run()
					status := uint32(0)
					if exitErr, ok := err.(*exec.ExitError); ok {
						status = uint32(exitErr.ExitCode())
					}
					_, _ = channel.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, status))
					_ = channel.Close()
					return
				default:
					_ = request.Reply(false, nil)
				}
			}
		}()
	}
}

func (s *testServer) connect(t *testing.T) *remote {
	t.Helper()
	r, err := dial(config{
		host:       s.host,
		root:       s.dir,
		keyFile:    s.keyFile,
		knownHosts: s.knownHosts,
		timeout:    10e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestDescribeListsRemoteTools(t *testing.T) {
	if len(descriptors) != 4 {
		t.Fatalf("descriptors = %d", len(descriptors))
	}
	for _, descriptor := range descriptors {
		for _, want := range []string{`"name"`, `"parameters"`, `"snippet"`} {
			if !strings.Contains(descriptor, want) {
				t.Errorf("descriptor missing %s: %s", want, descriptor)
			}
		}
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	server := startTestServer(t)
	r := server.connect(t)

	message, err := r.write(writeArgs{Path: "nested/dir/file.txt", Content: "one\ntwo\nthree\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "wrote nested/dir/file.txt") {
		t.Fatalf("write = %q", message)
	}
	if data, err := os.ReadFile(filepath.Join(server.dir, "nested/dir/file.txt")); err != nil || string(data) != "one\ntwo\nthree\n" {
		t.Fatalf("remote file = %q err=%v", data, err)
	}

	offset, limit := 2, 1
	text, err := r.read(readArgs{Path: "nested/dir/file.txt", Offset: &offset, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "two") || !strings.Contains(text, "offset=3") {
		t.Fatalf("read = %q", text)
	}
}

func TestWriteIsAtomic(t *testing.T) {
	server := startTestServer(t)
	r := server.connect(t)
	if _, err := r.write(writeArgs{Path: "file.txt", Content: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.write(writeArgs{Path: "file.txt", Content: "second"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(server.dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("content = %q", data)
	}
	entries, err := os.ReadDir(server.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".sshx-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestEditRejectsAmbiguousMatch(t *testing.T) {
	server := startTestServer(t)
	r := server.connect(t)
	if _, err := r.write(writeArgs{Path: "file.txt", Content: "x\nx\n"}); err != nil {
		t.Fatal(err)
	}
	_, err := r.edit(editArgs{Path: "file.txt", Edits: []edit{{OldText: "x", NewText: "y"}}})
	if err == nil || !strings.Contains(err.Error(), "occurrences") {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.edit(editArgs{Path: "file.txt", Edits: []edit{{OldText: "x\nx", NewText: "y\nz"}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(server.dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "y\nz\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestBashRunsInsideTheRoot(t *testing.T) {
	server := startTestServer(t)
	r := server.connect(t)
	out, err := r.bash(bashArgs{Command: "pwd && echo done"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, server.dir) || !strings.Contains(out, "done") {
		t.Fatalf("bash = %q", out)
	}
	if _, err := r.bash(bashArgs{Command: "exit 3"}); err == nil {
		t.Fatal("a failing command must be an error")
	}
}

func TestBashTimesOut(t *testing.T) {
	server := startTestServer(t)
	r := server.connect(t)
	timeout := 1
	_, err := r.bash(bashArgs{Command: "sleep 30", Timeout: &timeout})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}

func TestPathsOutsideTheRootAreRejected(t *testing.T) {
	server := startTestServer(t)
	r := server.connect(t)
	if _, err := r.read(readArgs{Path: "/etc/passwd"}); err == nil || !strings.Contains(err.Error(), "outside the remote root") {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.write(writeArgs{Path: "../escape", Content: "x"}); err == nil || !strings.Contains(err.Error(), "outside the remote root") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(server.dir, "..", "escape")); err == nil {
		t.Fatal("write escaped the root")
	}
}

func TestLoadConfigRequiresHostKey(t *testing.T) {
	t.Setenv("SSHX_HOST", "user@example.com")
	t.Setenv("SSHX_KNOWN_HOSTS", "")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "KNOWN_HOSTS") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("SSHX_HOST", "")
	t.Setenv("SSHX_KNOWN_HOSTS", "/tmp/kh")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "SSHX_HOST") {
		t.Fatalf("err = %v", err)
	}
}

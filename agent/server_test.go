package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// testKeys returns a host signer plus a client signer and its public key.
func testKeys(t *testing.T) (host ssh.Signer, clientSigner ssh.Signer, clientPub ssh.PublicKey) {
	t.Helper()
	_, hpriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	host, err = ssh.NewSignerFromKey(hpriv)
	require.NoError(t, err)

	_, cpriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	clientSigner, err = ssh.NewSignerFromKey(cpriv)
	require.NoError(t, err)
	return host, clientSigner, clientSigner.PublicKey()
}

// pipeConn connects an ssh client to a Server over a loopback TCP socket. A
// real (buffered) transport is required: the SSH version exchange writes before
// it reads on both ends, which deadlocks over the unbuffered net.Pipe. In
// production the transport is the podman-exec stdio pipe, which the kernel
// buffers, so this mirrors real behaviour.
func serveOverTCP(t *testing.T, srv *Server) net.Conn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = srv.Serve(context.Background(), conn, conn)
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	return client
}

// dialAgent wires an ssh client to a Server, authenticating with clientSigner.
func dialAgent(t *testing.T, cfg Config, clientSigner ssh.Signer) *ssh.Client {
	t.Helper()
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	transport := serveOverTCP(t, srv)
	cc := &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, chans, reqs, err := ssh.NewClientConn(transport, "pipe", cc)
	require.NoError(t, err)
	client := ssh.NewClient(conn, chans, reqs)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestExecExitStatusZero(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{HostKey: host, Authorized: []ssh.PublicKey{cp}}, cs)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()

	out, err := sess.Output("echo hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(out))
}

func TestExecExitStatusNonZero(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{HostKey: host, Authorized: []ssh.PublicKey{cp}}, cs)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()

	err = sess.Run("exit 3")
	var ee *ssh.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 3, ee.ExitStatus())
}

func TestExecEnvAndCwd(t *testing.T) {
	host, cs, cp := testKeys(t)
	dir := t.TempDir()
	cfg := Config{
		HostKey:     host,
		Authorized:  []ssh.PublicKey{cp},
		Cwd:         dir,
		Environment: map[string]string{"DEVC_MARKER": "present"},
	}
	client := dialAgent(t, cfg, cs)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()
	out, err := sess.Output("echo $DEVC_MARKER; pwd")
	require.NoError(t, err)
	assert.Contains(t, string(out), "present")
	assert.Contains(t, string(out), dir)
}

func TestRejectsWrongKey(t *testing.T) {
	host, _, cp := testKeys(t)
	// Authorize cp, but connect with a different key.
	_, wrongSigner, _ := testKeys(t)

	srv, err := NewServer(Config{HostKey: host, Authorized: []ssh.PublicKey{cp}})
	require.NoError(t, err)
	transport := serveOverTCP(t, srv)

	cc := &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(wrongSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	_, _, _, err = ssh.NewClientConn(transport, "pipe", cc)
	require.Error(t, err, "unauthorized key must be rejected")
}

func TestDirectTCPIP(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{HostKey: host, Authorized: []ssh.PublicKey{cp}}, cs)

	// A local echo listener the forwarded channel should reach.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c) // echo
	}()

	conn, err := client.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	msg := []byte("ping-through-tunnel")
	_, err = conn.Write(msg)
	require.NoError(t, err)

	buf := make([]byte, len(msg))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, msg, buf)
}

func TestRemoteForwardTCPIP(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{HostKey: host, Authorized: []ssh.PublicKey{cp}}, cs)

	// `ssh -R`: ask the agent to listen (inside the container) and forward
	// accepted connections back to us over the ssh connection.
	ln, err := client.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	// Echo whatever arrives on the client side of the forward.
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()

	// A plain TCP dial to the agent-side listen address must be tunnelled back.
	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	msg := []byte("ping-remote-forward")
	_, err = conn.Write(msg)
	require.NoError(t, err)

	buf := make([]byte, len(msg))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, msg, buf)
}

// TestRemoteForwardCancelReleasesPort: the client knows a port-0 forward only
// by the port the agent allocated, and cancel-tcpip-forward carries that. A
// server filing the listener under the requested port reports success but
// leaves it bound.
func TestRemoteForwardCancelReleasesPort(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{HostKey: host, Authorized: []ssh.PublicKey{cp}}, cs)

	ln, err := client.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := tcpAddr.Port
	require.NotZero(t, port, "the agent must report the port it allocated")

	require.NoError(t, ln.Close())

	// Must be released, not just acknowledged. The reply and the close are not
	// ordered, so allow a moment.
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = probe.Close()
			return
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port %d still bound after the forward was cancelled: %v", port, lastErr)
}

func TestInteractivePTY(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{HostKey: host, Authorized: []ssh.PublicKey{cp}}, cs)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()

	require.NoError(t, sess.RequestPty("xterm", 24, 80, ssh.TerminalModes{}))

	stdin, err := sess.StdinPipe()
	require.NoError(t, err)
	// stdout and stderr must be distinct sinks: the ssh client copies each
	// stream in its own goroutine, so sharing one bytes.Buffer is a data race.
	// Under a PTY all child output arrives on the stdout channel anyway.
	var out, errOut bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errOut
	require.NoError(t, sess.Shell())

	fmt.Fprintln(stdin, "echo pty-works")
	fmt.Fprintln(stdin, "exit")
	require.NoError(t, sess.Wait())
	assert.Contains(t, out.String(), "pty-works")
}

// syncBuffer lets the test poll stdout while the ssh client writes it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestWindowChangeResizesRunningPTY covers a resize during a session, the only
// time one happens. A server that stops reading requests once the shell starts
// ignores every resize.
func TestWindowChangeResizesRunningPTY(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{HostKey: host, Authorized: []ssh.PublicKey{cp}}, cs)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()
	require.NoError(t, sess.RequestPty("xterm", 24, 80, ssh.TerminalModes{}))

	stdin, err := sess.StdinPipe()
	require.NoError(t, err)
	out := &syncBuffer{}
	sess.Stdout = out
	sess.Stderr = io.Discard
	require.NoError(t, sess.Shell())

	require.NoError(t, sess.WindowChange(40, 100))

	// The resize and the shell's readiness race, so ask repeatedly.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fmt.Fprintln(stdin, "stty size")
		time.Sleep(100 * time.Millisecond)
		if strings.Contains(out.String(), "40 100") {
			return // resized
		}
	}
	t.Fatalf("pty was never resized to 40x100; shell saw:\n%s", out.String())
}

// TestChannelRequestsDoNotStallConnection is the sharp edge of the same defect.
// x/crypto/ssh feeds channel requests from the connection's one mux loop with a
// blocking send into a 16-slot buffer, so a server that stops draining them
// does not just ignore resizes - the whole connection freezes.
func TestChannelRequestsDoNotStallConnection(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{HostKey: host, Authorized: []ssh.PublicKey{cp}}, cs)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()
	require.NoError(t, sess.RequestPty("xterm", 24, 80, ssh.TerminalModes{}))
	_, err = sess.StdinPipe()
	require.NoError(t, err)
	sess.Stdout, sess.Stderr = io.Discard, io.Discard
	require.NoError(t, sess.Shell())

	// Comfortably more than chanSize; a user dragging a window emits this many.
	for i := 0; i < 4*16; i++ {
		require.NoError(t, sess.WindowChange(24+i%10, 80+i%10))
	}

	// Under a timeout: a wedged mux loop would hang the test binary.
	done := make(chan error, 1)
	go func() {
		s2, err := client.NewSession()
		if err != nil {
			done <- err
			return
		}
		defer s2.Close()
		_, err = s2.Output("echo still-alive")
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("connection stalled: a new session could not be opened after a channel-request flood")
	}
}

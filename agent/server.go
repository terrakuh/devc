// Package agent implements the in-container SSH server that devc injects and
// runs over `podman exec -i ... /.devc/agent __serve`. The transport is stdin/
// stdout - there is no TCP listener and no published port - but the SSH crypto
// still runs so a real client (VSCodium Remote-SSH, OpenSSH) connects normally.
//
// The server accepts a single connection (the one exec pipe it was started for)
// and serves every channel the client opens on it: interactive/exec sessions
// with optional PTY, and direct-tcpip forwards (how the editor reaches its own
// server port and how `ssh -L` works).
package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// debugEnvKey names a remoteEnv entry whose value is a file path. When set, the
// agent appends a trace of its connection, channel, and forwarding events there.
// It is a diagnostic hook, off unless the path is provided.
const debugEnvKey = "DEVC_AGENT_LOG"

// Config configures a Server. All fields are required except Environment.
type Config struct {
	// HostKey is the server's host key (from /.devc/host_key).
	HostKey ssh.Signer
	// Authorized is the set of public keys allowed to connect (the workspace's
	// single client key).
	Authorized []ssh.PublicKey
	// User is the OS user sessions run as (remoteUser); empty = the agent's own uid.
	User string
	// Cwd is the working directory for sessions (containerWorkspaceFolder).
	Cwd string
	// Environment is injected into every session (remoteEnv + probed env).
	Environment map[string]string
	// AgentForwarding allows ssh-agent forwarding: when the client requests it
	// (auth-agent-req@openssh.com), the server exposes a proxy SSH_AUTH_SOCK that
	// tunnels signing requests back to the client's agent. Off by default.
	AgentForwarding bool
}

// Server serves one SSH connection over a byte stream.
type Server struct {
	cfg  Config
	dbg  *log.Logger // nil unless DEVC_AGENT_LOG is set
	conn ssh.Conn    // set in Serve; used to open auth-agent channels back

	// Agent forwarding is per connection, not per session: VS Code launches its
	// server on one session and runs terminals as its children, so the forwarded
	// socket must outlive any single session. Created lazily on first request.
	fwdMu sync.Mutex
	fwd   *agentForward
}

// NewServer validates cfg and returns a Server.
func NewServer(cfg Config) (*Server, error) {
	if cfg.HostKey == nil {
		return nil, fmt.Errorf("agent: HostKey is required")
	}
	if len(cfg.Authorized) == 0 {
		return nil, fmt.Errorf("agent: at least one authorized key is required")
	}
	return &Server{cfg: cfg, dbg: newDebugLogger(cfg.Environment[debugEnvKey])}, nil
}

// newDebugLogger opens the trace file in append mode. A missing path or an open
// error just disables logging; diagnostics must never break the agent.
func newDebugLogger(path string) *log.Logger {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // operator-provided path
	if err != nil {
		return nil
	}
	return log.New(f, fmt.Sprintf("pid=%d ", os.Getpid()), log.LstdFlags|log.Lmicroseconds)
}

// debugf writes a trace line when logging is enabled.
func (s *Server) debugf(format string, a ...any) {
	if s.dbg != nil {
		s.dbg.Printf(format, a...)
	}
}

// rwc adapts a separate reader and writer (stdin/stdout) into the net.Conn that
// ssh.NewServerConn requires. The address and deadline methods are inert: the
// transport is a pipe, not a socket, so there is nothing to name or time out at
// this layer (ctx cancellation closes the connection instead).
type rwc struct {
	io.Reader
	io.Writer
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "devc-agent" }

func (rwc) Close() error                       { return nil }
func (rwc) LocalAddr() net.Addr                { return pipeAddr{} }
func (rwc) RemoteAddr() net.Addr               { return pipeAddr{} }
func (rwc) SetDeadline(_ time.Time) error      { return nil }
func (rwc) SetReadDeadline(_ time.Time) error  { return nil }
func (rwc) SetWriteDeadline(_ time.Time) error { return nil }

// Serve runs the SSH server over the given stream until the client disconnects
// or ctx is cancelled. Pass stdin as r and stdout as w in the __serve command.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	serverConf := &ssh.ServerConfig{
		PublicKeyCallback: s.authenticate,
		// No passwords, no keyboard-interactive: publickey only.
	}
	serverConf.AddHostKey(s.cfg.HostKey)

	conn, chans, globalReqs, err := ssh.NewServerConn(rwc{Reader: r, Writer: w}, serverConf)
	if err != nil {
		return fmt.Errorf("ssh handshake: %w", err)
	}
	defer conn.Close()
	s.conn = conn
	defer s.closeAgentForward()
	s.debugf("connection established: user=%s forwarding-allowed=%v", conn.User(), s.cfg.AgentForwarding)

	// Cancel the handler tree when ctx is done or the connection dies.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	// Remote forwards (`ssh -R`, tcpip-forward) open listeners inside the
	// container and stream accepted connections back over this conn. Track them
	// per-connection so they can be cancelled and are all closed on disconnect.
	fwds := newRemoteForwards(conn)
	defer fwds.closeAll()
	go s.handleGlobalRequests(ctx, fwds, globalReqs)

	for newChan := range chans {
		s.debugf("channel open: type=%s", newChan.ChannelType())
		switch newChan.ChannelType() {
		case "session":
			go s.handleSession(ctx, newChan)
		case "direct-tcpip":
			go s.handleDirectTCPIP(ctx, newChan)
		default:
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel type: "+newChan.ChannelType())
		}
	}
	return nil
}

// authenticate accepts a connection whose key matches one of the authorized
// keys, comparing marshaled key bytes.
func (s *Server) authenticate(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	want := key.Marshal()
	for _, ak := range s.cfg.Authorized {
		if keyEqual(ak.Marshal(), want) {
			return &ssh.Permissions{
				Extensions: map[string]string{
					"pubkey-fp": base64.StdEncoding.EncodeToString(want),
				},
			}, nil
		}
	}
	return nil, fmt.Errorf("public key rejected")
}

// keyEqual is a length-checked, constant-time-ish byte comparison. The keys are
// public, so this is defense-in-depth rather than a strict requirement.
func keyEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

package agent

import (
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// agentForward is the connection's ssh-agent forwarding state: a Unix socket in
// the container that proxies to the client's ssh-agent. No key material lives
// here; each socket connection is bridged back to the client, which signs.
type agentForward struct {
	dir  string // temp dir holding the socket, removed on cleanup
	sock string // SSH_AUTH_SOCK path handed to sessions
	ln   net.Listener
}

// handleAgentForwardReq handles an "auth-agent-req@openssh.com" request. It
// points the session's SSH_AUTH_SOCK at the connection-wide proxy socket, which
// buildEnv then passes to the shell/exec (the request always arrives first). The
// request is refused when AgentForwarding is off, even if the client asked.
func (s *session) handleAgentForwardReq(req *ssh.Request) {
	if !s.srv.cfg.AgentForwarding {
		s.srv.debugf("auth-agent-req refused: forwarding-allowed=false")
		reply(req, false)
		return
	}
	sock, err := s.srv.ensureAgentForward()
	if err != nil {
		s.srv.debugf("auth-agent-req failed: %v", err)
		reply(req, false)
		return
	}
	// SSH_AUTH_SOCK is injected by buildEnv from the connection-wide socket, so
	// every later session sees it too, not just this one.
	s.srv.debugf("auth-agent-req accepted: SSH_AUTH_SOCK=%s", sock)
	reply(req, true)
}

// ensureAgentForward returns the connection's proxy socket path, opening it on
// first use. Later sessions share the same socket so a long-lived process (the
// VS Code server) and its children all reach the same agent.
func (s *Server) ensureAgentForward() (string, error) {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
	if s.fwd != nil {
		return s.fwd.sock, nil
	}
	u, err := resolveUser(s.cfg.User)
	if err != nil {
		return "", err
	}
	fwd, err := newAgentForward(u)
	if err != nil {
		return "", err
	}
	s.fwd = fwd
	go s.serveAgentForward(fwd.ln)
	return fwd.sock, nil
}

// newAgentForward creates the listening Unix socket. It lives under /tmp to keep
// the path short (Unix sockets cap at ~108 bytes) and is chown'd to the session
// user so a non-root remote user can connect, the same as sshd does.
func newAgentForward(u *userInfo) (*agentForward, error) {
	dir, err := os.MkdirTemp("", "devc-agent-")
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if u.setCred {
		// The child runs as a different user and must be able to reach the socket.
		// The agent runs as root here, so chown succeeds. Best-effort.
		_ = os.Chown(dir, int(u.uid), int(u.gid))
		_ = os.Chown(sock, int(u.uid), int(u.gid))
	}
	return &agentForward{dir: dir, sock: sock, ln: ln}, nil
}

// serveAgentForward forwards each connection on the proxy socket to the client's
// agent over its own auth-agent@openssh.com channel.
func (s *Server) serveAgentForward(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed on connection teardown
		}
		go s.forwardAgentConn(conn)
	}
}

// forwardAgentConn bridges one socket connection to a new auth-agent channel.
// The client answers the channel by talking to the real ssh-agent, so the
// private key never enters the container.
func (s *Server) forwardAgentConn(conn net.Conn) {
	defer conn.Close()
	ch, reqs, err := s.conn.OpenChannel("auth-agent@openssh.com", nil)
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	bridge(ch, conn) // reuse the tcp-forward bridge helper
}

// agentSock returns the connection's proxy socket path, or "" if forwarding is
// not active. Sessions call it to inject SSH_AUTH_SOCK.
func (s *Server) agentSock() string {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
	if s.fwd == nil {
		return ""
	}
	return s.fwd.sock
}

// closeAgentForward tears the proxy socket down when the connection ends.
func (s *Server) closeAgentForward() {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
	if s.fwd == nil {
		return
	}
	_ = s.fwd.ln.Close()
	_ = os.RemoveAll(s.fwd.dir)
	s.fwd = nil
	s.debugf("agent forward closed")
}

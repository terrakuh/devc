package agent

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

// session accumulates the client's pty/env requests, then runs a shell or
// command when it receives "shell" or "exec".
type session struct {
	srv     *Server
	channel ssh.Channel

	ptyReq  bool
	term    string
	ptmx    *os.File
	winCh   chan winSize
	env     map[string]string
	started bool
}

type winSize struct{ cols, rows, x, y uint32 }

// handleSession accepts a session channel and drives its request loop.
func (s *Server) handleSession(ctx context.Context, newChan ssh.NewChannel) {
	channel, requests, err := newChan.Accept()
	if err != nil {
		return
	}
	sess := &session{
		srv:     s,
		channel: channel,
		env:     map[string]string{},
		winCh:   make(chan winSize, 4),
	}
	defer channel.Close()

	for req := range requests {
		switch req.Type {
		case "pty-req":
			sess.handlePTYReq(req)
		case "env":
			sess.handleEnv(req)
		case "window-change":
			sess.handleWindowChange(req)
		case "shell":
			sess.start(ctx, req, nil)
		case "exec":
			sess.start(ctx, req, execPayload(req.Payload))
		case "signal":
			// Best-effort; the child gets SIGHUP when the channel closes anyway.
			reply(req, true)
		case "subsystem":
			if subsystemName(req.Payload) == "sftp" {
				sess.startSFTP(ctx, req)
			} else {
				reply(req, false)
			}
		default:
			reply(req, false)
		}
	}
}

func (s *session) handlePTYReq(req *ssh.Request) {
	term, _, _, cols, rows, ok := parsePTYReq(req.Payload)
	if !ok {
		reply(req, false)
		return
	}
	s.ptyReq = true
	s.term = term
	select {
	case s.winCh <- winSize{cols: cols, rows: rows}:
	default:
	}
	reply(req, true)
}

func (s *session) handleEnv(req *ssh.Request) {
	name, value, ok := parseEnv(req.Payload)
	if ok {
		s.env[name] = value
	}
	reply(req, true)
}

func (s *session) handleWindowChange(req *ssh.Request) {
	cols, rows, _, _, ok := parseWindowChange(req.Payload)
	if ok && s.ptmx != nil {
		_ = pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	}
	reply(req, false)
}

// start launches the shell (command == nil) or the given command. It replies to
// the request, then runs to completion and sends exit-status.
func (s *session) start(ctx context.Context, req *ssh.Request, command *string) {
	if s.started {
		reply(req, false)
		return
	}
	s.started = true

	cmd, err := s.buildCommand(ctx, command)
	if err != nil {
		reply(req, false)
		s.exit(1)
		return
	}
	reply(req, true)

	if s.ptyReq {
		s.runWithPTY(cmd)
	} else {
		s.runWithPipes(cmd)
	}
}

// buildCommand assembles the *exec.Cmd: the login shell (interactive) or
// `<shell> -c <command>`, as the configured user, in Cwd, with the merged
// environment.
func (s *session) buildCommand(ctx context.Context, command *string) (*exec.Cmd, error) {
	u, err := resolveUser(s.srv.cfg.User)
	if err != nil {
		return nil, err
	}
	var args []string
	if command == nil {
		args = []string{"-l"} // login shell for interactive sessions
	} else {
		args = []string{"-c", *command}
	}
	return s.userCommand(ctx, u, u.shell, args...), nil
}

// userCommand builds an *exec.Cmd to run name+args as u, in the session's cwd,
// with the merged environment and (when switching identity) the user's process
// credentials. Setsid detaches the child into its own session.
func (s *session) userCommand(ctx context.Context, u *userInfo, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = s.cwd(u)
	cmd.Env = s.buildEnv(u)
	if u.setCred {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: u.uid, Gid: u.gid},
			Setsid:     true,
		}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	return cmd
}

func (s *session) cwd(u *userInfo) string {
	if s.srv.cfg.Cwd != "" {
		if fi, err := os.Stat(s.srv.cfg.Cwd); err == nil && fi.IsDir() {
			return s.srv.cfg.Cwd
		}
	}
	return u.home
}

// buildEnv merges, in increasing precedence: a minimal base, the configured
// environment (remoteEnv + probe), then the client-sent env requests.
func (s *session) buildEnv(u *userInfo) []string {
	merged := map[string]string{
		"HOME":    u.home,
		"USER":    u.name,
		"LOGNAME": u.name,
		"SHELL":   u.shell,
		"PATH":    "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	for k, v := range s.srv.cfg.Environment {
		merged[k] = v
	}
	for k, v := range s.env {
		merged[k] = v
	}
	if s.ptyReq && s.term != "" {
		merged["TERM"] = s.term
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}

// runWithPTY starts cmd attached to a new PTY and bridges it to the channel.
func (s *session) runWithPTY(cmd *exec.Cmd) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		s.exit(1)
		return
	}
	s.ptmx = ptmx
	defer func() { _ = ptmx.Close() }()

	// Apply any pending window size.
	select {
	case ws := <-s.winCh:
		_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(ws.cols), Rows: uint16(ws.rows)})
	default:
	}

	// client -> pty. This goroutine blocks reading the channel; it is unblocked
	// when exit() closes the channel below (the client may never close its own
	// stdin, so we must not wait on it).
	go func() { _, _ = io.Copy(ptmx, s.channel) }()

	_, _ = io.Copy(s.channel, ptmx) // pty -> client, ends when the shell exits

	err = cmd.Wait()
	_ = ptmx.Close()
	s.exit(exitCode(err))
}

// runWithPipes runs cmd without a TTY, wiring stdio to the channel.
func (s *session) runWithPipes(cmd *exec.Cmd) {
	cmd.Stdout = s.channel
	cmd.Stderr = s.channel.Stderr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.exit(1)
		return
	}
	if err := cmd.Start(); err != nil {
		s.exit(127)
		return
	}
	go func() {
		_, _ = io.Copy(stdin, s.channel)
		_ = stdin.Close()
	}()
	err = cmd.Wait()
	s.exit(exitCode(err))
}

// exit sends the SSH exit-status request and closes the channel. VSCodium (and
// ssh) rely on this to know a command finished and with what code.
func (s *session) exit(code int) {
	var payload [4]byte
	binary.BigEndian.PutUint32(payload[:], uint32(code)) //nolint:gosec // status fits
	_, _ = s.channel.SendRequest("exit-status", false, payload[:])
	_ = s.channel.Close()
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if ok := asExitError(err, &ee); ok {
		if ee.ExitCode() >= 0 {
			return ee.ExitCode()
		}
	}
	return 1
}

func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

func reply(req *ssh.Request, ok bool) {
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

// subsystemName extracts the subsystem name from a "subsystem" request payload.
func subsystemName(payload []byte) string {
	name, _ := readString(payload)
	return name
}

// execPayload extracts the command string from an "exec" request payload.
func execPayload(payload []byte) *string {
	s, ok := readString(payload)
	if !ok {
		empty := ""
		return &empty
	}
	return &s
}

// --- SSH request payload parsers (RFC 4254) ---

func parsePTYReq(payload []byte) (term string, width, height, cols, rows uint32, ok bool) {
	term, rest, ok := readStringRest(payload)
	if !ok {
		return
	}
	if len(rest) < 16 {
		return "", 0, 0, 0, 0, false
	}
	cols = binary.BigEndian.Uint32(rest[0:4])
	rows = binary.BigEndian.Uint32(rest[4:8])
	width = binary.BigEndian.Uint32(rest[8:12])
	height = binary.BigEndian.Uint32(rest[12:16])
	return term, width, height, cols, rows, true
}

func parseWindowChange(payload []byte) (cols, rows, width, height uint32, ok bool) {
	if len(payload) < 16 {
		return
	}
	cols = binary.BigEndian.Uint32(payload[0:4])
	rows = binary.BigEndian.Uint32(payload[4:8])
	width = binary.BigEndian.Uint32(payload[8:12])
	height = binary.BigEndian.Uint32(payload[12:16])
	return cols, rows, width, height, true
}

func parseEnv(payload []byte) (name, value string, ok bool) {
	name, rest, ok := readStringRest(payload)
	if !ok {
		return
	}
	value, ok = readString(rest)
	return name, value, ok
}

func readString(b []byte) (string, bool) {
	s, _, ok := readStringRest(b)
	return s, ok
}

func readStringRest(b []byte) (string, []byte, bool) {
	if len(b) < 4 {
		return "", nil, false
	}
	n := binary.BigEndian.Uint32(b[0:4])
	if uint32(len(b)-4) < n {
		return "", nil, false
	}
	return string(b[4 : 4+n]), b[4+n:], true
}

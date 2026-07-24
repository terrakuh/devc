package agent

import (
	"context"
	"io"
	"os"

	"golang.org/x/crypto/ssh"
)

// SFTPCommand is the argv the agent re-execs to serve SFTP as the session user.
// The main devc binary implements it (running an in-process SFTP server over
// stdin/stdout); the agent only spawns it with dropped privileges. It is a var
// so tests can point it at a stub.
var SFTPCommand = []string{"__sftp"}

// startSFTP handles a `subsystem sftp` request. Editors (VSCodium Remote-SSH)
// use SFTP to upload their server into a container that has no outbound
// internet, so this is required, not optional.
//
// The agent runs as root, but files must be owned by the session user, so the
// SFTP server runs in a privilege-dropped subprocess (the same devc binary
// re-exec'd as __sftp) whose stdio is bridged to the channel - mirroring how
// sshd forks its sftp-server as the user.
func (s *session) startSFTP(ctx context.Context, req *ssh.Request) {
	if s.started {
		reply(req, false)
		return
	}
	s.started = true

	self, err := os.Executable()
	if err != nil {
		reply(req, false)
		s.exit(127)
		return
	}
	u, err := resolveUser(s.srv.cfg.User)
	if err != nil {
		reply(req, false)
		s.exit(1)
		return
	}

	argv := append([]string{}, SFTPCommand...)
	cmd := s.userCommand(ctx, u, self, argv...)
	cmd.Stdout = s.channel
	cmd.Stderr = s.channel.Stderr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		reply(req, false)
		s.exit(1)
		return
	}
	if err := cmd.Start(); err != nil {
		reply(req, false)
		s.exit(127)
		return
	}
	reply(req, true)

	go func() {
		_, _ = io.Copy(stdin, s.channel)
		_ = stdin.Close()
	}()
	err = cmd.Wait()
	s.exit(exitCode(err))
}

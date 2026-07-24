package main

import (
	"errors"
	"io"
	"os"

	"github.com/pkg/sftp"
)

// runSFTP is the hidden entrypoint the agent re-execs (with the session user's
// privileges already dropped by the parent) to serve the SFTP subsystem over
// stdin/stdout. Editors upload their server through this when the container has
// no outbound internet.
func runSFTP() error {
	server, err := sftp.NewServer(stdio{})
	if err != nil {
		return err
	}
	defer server.Close()
	if err := server.Serve(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// stdio adapts the process's stdin/stdout into the io.ReadWriteCloser sftp
// expects. Close is a no-op - the process exiting closes the real fds.
type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return nil }

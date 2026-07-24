package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// proxyLog is a tiny append-only debug log for the `devc ssh --stdio` transport.
//
// GUI editors (VSCodium / open-remote-ssh) run the ProxyCommand with their
// stderr discarded and report only a generic "connection lost before handshake"
// when devc exits early, giving nothing to debug. proxyLog records each step to
// a fixed per-user file so a failed connection can be explained after the fact:
//
//	cat /tmp/devc-$(id -u)/ssh-proxy.log
//
// It is best-effort: if the log cannot be opened, logging is silently skipped -
// diagnostics must never break the transport. stdout stays untouched (it is the
// ssh pipe); only this file and the process exit are affected.
type proxyLog struct {
	f *os.File
}

// proxyLogPath is the fixed diagnostic file, under the per-uid /tmp dir also used
// for control sockets. A single file (last connection wins) keeps it easy to find.
func proxyLogPath() string {
	return filepath.Join("/tmp", fmt.Sprintf("devc-%d", os.Getuid()), "ssh-proxy.log")
}

// newProxyLog opens (truncating) the diagnostic log for one --stdio invocation.
// Truncation keeps it to the most recent attempt, which is what a user debugging
// a failed reconnect wants to see.
func newProxyLog() *proxyLog {
	path := proxyLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return &proxyLog{}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // fixed per-uid path
	if err != nil {
		return &proxyLog{}
	}
	pl := &proxyLog{f: f}
	pl.logf("=== devc ssh --stdio (pid %d) ===", os.Getpid())
	return pl
}

// logf appends a timestamped line. No-op when the log failed to open.
func (p *proxyLog) logf(format string, args ...any) {
	if p == nil || p.f == nil {
		return
	}
	ts := time.Now().Format("15:04:05.000")
	fmt.Fprintf(p.f, "%s %s\n", ts, fmt.Sprintf(format, args...))
}

// finish records the transport's outcome and closes the file.
func (p *proxyLog) finish(err error) {
	if p == nil || p.f == nil {
		return
	}
	if err != nil {
		p.logf("exit: error: %v", err)
	} else {
		p.logf("exit: ok")
	}
	_ = p.f.Close()
	p.f = nil
}

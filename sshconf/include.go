package sshconf

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteWorkspaceConfig writes the generated Host block(s) to path (0644),
// creating parent directories. The file is fully owned by devc and rewritten
// each time.
func WriteWorkspaceConfig(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644) //nolint:gosec // ssh client config, not secret
}

// EnsureInclude guarantees that userSSHConfig (usually ~/.ssh/config) begins
// with `Include <includePath>`, adding it idempotently. On the first insertion
// it writes a timestamped backup of the existing file. It returns whether a
// change was made.
//
// The Include is placed at the very top because OpenSSH applies the first
// matching option for a keyword, so devc's per-workspace options take
// precedence over any later global defaults.
func EnsureInclude(userSSHConfig, includePath string) (changed bool, err error) {
	includeLine := "Include " + includePath

	existing, err := os.ReadFile(userSSHConfig)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
		existing = nil
	}
	if containsIncludeLine(existing, includeLine) {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(userSSHConfig), 0o700); err != nil {
		return false, err
	}
	// Back up an existing, non-empty config before touching it.
	if len(existing) > 0 {
		backup := fmt.Sprintf("%s.devc-backup-%s", userSSHConfig, time.Now().Format("20060102-150405"))
		if err := os.WriteFile(backup, existing, 0o600); err != nil {
			return false, fmt.Errorf("back up %s: %w", userSSHConfig, err)
		}
	}

	var buf bytes.Buffer
	buf.WriteString("# Added by devc: per-workspace ssh config.\n")
	buf.WriteString(includeLine + "\n")
	if len(existing) > 0 {
		if !bytes.HasPrefix(existing, []byte("\n")) {
			buf.WriteString("\n")
		}
		buf.Write(existing)
	}
	if err := os.WriteFile(userSSHConfig, buf.Bytes(), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// containsIncludeLine reports whether data already has the exact include
// directive (ignoring leading whitespace and matching case-insensitively on the
// Include keyword, as OpenSSH does).
func containsIncludeLine(data []byte, includeLine string) bool {
	wantPath := strings.TrimSpace(strings.TrimPrefix(includeLine, "Include "))
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "Include") {
			if strings.TrimSpace(strings.TrimPrefix(line, fields[0])) == wantPath {
				return true
			}
		}
	}
	return false
}

// RemoveWorkspaceConfig deletes a workspace's generated config file (used by
// `devc down --purge`). Absence is not an error.
func RemoveWorkspaceConfig(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

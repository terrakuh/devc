package agent

import (
	"os"
	"os/user"
	"strconv"
	"strings"
)

// userInfo is the resolved identity a session runs as.
type userInfo struct {
	name    string
	uid     uint32
	gid     uint32
	home    string
	shell   string
	setCred bool // set process credentials (target user differs from current)
}

// resolveUser resolves the target session user. An empty name (or the current
// user's name) means "run as the agent's own uid" and skips setuid. Otherwise
// the user is looked up in /etc/passwd (os/user is pure-Go in a CGO-disabled
// build), and the process credentials are set.
func resolveUser(name string) (*userInfo, error) {
	cur, _ := user.Current()
	if name == "" || (cur != nil && (name == cur.Username || name == cur.Uid)) {
		return fromUser(cur, false), nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		// Fall back to current user rather than failing the whole session.
		return fromUser(cur, false), nil
	}
	// Only set credentials if we are actually switching identity.
	setCred := cur == nil || u.Uid != cur.Uid
	return fromUser(u, setCred), nil
}

// fromUser builds a userInfo from an *user.User (nil-safe), filling defaults.
func fromUser(u *user.User, setCred bool) *userInfo {
	info := &userInfo{home: "/", name: "root"}
	if u != nil {
		info.name = u.Username
		info.home = u.HomeDir
		info.uid = parseID(u.Uid)
		info.gid = parseID(u.Gid)
	}
	if info.home == "" {
		info.home = "/"
	}
	info.setCred = setCred
	info.shell = resolveShell(info.name)
	return info
}

func parseID(s string) uint32 {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// resolveShell picks the login shell for a user: the /etc/passwd entry if
// present and existing, otherwise the first of bash/sh that exists.
func resolveShell(name string) string {
	if sh := passwdShell(name); sh != "" {
		if _, err := os.Stat(sh); err == nil {
			return sh
		}
	}
	for _, sh := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			return sh
		}
	}
	return "/bin/sh"
}

// passwdShell reads the login-shell field for name from /etc/passwd.
func passwdShell(name string) string {
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 7 && fields[0] == name {
			return fields[6]
		}
	}
	return ""
}

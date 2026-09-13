package agent

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/terrakuh/devc/container"
	"golang.org/x/crypto/ssh"
)

// sessionUserEnvKey names the user the re-exec child drops to. Empty means the
// process is not that child.
const sessionUserEnvKey = "DEVC_TEST_SESSION_USER"

// TestMain doubles as the SFTP helper process. When the agent re-execs this
// test binary to serve SFTP (SFTPCommand is emptied in the test so only the
// executable runs), the GO_WANT_SFTP_HELPER env var - injected via the agent's
// session Environment - routes it here to run an in-process SFTP server over
// stdin/stdout instead of the normal test suite.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_SFTP_HELPER") == "1" {
		srv, err := sftp.NewServer(sftpStdio{})
		if err != nil {
			os.Exit(1)
		}
		_ = srv.Serve()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type sftpStdio struct{}

func (sftpStdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (sftpStdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (sftpStdio) Close() error                { return nil }

func TestSFTPRoundTrip(t *testing.T) {
	host, cs, cp := testKeys(t)
	dir := t.TempDir()

	// Route the re-exec'd helper to the SFTP server, and run only the bare
	// executable (no __sftp arg the go-test binary would choke on).
	orig := SFTPCommand
	SFTPCommand = []string{}
	t.Cleanup(func() { SFTPCommand = orig })

	cfg := Config{
		HostKey:     host,
		Authorized:  []ssh.PublicKey{cp},
		Cwd:         dir,
		Environment: map[string]string{"GO_WANT_SFTP_HELPER": "1"},
	}
	client := dialAgent(t, cfg, cs)

	sc, err := sftp.NewClient(client)
	require.NoError(t, err)
	defer sc.Close()

	// Upload a file (the VSCodium "upload the server" path).
	remotePath := filepath.Join(dir, "uploaded.txt")
	f, err := sc.Create(remotePath)
	require.NoError(t, err)
	payload := []byte("hello over sftp")
	_, err = f.Write(payload)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// It really landed on the host filesystem the agent serves.
	onDisk, err := os.ReadFile(remotePath)
	require.NoError(t, err)
	assert.Equal(t, payload, onDisk)

	// And it reads back through SFTP (get path).
	rf, err := sc.Open(remotePath)
	require.NoError(t, err)
	defer rf.Close()
	got := make([]byte, len(payload))
	_, err = rf.Read(got)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// Stat works too (editors probe for an existing server dir).
	fi, err := sc.Stat(remotePath)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), fi.Size())
}

// TestSFTPReExecAsSessionUser covers what TestSFTPRoundTrip cannot: a non-root
// remoteUser. startSFTP re-execs os.Executable() as that user, so it must be
// able to traverse the directory the binary sits in. The 0700 row is the
// regression.
func TestSFTPReExecAsSessionUser(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to drop privileges to another user")
	}
	sessionUser, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no unprivileged 'nobody' user to drop to")
	}

	self, err := os.Executable()
	require.NoError(t, err)
	binary, err := os.ReadFile(self)
	require.NoError(t, err)

	for _, tc := range []struct {
		name    string
		mode    os.FileMode
		wantErr bool
	}{
		{"injected mode", parseMode(t, container.AgentDirMode), false},
		{"0700 blocks the privilege-dropped re-exec", 0o700, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Everything above the agent dir must be traversable, so its own
			// mode is the only one under test. t.TempDir is 0700 at each level.
			root, err := os.MkdirTemp("", "devc-sftp-")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			require.NoError(t, os.Chmod(root, 0o755))

			agentDir := filepath.Join(root, "dotdevc")
			require.NoError(t, os.Mkdir(agentDir, tc.mode))
			agentBin := filepath.Join(agentDir, "agent")
			require.NoError(t, os.WriteFile(agentBin, binary, 0o755))
			// Mkdir is subject to umask; set the mode we actually mean.
			require.NoError(t, os.Chmod(agentDir, tc.mode))

			// Where the upload lands - the session user must be able to write it.
			workdir := filepath.Join(root, "work")
			require.NoError(t, os.Mkdir(workdir, 0o777))
			require.NoError(t, os.Chmod(workdir, 0o777))

			// Re-exec the staged copy so its os.Executable() is the path under test.
			cmd := exec.Command(agentBin, "-test.run=TestSFTPReExecChild", "-test.v")
			cmd.Env = append(os.Environ(),
				sessionUserEnvKey+"="+sessionUser.Username,
				"DEVC_TEST_WORKDIR="+workdir,
			)
			out, err := cmd.CombinedOutput()
			t.Logf("child output:\n%s", out)

			if tc.wantErr {
				require.Error(t, err, "a 0700 agent dir must break the SFTP re-exec")
				return
			}
			require.NoError(t, err)

			// Really uploaded, and really as the session user.
			fi, err := os.Stat(filepath.Join(workdir, "uploaded.txt"))
			require.NoError(t, err)
			st, ok := fi.Sys().(*syscall.Stat_t)
			require.True(t, ok)
			assert.Equal(t, sessionUser.Uid, strconv.FormatUint(uint64(st.Uid), 10),
				"uploaded file must be owned by the session user")
		})
	}
}

// TestSFTPReExecChild is the child half of TestSFTPReExecAsSessionUser, run
// only when re-exec'd from the staged agent directory.
func TestSFTPReExecChild(t *testing.T) {
	sessionUser := os.Getenv(sessionUserEnvKey)
	if sessionUser == "" {
		t.Skip("not the re-exec child")
	}

	host, cs, cp := testKeys(t)
	// Bare executable, so the re-exec lands in TestMain's helper branch.
	orig := SFTPCommand
	SFTPCommand = []string{}
	t.Cleanup(func() { SFTPCommand = orig })

	workdir := os.Getenv("DEVC_TEST_WORKDIR")
	client := dialAgent(t, Config{
		HostKey:     host,
		Authorized:  []ssh.PublicKey{cp},
		User:        sessionUser,
		Cwd:         workdir,
		Environment: map[string]string{"GO_WANT_SFTP_HELPER": "1"},
	}, cs)

	sc, err := sftp.NewClient(client)
	require.NoError(t, err)
	defer sc.Close()

	f, err := sc.Create(filepath.Join(workdir, "uploaded.txt"))
	require.NoError(t, err)
	_, err = f.Write([]byte("hello over sftp"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func parseMode(t *testing.T, s string) os.FileMode {
	t.Helper()
	m, err := strconv.ParseUint(s, 8, 32)
	require.NoError(t, err)
	return os.FileMode(m)
}

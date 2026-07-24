package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

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

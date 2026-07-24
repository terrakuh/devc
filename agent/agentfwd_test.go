package agent

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// TestAgentForwarding exercises the full forward path: the client requests
// agent forwarding, the server exposes SSH_AUTH_SOCK, and a connection to that
// socket reaches the client's keyring (the private key never leaves the client).
func TestAgentForwarding(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{
		HostKey:         host,
		Authorized:      []ssh.PublicKey{cp},
		AgentForwarding: true,
	}, cs)

	// A host-side keyring holding one key, exposed to the client for forwarding.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyring := agent.NewKeyring()
	require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: &priv}))
	require.NoError(t, agent.ForwardToAgent(client, keyring))

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()
	require.NoError(t, agent.RequestAgentForwarding(sess))

	stdin, err := sess.StdinPipe()
	require.NoError(t, err)
	stdout, err := sess.StdoutPipe()
	require.NoError(t, err)

	// Print the forwarded socket path, then block on `cat` so the session (and
	// thus the socket) stays alive while we dial it.
	require.NoError(t, sess.Start("echo $SSH_AUTH_SOCK; cat"))

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	sock := strings.TrimSpace(line)
	require.NotEmpty(t, sock, "SSH_AUTH_SOCK must be set in the session")

	// Dial the proxy socket and confirm it reaches the client's keyring.
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	keys, err := agent.NewClient(conn).List()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	wantPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	assert.Equal(t, wantPub.Marshal(), keys[0].Marshal(), "forwarded agent must expose the client key")

	_ = stdin.Close() // ends `cat`, closing the session
}

// TestAgentForwardingOutlivesSession is the VS Code case: the session that
// requested forwarding ends, but a later process (a child of the VS Code server)
// must still reach the socket. The socket is per connection, so it survives.
func TestAgentForwardingOutlivesSession(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{
		HostKey:         host,
		Authorized:      []ssh.PublicKey{cp},
		AgentForwarding: true,
	}, cs)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyring := agent.NewKeyring()
	require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: &priv}))
	require.NoError(t, agent.ForwardToAgent(client, keyring))

	// Request forwarding in one session, read the socket path, then let that
	// session close (Output waits for the command and closes the channel).
	sess, err := client.NewSession()
	require.NoError(t, err)
	require.NoError(t, agent.RequestAgentForwarding(sess))
	out, err := sess.Output("printf %s \"$SSH_AUTH_SOCK\"")
	require.NoError(t, err)
	sock := string(out)
	require.NotEmpty(t, sock)

	// The session is gone; the connection is not. The socket must still work.
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	require.NoError(t, err, "socket must outlive the requesting session")
	defer conn.Close()
	keys, err := agent.NewClient(conn).List()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	wantPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	assert.Equal(t, wantPub.Marshal(), keys[0].Marshal())
}

// TestAgentForwardingDisabled verifies the server refuses forwarding and leaves
// SSH_AUTH_SOCK unset when AgentForwarding is off (the default).
func TestAgentForwardingDisabled(t *testing.T) {
	host, cs, cp := testKeys(t)
	client := dialAgent(t, Config{HostKey: host, Authorized: []ssh.PublicKey{cp}}, cs)

	req, err := client.NewSession()
	require.NoError(t, err)
	// The server replies false, so the request errors.
	assert.Error(t, agent.RequestAgentForwarding(req))
	_ = req.Close()

	check, err := client.NewSession()
	require.NoError(t, err)
	defer check.Close()
	out, err := check.Output("echo sock=[$SSH_AUTH_SOCK]")
	require.NoError(t, err)
	assert.Contains(t, string(out), "sock=[]", "SSH_AUTH_SOCK must be unset without forwarding")
}

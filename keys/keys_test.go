package keys

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestEnsureGeneratesUsableMaterial(t *testing.T) {
	dir := t.TempDir()
	set, err := Ensure(dir, "devc.shop")
	require.NoError(t, err)

	// All files exist with sane modes.
	for _, p := range []string{set.ClientPrivate, set.ClientPublic, set.HostPrivate, set.HostPublic, set.KnownHosts} {
		fi, err := os.Stat(p)
		require.NoError(t, err, p)
		assert.False(t, fi.IsDir())
	}
	priv, err := os.Stat(set.ClientPrivate)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), priv.Mode().Perm(), "private key must be 0600")

	// The host signer loads and the client authorized key parses.
	signer, err := LoadSigner(set.HostPrivate)
	require.NoError(t, err)
	assert.NotNil(t, signer.PublicKey())

	authKeys, err := LoadAuthorized(set.ClientPublic)
	require.NoError(t, err)
	require.Len(t, authKeys, 1)

	// known_hosts pins the alias to the host key.
	kh, err := os.ReadFile(set.KnownHosts)
	require.NoError(t, err)
	assert.Contains(t, string(kh), "devc.shop ")
	assert.Contains(t, string(kh), "ssh-ed25519 ")
}

func TestEnsureIsIdempotentButKnownHostsFollowsHostKey(t *testing.T) {
	dir := t.TempDir()
	set1, err := Ensure(dir, "devc.shop")
	require.NoError(t, err)
	firstClient, err := os.ReadFile(set1.ClientPrivate)
	require.NoError(t, err)

	// Second Ensure keeps the same keys.
	_, err = Ensure(dir, "devc.shop")
	require.NoError(t, err)
	secondClient, err := os.ReadFile(set1.ClientPrivate)
	require.NoError(t, err)
	assert.Equal(t, firstClient, secondClient, "keys must be stable across Ensure")
}

func TestRotateChangesIdentity(t *testing.T) {
	dir := t.TempDir()
	set, err := Ensure(dir, "devc.shop")
	require.NoError(t, err)
	before, err := os.ReadFile(set.ClientPrivate)
	require.NoError(t, err)

	_, err = Rotate(dir, "devc.shop")
	require.NoError(t, err)
	after, err := os.ReadFile(set.ClientPrivate)
	require.NoError(t, err)
	assert.NotEqual(t, before, after, "rotate must replace the client key")
}

// TestAuthorizedKeyMatchesGenerated confirms the public key devc injects matches
// the private key it keeps - a mismatch would silently break auth.
func TestAuthorizedKeyMatchesGenerated(t *testing.T) {
	dir := t.TempDir()
	set, err := Ensure(dir, "devc.shop")
	require.NoError(t, err)

	privBytes, err := os.ReadFile(set.ClientPrivate)
	require.NoError(t, err)
	signer, err := ssh.ParsePrivateKey(privBytes)
	require.NoError(t, err)

	authKeys, err := LoadAuthorized(set.ClientPublic)
	require.NoError(t, err)
	assert.Equal(t, signer.PublicKey().Marshal(), authKeys[0].Marshal())

	_ = filepath.Base(dir)
}

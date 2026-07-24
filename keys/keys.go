// Package keys manages the per-workspace SSH credentials devc uses to secure
// the agent transport. Everything is generated locally, once, and kept under
// the workspace state directory (0700):
//
//	id_ed25519 / id_ed25519.pub   the client key (VSCodium / ssh authenticate with it)
//	host_key   / host_key.pub      the agent's host key (copied into the container)
//	known_hosts                    pins host_key.pub to the workspace's ssh alias
//
// Only host_key ever leaves the host (the in-container agent needs it); both
// private halves are otherwise local. This is what lets StrictHostKeyChecking
// be honestly enabled and needs no sshd, no published port, and no user secret.
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// File names within the workspace key directory.
const (
	ClientPrivateName = "id_ed25519"
	ClientPublicName  = "id_ed25519.pub"
	HostPrivateName   = "host_key"
	HostPublicName    = "host_key.pub"
	KnownHostsName    = "known_hosts"
)

// Set is the resolved set of key file paths for a workspace.
type Set struct {
	Dir           string
	ClientPrivate string
	ClientPublic  string
	HostPrivate   string
	HostPublic    string
	KnownHosts    string
}

// pathsIn builds a Set of file paths rooted at dir (no I/O).
func pathsIn(dir string) *Set {
	return &Set{
		Dir:           dir,
		ClientPrivate: filepath.Join(dir, ClientPrivateName),
		ClientPublic:  filepath.Join(dir, ClientPublicName),
		HostPrivate:   filepath.Join(dir, HostPrivateName),
		HostPublic:    filepath.Join(dir, HostPublicName),
		KnownHosts:    filepath.Join(dir, KnownHostsName),
	}
}

// Ensure creates any missing key material in dir and (re)writes known_hosts for
// hostAlias. It is idempotent: existing keys are kept, so the SSH identity is
// stable across `devc up` runs. hostAlias is the ssh Host name (e.g. "devc.shop").
func Ensure(dir, hostAlias string) (*Set, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := pathsIn(dir)

	if !exists(s.ClientPrivate) {
		if err := generateKeyPair(s.ClientPrivate, s.ClientPublic); err != nil {
			return nil, fmt.Errorf("generate client key: %w", err)
		}
	}
	if !exists(s.HostPrivate) {
		if err := generateKeyPair(s.HostPrivate, s.HostPublic); err != nil {
			return nil, fmt.Errorf("generate host key: %w", err)
		}
	}

	// known_hosts is derived state; regenerate every time so a rotated host key
	// is reflected and the pin stays correct.
	if err := writeKnownHosts(s.KnownHosts, hostAlias, s.HostPublic); err != nil {
		return nil, fmt.Errorf("write known_hosts: %w", err)
	}
	return s, nil
}

// Rotate regenerates both key pairs (and known_hosts), invalidating the old
// identity. The container must be re-provisioned afterward.
func Rotate(dir, hostAlias string) (*Set, error) {
	s := pathsIn(dir)
	for _, p := range []string{s.ClientPrivate, s.ClientPublic, s.HostPrivate, s.HostPublic} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return Ensure(dir, hostAlias)
}

// AuthorizedKey returns the client public key in authorized_keys format (a
// single line), for injecting into the container as the agent's sole allowed key.
func (s *Set) AuthorizedKey() ([]byte, error) {
	return os.ReadFile(s.ClientPublic)
}

// generateKeyPair writes a fresh ed25519 key pair: the private half in OpenSSH
// PEM (0600) and the public half in authorized_keys line format (0644).
func generateKeyPair(privPath, pubPath string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "devc")
	if err != nil {
		return err
	}
	if err := os.WriteFile(privPath, pem.EncodeToMemory(block), 0o600); err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	if err := os.WriteFile(pubPath, ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil { //nolint:gosec // public key
		return err
	}
	return nil
}

// writeKnownHosts writes a single pinned host line "<alias> ssh-ed25519 AAAA...".
func writeKnownHosts(path, hostAlias, hostPubPath string) error {
	pubBytes, err := os.ReadFile(hostPubPath)
	if err != nil {
		return err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(pubBytes)
	if err != nil {
		return fmt.Errorf("parse host public key: %w", err)
	}
	line := fmt.Sprintf("%s %s\n", hostAlias, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))))
	return os.WriteFile(path, []byte(line), 0o644) //nolint:gosec // public host key pin
}

// LoadSigner parses an OpenSSH private key file into an ssh.Signer (used by the
// agent to present its host key).
func LoadSigner(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(b)
}

// LoadAuthorized parses an authorized_keys file into the set of accepted public
// keys, keyed by their marshaled form for constant-time-friendly comparison.
func LoadAuthorized(path string) ([]ssh.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []ssh.PublicKey
	rest := b
	for len(rest) > 0 {
		pub, _, _, remainder, err := ssh.ParseAuthorizedKey(rest)
		if err != nil {
			break
		}
		out = append(out, pub)
		rest = remainder
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no authorized keys in %s", path)
	}
	return out, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

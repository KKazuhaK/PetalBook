package gitstore

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Credential is what a command that talks to the remote runs with. Only the
// two types of this package implement it: a secret reaches git through the
// environment (a token) or a short-lived key file (SSH), never through a
// command line, a URL or the repository's configuration.
type Credential interface {
	// prepare returns the environment for one command, the secrets to scrub
	// from its output, and a cleanup to call when it has finished.
	prepare() (env, secrets []string, cleanup func(), err error)
}

// Token is an https access token. Get is called once per command, so the
// caller can keep the token encrypted until then.
type Token struct {
	Username string
	Get      func() (string, error)
	// URL is the remote's address. The token is offered only to that scheme and
	// host: if the remote redirects somewhere else and that place asks for a
	// password, it gets none.
	URL string
}

// CredentialScope is what a token is given for: "scheme://host[:port]" of an
// https address, or "" for any other.
func CredentialScope(raw string) string { return credentialScope(raw) }

// credentialScope is "scheme://host[:port]" of an https address, or "".
func credentialScope(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// tokenHelper is the whole of the credential helper: a fixed string that
// reads the user name and the token from the command's environment. The empty
// helper before it clears any helper configured elsewhere.
const tokenHelper = `!f() { test "$1" = get || exit 0; echo "username=$PETALBOOK_GIT_USER"; echo "password=$PETALBOOK_GIT_TOKEN"; }; f`

func (t Token) prepare() ([]string, []string, func(), error) {
	if t.Get == nil {
		return nil, nil, nil, errors.New("no token")
	}
	tok, err := t.Get()
	if err != nil {
		return nil, nil, nil, err
	}
	if tok == "" || strings.ContainsAny(tok, "\n\r\x00") {
		return nil, nil, nil, errors.New("the stored token is not usable")
	}
	user := t.Username
	if user == "" {
		user = "x-access-token"
	}
	key := "credential.helper"
	if scope := credentialScope(t.URL); scope != "" {
		key = "credential." + scope + ".helper"
	}
	env := []string{
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=" + key, "GIT_CONFIG_VALUE_1=" + tokenHelper,
		// A redirect is followed only for the first request of a conversation,
		// which is git's default; said here so no configuration can widen it.
		"GIT_CONFIG_KEY_2=http.followRedirects", "GIT_CONFIG_VALUE_2=initial",
		"PETALBOOK_GIT_USER=" + user, "PETALBOOK_GIT_TOKEN=" + tok,
	}
	return env, []string{tok}, func() {}, nil
}

// SSH authenticates with a private key. Key returns the OpenSSH-format
// private key; it is called for each command and the file it is written to
// exists only while that command runs.
type SSH struct {
	Key        func() ([]byte, error)
	KnownHosts string // file the trusted host keys are kept in
	Command    string // the ssh program; default "ssh"
}

// SSHCommand is the GIT_SSH_COMMAND for a key file and a known-hosts file.
// IdentitiesOnly keeps ssh from offering any other key, BatchMode from asking
// anything, accept-new trusts a host the first time and refuses a changed key,
// and -F /dev/null ignores the user's ssh configuration (which could run a
// program).
func SSHCommand(ssh, keyFile, knownHosts string) string {
	if ssh == "" {
		ssh = "ssh"
	}
	return strings.Join([]string{ssh,
		"-i", shellQuote(keyFile),
		"-F", os.DevNull,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + shellQuote(knownHosts),
		"-o", "GlobalKnownHostsFile=" + os.DevNull,
		"-o", "HashKnownHosts=no",
		"-o", "PreferredAuthentications=publickey",
		"-o", "ClearAllForwardings=yes",
		"-o", "ConnectTimeout=20",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
	}, " ")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (s SSH) prepare() ([]string, []string, func(), error) {
	if s.Key == nil || s.KnownHosts == "" {
		return nil, nil, nil, errors.New("no ssh key")
	}
	key, err := s.Key()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.KnownHosts), 0o700); err != nil {
		return nil, nil, nil, err
	}
	dir, err := os.MkdirTemp("", "petalbook-ssh-")
	if err != nil {
		return nil, nil, nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	file := filepath.Join(dir, "key")
	if err := os.WriteFile(file, key, 0o600); err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	env := []string{"GIT_SSH_COMMAND=" + SSHCommand(s.Command, file, s.KnownHosts), "GIT_SSH_VARIANT=ssh"}
	return env, []string{file, dir}, cleanup, nil
}

// SSHKey is a generated key pair.
type SSHKey struct {
	Private     []byte // OpenSSH private key, PEM
	Public      string // "ssh-ed25519 AAAA… comment", one line
	Fingerprint string // SHA256:…
}

// GenerateSSHKey makes an ed25519 key pair.
func GenerateSSHKey(comment string) (*SSHKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := marshalPrivate(priv, comment)
	if err != nil {
		return nil, err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	if comment != "" {
		line += " " + comment
	}
	return &SSHKey{Private: block, Public: line, Fingerprint: ssh.FingerprintSHA256(sp)}, nil
}

func marshalPrivate(priv ed25519.PrivateKey, comment string) ([]byte, error) {
	b, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, err
	}
	return pemEncode(b), nil
}

// Fingerprint returns the fingerprint of a public key line.
func Fingerprint(publicLine string) (string, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicLine))
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(pub), nil
}

// HostKeyFingerprint is the fingerprint of the host key trusted in a
// known-hosts file ("" when there is none yet).
func HostKeyFingerprint(knownHosts string) string {
	data, err := os.ReadFile(knownHosts)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, _, key, _, _, err := ssh.ParseKnownHosts([]byte(line)); err == nil && key != nil {
			return ssh.FingerprintSHA256(key)
		}
	}
	return ""
}

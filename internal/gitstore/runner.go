// Package gitstore is the Git side of a space that follows a remote
// repository: the restricted git runner, credentials for https and ssh,
// fetching, committing only given paths, pushing, rewritten-history
// detection, and page history. It depends on nothing else in the application,
// so it can become a shared module.
//
// Every git process in the application is started by execRunner in this file,
// after refuseDestructive (safegit.go) has looked at the command line.
package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Cmd is one git invocation: the arguments after "git" (the runner adds
// "-C Dir" and its fixed -c options), extra environment and stdin.
type Cmd struct {
	Dir   string
	Args  []string
	Env   []string // set by this package only (identity, credentials)
	Stdin []byte
	// Stdout, when set, receives standard output as it is produced instead of
	// it being buffered (history reads use this to look only at what they need).
	Stdout io.Writer
	// Secrets are scrubbed from the error text.
	Secrets []string
	// AllowUnsafe adds local paths, file:// and http:// to the allowed transports.
	// Tests only.
	AllowUnsafe bool
}

// Runner runs git. It is the only place that starts a git process; tests
// replace it with a recorder or a hook.
type Runner interface {
	Run(ctx context.Context, c Cmd) (stdout, stderr string, err error)
}

// RunReal runs a command with the real git (after the allow-list check). A
// Runner that wraps another one (a test hook) ends in this.
func RunReal(ctx context.Context, c Cmd) (string, string, error) { return execRunner{}.Run(ctx, c) }

// Path is where the git program is, for a caller that has to hand it to
// something else (a test server that serves repositories with it); the
// application itself starts git only through a Runner.
func Path() (string, error) { return exec.LookPath("git") }

// MaxOutput bounds what one git command may print into memory.
const MaxOutput = 64 << 20

type execRunner struct{}

// defaultRunner runs the real git. Tests may replace it.
var defaultRunner Runner = execRunner{}

// passEnv are the variables of the server's own environment that git (and
// ssh, and curl under it) may see. Everything else, in particular every GIT_*
// variable that could point git elsewhere, is dropped.
var passEnv = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "TMPDIR": true, "TMP": true, "TEMP": true, "TZ": true,
	"SYSTEMROOT": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "GIT_SSL_CAINFO": true, "GIT_SSL_CAPATH": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true, "all_proxy": true,
}

// protocols is GIT_ALLOW_PROTOCOL: nothing but https and ssh can be fetched
// from or pushed to, whatever a remote's URL or a submodule says.
const protocols = "https:ssh"

func baseEnv(unsafe bool) []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); passEnv[k] {
			env = append(env, kv)
		}
	}
	allow := protocols
	if unsafe {
		allow = "file:http:https:ssh"
	}
	return append(env,
		"LC_ALL=C", "LANG=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL="+allow,
		// The server's behaviour must not depend on whoever's configuration it finds.
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		// A path is a path. Without this git reads the paths it is given as
		// patterns: committing "[ab].md" would also take "a.md" and "b.md".
		"GIT_LITERAL_PATHSPECS=1",
		"GIT_OPTIONAL_LOCKS=0",
		// Git never goes to the network on its own: a partial clone's missing
		// objects are an error the server handles, not a silent request without
		// the server's credential or deadline (see FetchBlobs).
		"GIT_NO_LAZY_FETCH=1")
}

// commitBuilding are the commands that stage and assemble a commit.
var commitBuilding = map[string]bool{
	"add": true, "rm": true, "diff": true, "ls-files": true, "update-index": true,
	"read-tree": true, "write-tree": true, "commit-tree": true,
}

type limitWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		return 0, errors.New("git printed too much")
	}
	return w.buf.Write(p)
}

// commandLine is what follows "git" for these arguments run in dir: the
// runner's fixed options, then the caller's arguments.
func commandLine(dir string, args []string) []string {
	// Hooks never run (not even pre-push) and files are committed byte for
	// byte, whatever the platform default is.
	full := []string{"-C", dir, "-c", "core.quotepath=off", "-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.autocrlf=false", "-c", "safe.directory=*"}
	// While a commit is being built, names are taken as they are written. A
	// file system may ignore case; Git must not then, or a file renamed to
	// another spelling of its own name keeps the old one in the repository:
	// which name a file has is decided by Repo.CommitPaths, from the directory
	// listing. Everything else (checking out, fast-forwarding, status) runs
	// on the repository's own setting: there Git has to know that the disk
	// takes two spellings for one file, or a rename by case coming from the
	// remote looks like a file in the way.
	if sub, _ := subcommand(args); commitBuilding[sub] {
		full = append(full, "-c", "core.ignorecase=false")
	}
	return append(full, args...)
}

func (execRunner) Run(ctx context.Context, c Cmd) (string, string, error) {
	if err := refuseDestructive(c.Args); err != nil {
		return "", "", err
	}
	cmd := exec.CommandContext(ctx, "git", commandLine(c.Dir, c.Args)...)
	cmd.Env = append(baseEnv(c.AllowUnsafe), c.Env...)
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	setProcessGroup(cmd)
	cmd.WaitDelay = 3 * time.Second
	var out limitWriter
	out.max = MaxOutput
	var errb limitWriter
	errb.max = 1 << 20
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	} else {
		cmd.Stdout = &out
	}
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("%w: %v", ctx.Err(), err)
	}
	return out.buf.String(), scrub(errb.buf.String(), c.Secrets), err
}

// GitError is a failed git command. Its text never contains the credentials
// the command ran with.
type GitError struct {
	Sub    string
	Stderr string
	Err    error
}

func (e *GitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", e.Sub, firstLines(msg, 3))
}

func (e *GitError) Unwrap() error { return e.Err }

// ExitCode is git's exit status, or -1 when git did not run to completion.
func (e *GitError) ExitCode() int {
	var ee *exec.ExitError
	if errors.As(e.Err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func firstLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = append(l[:n], "...")
	}
	return strings.Join(l, "; ")
}

package gitstore

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// Kind says why a network command failed, in terms the caller acts on.
type Kind int

const (
	// KindOther is a failure that is none of the others.
	KindOther Kind = iota
	// KindOffline: the remote could not be reached (DNS, connection, timeout).
	KindOffline
	// KindAuth: the credential was refused, or the repository is not visible to it.
	KindAuth
	// KindHostKey: the SSH host key is not the one trusted before.
	KindHostKey
	// KindRejected: the remote refused a push (it moved, or a rule declined it).
	KindRejected
)

// NetError is a failed network command with a message that is safe to show.
type NetError struct {
	Kind Kind
	Msg  string // for people: no credentials, no key paths
	Err  error  // the underlying error, whose text is scrubbed too
}

func (e *NetError) Error() string { return e.Msg }
func (e *NetError) Unwrap() error { return e.Err }

// KindOf returns the kind of a network failure, KindOther for any other error.
func KindOf(err error) Kind {
	var ne *NetError
	if errors.As(err, &ne) {
		return ne.Kind
	}
	return KindOther
}

var userinfoRe = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]*@`)

// scrub removes what must never reach a log or a person: the secrets the
// command ran with, user information inside URLs, and the temporary key file.
func scrub(s string, secrets []string) string {
	for _, sec := range secrets {
		if len(sec) >= 3 {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return userinfoRe.ReplaceAllString(s, "${1}")
}

// Scrub is scrub for text built by callers from errors of this package.
func Scrub(s string) string { return scrub(s, nil) }

// classify turns the failure of a command that talks to the remote into a
// NetError. stderr is git's own text, already scrubbed.
func classify(sub string, err error) error {
	if err == nil {
		return nil
	}
	var ge *GitError
	text := err.Error()
	if errors.As(err, &ge) {
		text = ge.Stderr
	}
	low := strings.ToLower(text)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(low, strings.ToLower(s)) {
				return true
			}
		}
		return false
	}
	detail := ""
	if l := strings.TrimSpace(firstLines(text, 2)); l != "" {
		detail = " (" + clip(l, 200) + ")"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &NetError{KindOffline, "the remote did not answer in time", err}
	case errors.Is(err, context.Canceled):
		return &NetError{KindOffline, "the request was cancelled", err}
	case has("host key verification failed", "remote host identification has changed", "host key for"):
		return &NetError{KindHostKey, "the host key of the remote is not the one trusted before; if the change is expected, disconnect and connect again", err}
	case has("authentication failed", "permission denied", "could not read username", "could not read password",
		"invalid username or password", "returned error: 401", "returned error: 403", "access denied", "bad credentials",
		"repository not found", "support for password authentication was removed", "permission to", "publickey"):
		return &NetError{KindAuth, "the remote refused the credential, or the repository does not exist or is not visible to it", err}
	case has("[rejected]", "non-fast-forward", "fetch first", "failed to push some refs", "[remote rejected]", "protected branch", "denied to"):
		return &NetError{KindRejected, "the remote did not accept the push" + detail, err}
	case has("could not resolve host", "connection refused", "connection timed out", "network is unreachable", "no route to host",
		"operation timed out", "timed out", "could not read from remote repository", "unable to access", "ssl", "early eof",
		"rpc failed", "hung up unexpectedly", "connection reset", "unable to connect", "kex_exchange_identification", "broken pipe",
		"failed to connect", "couldn't connect", "name or service not known", "temporary failure in name resolution"):
		return &NetError{KindOffline, "could not reach the remote" + detail, err}
	}
	return &NetError{KindOther, "git " + sub + " failed" + detail, err}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// ErrNoBranch: the remote has no such branch (a new, empty repository).
var ErrNoBranch = errors.New("the remote has no such branch")

// Errors of the rewritten-history rules.
var (
	// ErrHistoryRewritten is matched by errors.Is for a *HistoryRewrittenError.
	ErrHistoryRewritten = errors.New("the remote history was rewritten")
	// ErrNotRemoteTip: the commit offered for acceptance is not the remote's tip.
	ErrNotRemoteTip = errors.New("that commit is not the current tip of the remote branch")
	// ErrNothingToAccept: the remote history was not rewritten.
	ErrNothingToAccept = errors.New("the remote history was not rewritten, there is nothing to accept")
)

// HistoryRewrittenError says writes are paused because origin/<branch> is no
// longer a descendant of the last accepted commit.
type HistoryRewrittenError struct{ LastGood, Remote string }

func (e *HistoryRewrittenError) Error() string {
	return "the remote history was rewritten: the remote branch is at " + short(e.Remote) +
		", which does not contain the last accepted commit " + short(e.LastGood) + "; changes are paused until the owner accepts the new history"
}

func (e *HistoryRewrittenError) Is(target error) bool { return target == ErrHistoryRewritten }

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

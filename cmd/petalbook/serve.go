package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/drafts"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/oauth"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/tokens"
	"github.com/KKazuhaK/PetalBook/internal/webui"
)

type serveConfig struct {
	// space selects single-user mode, data accounts mode; exactly one is set.
	space, data                                string
	listen, name, stateDir, committer          string
	database                                   string
	publicURL                                  string
	trustedProxies                             []string
	oidcIssuer, oidcClientID, oidcName         string
	oidcClientSecretFile, oidcClientSecret     string
	oidcScopes                                 []string
	oidcAutoCreate                             bool
	commitIdle, commitMax, draftIdle, draftMax time.Duration
	// maxSpaces is how many spaces one user may own (0: no limit); spaceIdle
	// and maxOpenSpaces decide when spaces that are not in use are closed.
	maxSpaces, maxOpenSpaces int
	// maxUpload is the largest file accepted as an upload, in bytes; inbox is
	// the folder quick notes go to in single-user mode.
	maxUpload int64
	// maxImport is the largest body of an import, in bytes.
	maxImport int64
	inbox     string
	// noMCP switches /mcp off in single-user mode (--mcp=false); mcpSet says
	// the flag was given.
	noMCP, mcpSet bool
	spaceIdle     time.Duration
	// pullInterval is how often a space that follows a remote is fetched (0:
	// never by itself), remoteHosts the hosts a remote may be on, remoteTimeout
	// how long one command that talks to a remote may take. remoteSet says one
	// of them was given. remoteUnsafe lets tests use local paths as remotes.
	pullInterval, remoteTimeout time.Duration
	remoteHosts                 []string
	remoteSet, remoteUnsafe     bool
	// draftTick is how often due drafts are looked for (tests shorten it).
	draftTick         time.Duration
	nameSet, insecure bool
	allowOrigins      []string
	logger            *slog.Logger
	// onReady is told the address once the server is accepting requests.
	onReady func(addr net.Addr)
	// oidcHTTPClient reaches the identity provider (tests).
	oidcHTTPClient *http.Client
}

// passwordParams are the password hashing parameters; zero means the default.
// Tests lower them: argon2 at its real cost is slow under the race detector.
var passwordParams auth.PasswordParams

func newServe() *cobra.Command {
	var c serveConfig
	var mcpOn bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve spaces over HTTP",
		Long: `Serve pages over the HTTP API, in one of two modes. Give exactly one of
--space and --data.

--space DIR is single-user mode: the directory is one space, nobody signs in,
and the server listens on a loopback address only unless --insecure-no-auth
says that is acceptable.

--data DIR is accounts mode: people sign in with a password, a passkey or a
provider (OpenID Connect, SAML or GitHub) that the administrators set up on the
安全 page, each has a personal space and may make more, and the server
may listen on any address. Spaces live in DIR/spaces/<id>, their indexes in
DIR/cache/<id>, and the state database (users, sessions, drafts) in
DIR/petalbook.db unless --database says otherwise. The first run prints a
set-up link in the log; open it to create the administrator. If nobody can
sign in, "petalbook user add", "petalbook user passwd" and "petalbook security
reset" work on the data directory directly. The --oidc-* flags of earlier
versions are read once, at the first start, and become the provider "sso" of the
state database; from then on the database counts and the flags only warn.

Either way a space is a Git repository. Typing is kept as a draft in the state
database; a draft becomes the file after --draft-idle without changes (or
--draft-max after the first), and the file is committed a few seconds later by
a background committer, and when the server stops.

In accounts mode the owner of a space may make it follow a remote repository
(https with an access token, or ssh with a deploy key the server makes). Then
the remote branch is the truth: every change is committed and pushed before the
request is answered, and the server fetches the remote every --pull-interval and
moves the space forward to it. It never forces a push and never rewrites
anything on the remote; if the remote's history was rewritten the space pauses
until its owner accepts the new history. Remotes may only be on the hosts given
with --remote-host. Their credentials are stored encrypted with the key in
DATA/secret.key (made on first use) or in PETALBOOK_SECRET_KEY, and are passed to
git through its environment, never on a command line or in a log.

--database is a SQLite file path or a postgres:// URL; as it may contain a
password, prefer PETALBOOK_DATABASE and note that the URL is never logged.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c.nameSet = cmd.Flags().Changed("name")
			c.mcpSet = cmd.Flags().Changed("mcp")
			c.noMCP = !mcpOn
			c.remoteSet = cmd.Flags().Changed("pull-interval") || cmd.Flags().Changed("remote-host") || cmd.Flags().Changed("remote-timeout")
			c.oidcClientSecret = os.Getenv("PETALBOOK_OIDC_CLIENT_SECRET")
			c.logger = slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runServe(ctx, c)
		},
	}
	f := cmd.Flags()
	f.StringVar(&c.space, "space", "", "single-user mode: directory of the space (created if missing)")
	f.StringVar(&c.data, "data", "", "accounts mode: data directory (created if missing)")
	f.StringVar(&c.listen, "listen", "127.0.0.1:8080", "address to listen on")
	f.StringVar(&c.database, "database", "", "state database: a SQLite file path or a postgres:// URL (default: DATA/petalbook.db, or state.db in the state directory)")
	f.StringVar(&c.publicURL, "public-url", "", "accounts mode: the address people use, e.g. https://notes.example.com (decides whether cookies are Secure, and is accepted as the server's own origin; required with OIDC)")
	f.StringSliceVar(&c.trustedProxies, "trusted-proxy", nil, "address or CIDR of a reverse proxy whose X-Forwarded-For is believed when limiting sign-in attempts (repeatable, or comma-separated)")
	f.StringVar(&c.oidcIssuer, "oidc-issuer", "", "OpenID Connect issuer URL (https); imported once as the provider \"sso\" of the state database, which counts from then on")
	f.StringVar(&c.oidcClientID, "oidc-client-id", "", "OpenID Connect client id")
	f.StringVar(&c.oidcClientSecretFile, "oidc-client-secret-file", "", "file holding the OpenID Connect client secret (or set PETALBOOK_OIDC_CLIENT_SECRET)")
	f.StringVar(&c.oidcName, "oidc-name", "", "name of the provider on the sign-in page (default \"Single sign-on\")")
	f.StringSliceVar(&c.oidcScopes, "oidc-scopes", nil, "scopes to ask for besides openid (default: profile, email)")
	f.BoolVar(&c.oidcAutoCreate, "oidc-auto-create", false, "make a member account for an identity nobody has, at its first sign-in (otherwise only invited or existing accounts can sign in)")
	f.StringVar(&c.name, "name", space.DefaultName, "single-user mode: display name; used when the space is created, otherwise space.yml decides unless this is given")
	f.StringVar(&c.stateDir, "state-dir", "", "single-user mode: where the index and state database are kept (default: <space>/.git/petalbook-state)")
	f.StringVar(&c.committer, "committer", space.DefaultCommitter, "Git committer of automatic commits, \"Name <email>\" (the author is the space's owner in accounts mode)")
	f.DurationVar(&c.commitIdle, "commit-idle", space.DefaultCommitIdle, "commit this long after the last file write")
	f.DurationVar(&c.commitMax, "commit-max", space.DefaultCommitMax, "commit at most this long after the first uncommitted write")
	f.DurationVar(&c.draftIdle, "draft-idle", drafts.DefaultIdle, "write a draft to its file after this long without changes")
	f.DurationVar(&c.draftMax, "draft-max", drafts.DefaultMax, "write a draft to its file at most this long after it was first stored")
	f.DurationVar(&c.pullInterval, "pull-interval", spaces.DefaultPullInterval, "accounts mode: fetch the remote of every open space that follows one this often, jittered (0: never by itself; a space is also fetched when it is opened)")
	f.StringSliceVar(&c.remoteHosts, "remote-host", gitstore.DefaultHosts, "accounts mode: a host a space's remote repository may be on (repeatable, or comma-separated; * allows any host); giving it replaces the default list")
	f.DurationVar(&c.remoteTimeout, "remote-timeout", 60*time.Second, "accounts mode: how long one fetch or push may take before the remote counts as unreachable")
	f.Int64Var(&c.maxUpload, "max-upload", api.DefaultMaxUpload, "largest file accepted as an upload, in bytes (0: the default, 10 MiB)")
	f.Int64Var(&c.maxImport, "max-import", api.DefaultMaxImport, "largest Markdown file or zip accepted as an import, in bytes (0: the default, 100 MiB)")
	f.BoolVar(&mcpOn, "mcp", true, "single-user mode: serve /mcp for MCP clients with an API token (false: it answers 404); in accounts mode an administrator decides, in the app")
	f.StringVar(&c.inbox, "inbox", store.DefaultInbox, "single-user mode: the folder quick notes go to")
	f.IntVar(&c.maxSpaces, "max-spaces", spaces.DefaultMaxSpaces, "accounts mode: how many spaces one user may own (0: no limit)")
	f.DurationVar(&c.spaceIdle, "space-idle", spaces.DefaultIdleAfter, "accounts mode: close a space that has not been used for this long (it reopens on the next request)")
	f.IntVar(&c.maxOpenSpaces, "max-open-spaces", spaces.DefaultMaxOpen, "accounts mode: at most this many spaces open at once; the least recently used are closed first")
	f.BoolVar(&c.insecure, "insecure-no-auth", false, "single-user mode: allow a non-loopback --listen address (anyone who can reach it can read and edit everything). The checks against requests from other web sites stay on, and without loopback-only mode there is no implicit own origin: a browser needs --allow-origin for the address it uses")
	f.StringSliceVar(&c.allowOrigins, "allow-origin", nil, "an origin besides the server's own that may call the API, e.g. http://localhost:5173 for a dev server that proxies here (repeatable, or comma-separated)")
	return cmd
}

// checkListen refuses to expose an unauthenticated server beyond this machine.
func checkListen(addr net.Addr, insecure bool) error {
	if isLoopback(addr.String()) {
		return nil
	}
	if !insecure {
		return fmt.Errorf("refusing to listen on %s without authentication: listen on 127.0.0.1, or pass --insecure-no-auth (PETALBOOK_INSECURE_NO_AUTH=true) to accept that anyone who can reach it can read and edit everything", addr)
	}
	return nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host, _, _ = strings.Cut(host, "%") // IPv6 zone, e.g. ::1%lo0
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checked is the configuration after validation.
type checked struct {
	origins   []string
	publicURL string // normalised, no trailing slash; "" if none
	proxies   []netip.Prefix
	oidc      *auth.OIDCConfig
	accounts  bool
}

// check validates the configuration without touching the disk or the network,
// so a mistake fails before anything is created.
func (c serveConfig) check() (*checked, error) {
	var out checked
	if (c.space == "") == (c.data == "") {
		return nil, errors.New("give exactly one of --space (single-user mode) and --data (accounts mode)")
	}
	out.accounts = c.data != ""
	if strings.ContainsAny(c.data, "?#") {
		return nil, errors.New("--data: '?' and '#' in the path are not supported")
	}
	var err error
	if out.origins, err = parseOrigins(c.allowOrigins); err != nil {
		return nil, err
	}
	if c.maxSpaces < 0 || c.maxOpenSpaces < 0 || c.spaceIdle < 0 {
		return nil, errors.New("--max-spaces, --max-open-spaces and --space-idle must not be negative")
	}
	// An upload is held in memory while it is stored, so the limit has a limit.
	// An import is read into memory too.
	if c.maxImport < 0 || c.maxImport > 512<<20 {
		return nil, errors.New("--max-import must be between 0 and 536870912 (512 MiB)")
	}
	if c.maxUpload < 0 || c.maxUpload > 256<<20 {
		return nil, errors.New("--max-upload must be between 0 and 268435456 (256 MiB)")
	}
	if c.inbox != "" {
		if _, err := spaces.ValidInbox(c.inbox); err != nil {
			return nil, fmt.Errorf("--inbox: %s", strings.TrimPrefix(err.Error(), spaces.ErrInvalid.Error()+": "))
		}
	}
	if c.pullInterval < 0 || c.remoteTimeout < 0 {
		return nil, errors.New("--pull-interval and --remote-timeout must not be negative")
	}
	for _, h := range c.remoteHosts {
		if h = strings.TrimSpace(h); h == "" || strings.ContainsAny(h, "/@ \t") || strings.Contains(h, "://") {
			return nil, fmt.Errorf("--remote-host %q must be a host name such as github.com, or *", h)
		}
	}
	if c.draftIdle < 0 || c.draftMax < 0 || (c.draftIdle > 0 && c.draftMax > 0 && c.draftMax < c.draftIdle) {
		return nil, errors.New("--draft-max must not be shorter than --draft-idle")
	}
	if out.accounts {
		if c.insecure {
			return nil, errors.New("--insecure-no-auth is for single-user mode (--space); accounts mode has sign-in")
		}
		if c.stateDir != "" || c.nameSet {
			return nil, errors.New("--state-dir and --name are for single-user mode (--space); in accounts mode the data directory holds everything")
		}
		if c.mcpSet {
			return nil, errors.New("--mcp is for single-user mode (--space); in accounts mode an administrator switches MCP in the app")
		}
	} else {
		if c.remoteSet {
			return nil, errors.New("--pull-interval, --remote-host and --remote-timeout are for accounts mode (--data): a single-user space stays local")
		}
		if c.publicURL != "" || len(c.trustedProxies) > 0 || c.oidcIssuer != "" || c.oidcClientID != "" || c.oidcClientSecretFile != "" || c.oidcClientSecret != "" || c.oidcName != "" || len(c.oidcScopes) > 0 || c.oidcAutoCreate {
			return nil, errors.New("--public-url, --trusted-proxy and --oidc-* are for accounts mode (--data)")
		}
		return &out, nil
	}
	if c.publicURL != "" {
		if out.publicURL, err = parsePublicURL(c.publicURL); err != nil {
			return nil, err
		}
	}
	if out.proxies, err = auth.ParseTrustedProxies(c.trustedProxies); err != nil {
		return nil, fmt.Errorf("--trusted-proxy: %w", err)
	}
	if c.oidcIssuer != "" || c.oidcClientID != "" || c.oidcClientSecretFile != "" || c.oidcClientSecret != "" || c.oidcName != "" || len(c.oidcScopes) > 0 || c.oidcAutoCreate {
		if c.oidcIssuer == "" || c.oidcClientID == "" {
			return nil, errors.New("single sign-on needs --oidc-issuer and --oidc-client-id")
		}
		if out.publicURL == "" {
			return nil, errors.New("single sign-on needs --public-url: the provider sends people back to <public-url>/api/auth/oidc/callback")
		}
		secret := c.oidcClientSecret
		if c.oidcClientSecretFile != "" {
			if secret != "" {
				return nil, errors.New("give the client secret in --oidc-client-secret-file or PETALBOOK_OIDC_CLIENT_SECRET, not both")
			}
			raw, err := os.ReadFile(c.oidcClientSecretFile)
			if err != nil {
				return nil, fmt.Errorf("--oidc-client-secret-file: %w", err)
			}
			secret = strings.TrimRight(string(raw), "\r\n")
		}
		out.oidc = &auth.OIDCConfig{Issuer: c.oidcIssuer, ClientID: c.oidcClientID, ClientSecret: secret, Name: c.oidcName, Scopes: c.oidcScopes, AutoCreate: c.oidcAutoCreate}
		if err := auth.ValidateOIDC(*out.oidc, out.publicURL); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// parsePublicURL accepts scheme://host[:port] and returns it without a trailing slash.
func parsePublicURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("--public-url %q must look like https://notes.example.com (scheme, host and port only)", raw)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

// runServe runs until ctx is cancelled, then stops accepting requests, waits
// for those in flight, stops the draft sweeper, and flushes the committers so
// nothing is left uncommitted. Drafts that are not yet applied stay in the
// database.
func runServe(ctx context.Context, c serveConfig) error {
	log := c.logger
	if log == nil {
		log = slog.Default()
	}
	cfg, err := c.check()
	if err != nil {
		return err
	}
	// Listen first: a bad address or a refused exposure should fail before
	// the (possibly long) index build.
	ln, err := net.Listen("tcp", c.listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	loopback := isLoopback(ln.Addr().String())
	if cfg.accounts {
		if !loopback && cfg.publicURL == "" && len(cfg.origins) == 0 {
			log.Warn("no --public-url or --allow-origin is set: a browser cannot use this server, because nothing is accepted as its own origin")
		}
		if !loopback && (cfg.publicURL == "" || strings.HasPrefix(cfg.publicURL, "http://")) {
			log.Warn("sessions are not protected by https: put the server behind a TLS proxy and set --public-url to its https address")
		}
	} else {
		if err := checkListen(ln.Addr(), c.insecure); err != nil {
			return err
		}
		if !loopback {
			log.Warn("listening on a non-loopback address without authentication", "addr", ln.Addr().String())
			if w := noOriginWarning(cfg.origins); w != "" {
				log.Warn(w)
			}
		}
	}

	// The state database and the spaces. Each of these, once opened, is
	// closed on every path out of here.
	var (
		st       *store.Store
		mgr      *spaces.Manager
		sp       *space.Space
		svc      *auth.Service
		spClosed bool
	)
	defer func() {
		if sp != nil && !spClosed {
			sp.Close()
		}
		if mgr != nil && cfg.accounts {
			mgr.Close() // idempotent: the normal path has closed it already
		}
		if st != nil {
			st.Close()
		}
	}()
	if cfg.accounts {
		if err := os.MkdirAll(c.data, 0o755); err != nil {
			return err
		}
		target := c.database
		if target == "" {
			target = filepath.Join(c.data, "petalbook.db")
		}
		if st, err = store.Open(ctx, target); err != nil {
			return err
		}
		if mgr, err = spaces.NewAccounts(spaces.Options{Store: st, DataDir: c.data, Committer: c.committer,
			CommitIdle: c.commitIdle, CommitMax: c.commitMax, Logger: log,
			MaxSpaces: c.maxSpaces, IdleAfter: c.spaceIdle, MaxOpen: c.maxOpenSpaces,
			PullInterval: c.pullInterval, NetTimeout: c.remoteTimeout,
			RemotePolicy: gitstore.Policy{Hosts: c.remoteHosts, AllowUnsafe: c.remoteUnsafe}}); err != nil {
			return err
		}
		if svc, err = auth.New(ctx, auth.Config{Store: st, Spaces: mgr, PublicURL: cfg.publicURL, TrustedProxies: cfg.proxies,
			OIDC: cfg.oidc, HTTPClient: c.oidcHTTPClient, Password: passwordParams, Logger: log}); err != nil {
			return err
		}
	} else {
		if sp, err = space.Open(space.Options{
			Root: c.space, Name: c.nameIfSet(), StateDir: c.stateDir, Committer: c.committer,
			CommitIdle: c.commitIdle, CommitMax: c.commitMax, Logger: log,
		}); err != nil {
			return err
		}
		target := c.database
		if target == "" {
			target = filepath.Join(sp.StateDir(), "state.db")
		}
		if st, err = store.Open(ctx, target); err != nil {
			return err
		}
		if mgr, err = spaces.NewSingle(ctx, st, sp, log); err != nil {
			return err
		}
		for _, f := range sp.EarlierStateDBs() {
			importEarlierDrafts(ctx, st, f, c.draftIdle, c.draftMax, log)
		}
		if c.inbox != "" {
			if err := mgr.SetSingleInbox(c.inbox); err != nil {
				return err
			}
		}
	}
	drf, err := drafts.New(ctx, st, mgr, drafts.Config{Idle: c.draftIdle, Max: c.draftMax, Tick: c.draftTick}, log)
	if err != nil {
		return err
	}

	tokenSvc := api.NewTokens(tokens.Config{Store: st, Logger: log}, svc)
	// The authorization server for MCP clients needs accounts and the address
	// people use: it is the issuer of its tokens.
	var oauthSvc *oauth.Service
	if cfg.accounts && cfg.publicURL != "" {
		if oauthSvc, err = oauth.New(ctx, oauth.Config{Store: st, Secrets: mgr.Secrets(), Users: svc, Machine: tokenSvc, Issuer: cfg.publicURL,
			ClientIP: svc.ClientAddr, Logger: log}); err != nil {
			return fmt.Errorf("the authorization server: %w", err)
		}
	}
	handler := api.New(api.Options{Store: st, Spaces: mgr, Drafts: drf, Auth: svc, Tokens: tokenSvc, OAuth: oauthSvc, PublicURL: cfg.publicURL, NoMCP: c.noMCP, Version: version, MaxUpload: c.maxUpload, MaxImport: c.maxImport,
		RestrictHost: loopback, AllowOrigins: cfg.origins, Logger: log})
	var webOpts []webui.Option
	if svc != nil {
		// The page's policy lets the configured captcha provider's widget load.
		webOpts = append(webOpts, webui.WithCaptcha(func(r *http.Request) string { return svc.CaptchaProvider(r.Context()) }))
	}
	webApp, webBuilt := webui.Handler(webOpts...)
	if !webBuilt {
		log.Warn("this binary has no web app built in (build it with make release); only the API is served")
	}
	srv := &http.Server{
		Handler:           withWebApp(handler, webApp),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute, // a reindex of a large space is slow
		IdleTimeout:       2 * time.Minute,
	}
	if cfg.accounts {
		log.Info("petalbook serving accounts", "version", version, "data", c.data, "listen", ln.Addr().String(), "database", string(st.Dialect()),
			"sign-in", signInMethods(cfg), "browser origins", browserOrigins(loopback, append(append([]string(nil), cfg.origins...), originOf(cfg.publicURL)...)))
		if tok := svc.SetupToken(); tok != "" {
			base := cfg.publicURL
			if base == "" {
				base = "http://" + ln.Addr().String()
			}
			log.Info("no account exists yet: open this link to create the administrator", "link", base+"/setup#"+tok)
		}
	} else {
		log.Info("petalbook serving", "version", version, "space", sp.Root(), "name", sp.Name(), "listen", ln.Addr().String(),
			"browser origins", browserOrigins(loopback, cfg.origins))
	}

	// Background work stops with this context, before the spaces are closed.
	bg, stopBg := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	run := func(f func(context.Context)) {
		wg.Add(1)
		go func() { defer wg.Done(); f(bg) }()
	}
	run(drf.Run)
	run(tokenSvc.Run)
	if oauthSvc != nil {
		run(oauthSvc.Run)
	}
	if cfg.accounts {
		run(mgr.Run)
		run(mgr.RunPull)
	}
	if svc != nil {
		run(svc.Run)
		run(svc.WarmProviders)
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	if c.onReady != nil {
		c.onReady(ln.Addr())
	}

	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		serveErr = srv.Shutdown(shutCtx)
		cancel()
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	stopBg()
	wg.Wait()
	// Requests are done; commit what is pending before exiting.
	closeErr := func(what string, err error) {
		if err != nil {
			log.Error("closing "+what, "error", err)
			if serveErr == nil {
				serveErr = err
			}
		}
	}
	if mgr != nil && cfg.accounts {
		closeErr("the spaces", mgr.Close())
	}
	if sp != nil {
		spClosed = true
		closeErr("the space", sp.Close())
	}
	return serveErr
}

// withWebApp sends /api/ and the endpoints of the authorization server to the API and everything else to the web app. The
// prefix test, not a ServeMux, so the API sees the request path exactly as it
// did before the app was served from here. The page and the API share one
// origin, so the API's own-origin rules (the public URL in accounts mode, the
// loopback address on the listen port in single-user mode) already let the
// page call it.
func withWebApp(apiHandler, app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if api.HandlesPath(r.URL.Path) {
			apiHandler.ServeHTTP(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})
}

func signInMethods(cfg *checked) string {
	if cfg.oidc != nil {
		return "password, passkeys, providers of the state database (--oidc-* is imported once: " + cfg.oidc.Issuer + ")"
	}
	return "password, passkeys, providers of the state database"
}

func originOf(u string) []string {
	if o := api.OriginOf(u); o != "" {
		return []string{o}
	}
	return nil
}

// nameIfSet returns the name only when the user gave one, so a name already
// in space.yml is not overridden by the flag's default.
func (c serveConfig) nameIfSet() string {
	if c.nameSet {
		return c.name
	}
	return ""
}

// parseOrigins checks --allow-origin values: scheme://host[:port], nothing else.
func parseOrigins(in []string) ([]string, error) {
	var out []string
	for _, o := range in {
		o = api.NormalizeOrigin(o)
		if o == "" {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
			return nil, fmt.Errorf("--allow-origin %q must look like http://localhost:5173 (scheme, host and port only)", o)
		}
		out = append(out, o)
	}
	return out, nil
}

// browserOrigins describes, for the start-up line, which browser origins the
// server will accept.
func browserOrigins(loopback bool, allowed []string) string {
	own := "loopback addresses on the listen port"
	if !loopback {
		own = "none implicit (--insecure-no-auth)"
	}
	if len(allowed) == 0 {
		return own
	}
	return own + ", plus " + strings.Join(allowed, ", ")
}

// noOriginWarning is the start-up warning for --insecure-no-auth without any
// --allow-origin, which leaves browsers locked out; "" when there is nothing to warn about.
func noOriginWarning(allowed []string) string {
	if len(allowed) > 0 {
		return ""
	}
	return "no --allow-origin is set: a browser cannot use this server, because with --insecure-no-auth there is no implicit own origin; add --allow-origin http://<address you open in the browser>"
}

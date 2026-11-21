package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/KazuhaHub/PetalBook/internal/api"
	"github.com/KazuhaHub/PetalBook/internal/space"
)

type serveConfig struct {
	space, listen, name, stateDir, committer string
	commitIdle, commitMax                    time.Duration
	nameSet, insecure                        bool
	allowOrigins                             []string
	logger                                   *slog.Logger
	// onReady is told the address once the server is accepting requests.
	onReady func(addr net.Addr)
}

func newServe() *cobra.Command {
	var c serveConfig
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve a space over HTTP",
		Long: `Serve the pages in a directory over the HTTP API.

The directory is turned into a space on first use: it becomes a Git repository
if it is not one, and gets .petalbook/space.yml. Saving writes the file at once;
a background committer makes the Git commit after a quiet period, and when the
server stops. Nothing is ever pushed.

There is no sign-in in this version, so the server listens on a loopback
address only, unless --insecure-no-auth says that is acceptable.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c.nameSet = cmd.Flags().Changed("name")
			c.logger = slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runServe(ctx, c)
		},
	}
	f := cmd.Flags()
	f.StringVar(&c.space, "space", "", "directory of the space (created if missing) (required)")
	f.StringVar(&c.listen, "listen", "127.0.0.1:8080", "address to listen on")
	f.StringVar(&c.name, "name", space.DefaultName, "display name; used when the space is created, otherwise space.yml decides unless this is given")
	f.StringVar(&c.stateDir, "state-dir", "", "where the index is kept (default: <space>/.petalbook/cache)")
	f.StringVar(&c.committer, "committer", space.DefaultCommitter, "Git author of automatic commits, \"Name <email>\"")
	f.DurationVar(&c.commitIdle, "commit-idle", space.DefaultCommitIdle, "commit after this long without writes")
	f.DurationVar(&c.commitMax, "commit-max", space.DefaultCommitMax, "commit at most this long after the first uncommitted write")
	f.BoolVar(&c.insecure, "insecure-no-auth", false, "allow a non-loopback --listen address (anyone who can reach it can read and edit everything). The checks against requests from other web sites stay on, and without loopback-only mode there is no implicit own origin: a browser needs --allow-origin for the address it uses")
	f.StringSliceVar(&c.allowOrigins, "allow-origin", nil, "an origin besides the server's own that may call the API, e.g. http://localhost:5173 for a dev server that proxies here (repeatable, or comma-separated)")
	cmd.MarkFlagRequired("space")
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

// runServe runs until ctx is cancelled, then stops accepting requests, waits
// for those in flight, and flushes the committer so nothing is left uncommitted.
func runServe(ctx context.Context, c serveConfig) error {
	log := c.logger
	if log == nil {
		log = slog.Default()
	}
	origins, err := parseOrigins(c.allowOrigins)
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
	if err := checkListen(ln.Addr(), c.insecure); err != nil {
		return err
	}
	loopback := isLoopback(ln.Addr().String())
	if !loopback {
		log.Warn("listening on a non-loopback address without authentication", "addr", ln.Addr().String())
		if w := noOriginWarning(origins); w != "" {
			log.Warn(w)
		}
	}

	sp, err := space.Open(space.Options{
		Root: c.space, Name: c.nameIfSet(), StateDir: c.stateDir, Committer: c.committer,
		CommitIdle: c.commitIdle, CommitMax: c.commitMax, Logger: log,
	})
	if err != nil {
		return err
	}
	handler := api.New(api.Options{Space: sp, Version: version, RestrictHost: loopback, AllowOrigins: origins, Logger: log})
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute, // a reindex of a large space is slow
		IdleTimeout:       2 * time.Minute,
	}
	log.Info("petalbook serving", "version", version, "space", sp.Root(), "name", sp.Name(), "listen", ln.Addr().String(),
		"browser origins", browserOrigins(loopback, origins))

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
	// Requests are done; commit what is pending before exiting.
	if err := sp.Close(); err != nil {
		log.Error("closing the space", "error", err)
		if serveErr == nil {
			serveErr = err
		}
	}
	return serveErr
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

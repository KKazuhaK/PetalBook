package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/tokens"
)

func newToken() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Make, list and revoke API tokens",
		Long: `API tokens let a script or an MCP client act for one person in a few of their
spaces. In single-user mode there are no accounts and no token pages, so a
token is made here, for the built-in user and the one space. The commands read
and write the state database directly and work while the server runs; a token
revoked here is refused by the running server within 30 seconds.

For a server with accounts, tokens are made in the app; here an administrator
who cannot sign in may list and revoke one person's tokens with --data and --user.`,
	}
	cmd.AddCommand(newTokenCreate(), newTokenList(), newTokenRevoke())
	return cmd
}

type tokenFlags struct {
	draftFlags
	user string
}

func (f *tokenFlags) bind(cmd *cobra.Command) {
	f.draftFlags.bind(cmd)
	cmd.Flags().StringVar(&f.user, "user", "", "accounts mode: whose tokens (a username)")
}

// owner opens the state database and says whose tokens these are: the built-in
// user in single-user mode, the named user with --data.
func (f *tokenFlags) owner(ctx context.Context) (*store.Store, *store.User, error) {
	st, err := f.open(ctx)
	if err != nil {
		return nil, nil, err
	}
	var u *store.User
	if f.data != "" {
		if f.user == "" {
			st.Close()
			return nil, nil, errors.New("with --data, say whose tokens with --user USERNAME")
		}
		name, nerr := auth.NormalizeUsername(f.user)
		if nerr == nil {
			u, err = store.GetUserByUsername(ctx, st.Q(), name)
		} else {
			err = store.ErrNotFound
		}
		if errors.Is(err, store.ErrNotFound) {
			st.Close()
			return nil, nil, fmt.Errorf("no user named %q", f.user)
		}
	} else {
		if f.user != "" {
			st.Close()
			return nil, nil, errors.New("--user is for --data; single-user mode has one user")
		}
		u, err = store.GetUser(ctx, st.Q(), store.AllTenants(), store.LocalUserID)
		if errors.Is(err, store.ErrNotFound) {
			st.Close()
			return nil, nil, errors.New("this space has never been served by this version of PetalBook; start the server once first")
		}
	}
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, u, nil
}

// parseExpiry reads --expires: "90d" (1 to 365 days) or "never".
func parseExpiry(s string) (time.Duration, string, error) {
	if s == "never" {
		return 0, "never", nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
	if err != nil || !strings.HasSuffix(s, "d") || n < 1 || n > tokens.MaxDays {
		return 0, "", fmt.Errorf("--expires must look like 90d (1 to %d days) or be never", tokens.MaxDays)
	}
	return time.Duration(n) * 24 * time.Hour, "", nil
}

func newTokenCreate() *cobra.Command {
	var f tokenFlags
	var name, access, expires string
	cmd := &cobra.Command{
		Use:   "create --space DIR --name NAME [--access read|write] [--expires 90d|never]",
		Short: "Make a token for single-user mode",
		Long: `Makes a token for the built-in user and the space "default". The secret is
printed alone on one line on standard output; what it is goes to standard error.
It cannot be shown again.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.data != "" {
				return errors.New("tokens of a server with accounts are made in the app; this command is for --space")
			}
			ttl, _, err := parseExpiry(expires)
			if err != nil {
				return err
			}
			st, u, err := f.owner(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			svc := tokens.New(tokens.Config{Store: st})
			t, secret, err := svc.Create(cmd.Context(), tokens.CreateParams{UserID: u.ID, Name: name, Access: access,
				Spaces: []string{spaces.SingleID}, ExpiresIn: ttl})
			if err != nil {
				var inv *tokens.Invalid
				switch {
				case errors.As(err, &inv):
					return errors.New(inv.Msg)
				case errors.Is(err, tokens.ErrLimit):
					return fmt.Errorf("there are already %d tokens in use; revoke one first", tokens.MaxLive)
				}
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), secret)
			exp := "never"
			if !t.Expires.IsZero() {
				exp = t.Expires.Format("2006-01-02")
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "token %s %q (%s, expires %s): shown once, keep it now\n", t.ID, t.Name, t.Access, exp)
			return nil
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVar(&name, "name", "", "what the token is for (required)")
	cmd.Flags().StringVar(&access, "access", "read", "read or write")
	cmd.Flags().StringVar(&expires, "expires", "90d", "how long it lasts: 1d to 365d, or never")
	cmd.MarkFlagRequired("name")
	return cmd
}

func newTokenList() *cobra.Command {
	var f tokenFlags
	var all bool
	cmd := &cobra.Command{
		Use:   "list (--space DIR | --data DIR --user USERNAME) [--all]",
		Short: "List tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, u, err := f.owner(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			rows, err := store.ListAPITokens(cmd.Context(), st.Q(), store.AllTenants(), store.TokenFilter{UserID: u.ID, Limit: 500})
			if err != nil {
				return err
			}
			now := time.Now()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tPREFIX\tNAME\tACCESS\tEXPIRES\tLAST USED\tSTATE")
			for _, r := range rows {
				state := tokenState(r.APIToken, now)
				if state != "active" && !all {
					continue
				}
				exp, used := "never", "-"
				if !r.Expires.IsZero() {
					exp = r.Expires.Format("2006-01-02")
				}
				if !r.LastUsed.IsZero() {
					used = r.LastUsed.Format("2006-01-02 15:04")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Prefix, oneLine(r.Name), r.Access, exp, used, state)
			}
			return w.Flush()
		},
	}
	f.bind(cmd)
	cmd.Flags().BoolVar(&all, "all", false, "include revoked and expired tokens")
	return cmd
}

func tokenState(t store.APIToken, now time.Time) string {
	switch {
	case !t.Revoked.IsZero():
		return "revoked"
	case !t.Expires.IsZero() && !now.Before(t.Expires):
		return "expired"
	}
	return "active"
}

func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// shownPrefixLen is the length of the prefix a list shows; less than that is
// too little to tell one token from another.
var shownPrefixLen = len(tokens.ShownPrefix(strings.Repeat("x", 64)))

func newTokenRevoke() *cobra.Command {
	var f tokenFlags
	cmd := &cobra.Command{
		Use:   "revoke (--space DIR | --data DIR --user USERNAME) ID|PREFIX",
		Short: "Revoke a token by its id, or by its prefix when only one has it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, u, err := f.owner(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			rows, err := store.ListAPITokens(cmd.Context(), st.Q(), store.AllTenants(), store.TokenFilter{UserID: u.ID, Limit: 500})
			if err != nil {
				return err
			}
			want := strings.TrimSpace(args[0])
			var hit []store.TokenRow
			for _, r := range rows {
				if r.ID == want {
					hit = []store.TokenRow{r}
					break
				}
				if len(want) >= shownPrefixLen && strings.HasPrefix(r.Prefix, want) {
					hit = append(hit, r)
				}
			}
			if len(hit) == 0 && len(want) < shownPrefixLen {
				return fmt.Errorf("%q is not an id of yours; to name a token by its prefix give all %d characters (petalbook token list)", want, shownPrefixLen)
			}
			switch {
			case len(hit) == 0:
				return fmt.Errorf("no token has the id or prefix %q", want)
			case len(hit) > 1:
				return fmt.Errorf("%d tokens have the prefix %q; use an id (petalbook token list --all)", len(hit), want)
			}
			if _, err := store.RevokeAPIToken(cmd.Context(), st.Q(), hit[0].ID, time.Now()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "revoked %s %q\n", hit[0].ID, oneLine(hit[0].Name))
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

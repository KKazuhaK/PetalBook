package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// userEnv is the data directory's state and spaces, opened for a command.
type userEnv struct {
	st  *store.Store
	mgr *spaces.Manager
	svc *auth.Service
}

func (e *userEnv) close() {
	e.mgr.Close()
	e.st.Close()
}

type userFlags struct {
	data, database, username, name string
	admin                          bool
}

func (f *userFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.data, "data", "", "the data directory of the server (required)")
	cmd.Flags().StringVar(&f.database, "database", "", "state database, if not DATA/petalbook.db: a SQLite path or a postgres:// URL")
	cmd.MarkFlagRequired("data")
}

func (f *userFlags) open(ctx context.Context) (*userEnv, error) {
	if err := os.MkdirAll(f.data, 0o755); err != nil {
		return nil, err
	}
	target := f.database
	if target == "" {
		target = filepath.Join(f.data, "petalbook.db")
	}
	st, err := store.Open(ctx, target)
	if err != nil {
		return nil, err
	}
	mgr, err := spaces.NewAccounts(spaces.Options{Store: st, DataDir: f.data, Committer: space.DefaultCommitter})
	if err != nil {
		st.Close()
		return nil, err
	}
	svc, err := auth.New(ctx, auth.Config{Store: st, Spaces: mgr, Password: passwordParams})
	if err != nil {
		mgr.Close()
		st.Close()
		return nil, err
	}
	return &userEnv{st, mgr, svc}, nil
}

func newUser() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage accounts of a data directory",
		Long: `Add users, set passwords and list users directly in a data directory's state
database, for when nobody can sign in. They work while the server runs; a
change made here reaches the running server within a few minutes.

Passwords are read from standard input (one line, or a prompt on a terminal),
never from the command line.`,
	}
	cmd.AddCommand(newUserAdd(), newUserPasswd(), newUserList())
	return cmd
}

func newUserAdd() *cobra.Command {
	var f userFlags
	cmd := &cobra.Command{
		Use:   "add --data DIR --username NAME [--admin] [--name N]",
		Short: "Add a user (the password comes from standard input)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := auth.NormalizeUsername(f.username); err != nil {
				return err
			}
			pw, err := readPassword(cmd, "Password: ", true)
			if err != nil {
				return err
			}
			env, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer env.close()
			role := "member"
			if f.admin {
				role = "admin"
			}
			u, err := env.svc.CreateUser(cmd.Context(), auth.CreateParams{Username: f.username, Name: f.name, Role: role, Password: pw, FirstOK: true})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added %s (%s)\n", u.Username, u.Role)
			return nil
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVar(&f.username, "username", "", "the username (required)")
	cmd.Flags().StringVar(&f.name, "name", "", "display name (default: the username)")
	cmd.Flags().BoolVar(&f.admin, "admin", false, "make the user an administrator")
	cmd.MarkFlagRequired("username")
	return cmd
}

func newUserPasswd() *cobra.Command {
	var f userFlags
	cmd := &cobra.Command{
		Use:   "passwd --data DIR --username NAME",
		Short: "Set a user's password and end their sessions (the password comes from standard input)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := auth.NormalizeUsername(f.username); err != nil {
				return err
			}
			pw, err := readPassword(cmd, "New password: ", true)
			if err != nil {
				return err
			}
			env, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer env.close()
			if err := env.svc.SetPassword(cmd.Context(), f.username, pw); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "password set for %s; their sessions ended\n", strings.ToLower(f.username))
			return nil
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVar(&f.username, "username", "", "the username (required)")
	cmd.MarkFlagRequired("username")
	return cmd
}

func newUserList() *cobra.Command {
	var f userFlags
	cmd := &cobra.Command{
		Use:   "list --data DIR",
		Short: "List users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer env.close()
			us, err := env.svc.Users(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "USERNAME\tROLE\tSTATE\tNAME\tCREATED")
			for _, u := range us {
				state := "enabled"
				if u.Disabled {
					state = "disabled"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", u.Username, u.Role, state, u.Name, u.Created.Format("2006-01-02"))
			}
			return w.Flush()
		},
	}
	f.bind(cmd)
	return cmd
}

// readPassword reads a password from standard input: on a terminal with a
// prompt and no echo (asked twice when confirm is set), otherwise one line.
func readPassword(cmd *cobra.Command, prompt string, confirm bool) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		pw, err := promptHidden(f, cmd.ErrOrStderr(), prompt)
		if err != nil {
			return "", err
		}
		if confirm {
			again, err := promptHidden(f, cmd.ErrOrStderr(), "Again: ")
			if err != nil {
				return "", err
			}
			if again != pw {
				return "", errors.New("the two passwords differ")
			}
		}
		return pw, nil
	}
	line, err := bufio.NewReader(io.LimitReader(in, auth.MaxPasswordBytes+16)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	pw := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if pw == "" {
		return "", errors.New("no password on standard input")
	}
	return pw, nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// promptHidden asks on a terminal with echo off. It uses stty, which every
// Unix has, to avoid a dependency for one prompt.
func promptHidden(tty *os.File, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	off := exec.Command("stty", "-echo")
	off.Stdin = tty
	if err := off.Run(); err != nil {
		return "", errors.New("cannot turn off echo on this terminal; pipe the password to standard input instead")
	}
	defer func() {
		on := exec.Command("stty", "echo")
		on.Stdin = tty
		on.Run()
		fmt.Fprintln(out)
	}()
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", errors.New("no password entered")
	}
	return pw, nil
}

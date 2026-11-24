package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

type draftFlags struct {
	data, space, stateDir, database string
}

func (f *draftFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.data, "data", "", "accounts mode: the data directory of the server")
	fl.StringVar(&f.space, "space", "", "single-user mode: the space directory (its space id is \"default\")")
	fl.StringVar(&f.stateDir, "state-dir", "", "single-user mode: the state directory, if not <space>/.git/petalbook-state")
	fl.StringVar(&f.database, "database", "", "state database, if not the default one: a SQLite path or a postgres:// URL")
}

// open opens the state database of an existing installation, without making one.
func (f *draftFlags) open(ctx context.Context) (*store.Store, error) {
	if (f.data == "") == (f.space == "") {
		return nil, errors.New("give exactly one of --data and --space")
	}
	target := f.database
	if target == "" {
		if f.data != "" {
			target = filepath.Join(f.data, "petalbook.db")
		} else {
			dir := f.stateDir
			if dir == "" {
				dir = space.DefaultStateDir(f.space)
			}
			target = filepath.Join(dir, "state.db")
		}
	}
	if !strings.HasPrefix(target, "postgres://") && !strings.HasPrefix(target, "postgresql://") {
		if _, err := os.Stat(target); err != nil {
			return nil, fmt.Errorf("no state database at %s: %w", target, err)
		}
	}
	return store.Open(ctx, target)
}

func newDraft() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "draft",
		Short: "Find and recover drafts the server could not apply",
		Long: `A draft the server cannot write to its page (the page changed under it, or its
file was removed outside the server) is kept, never deleted, and set aside.
These commands show such drafts and print their text, so it can be recovered.
They read the state database directly and work while the server runs.`,
	}
	cmd.AddCommand(newDraftList(), newDraftExport())
	return cmd
}

func newDraftList() *cobra.Command {
	var f draftFlags
	cmd := &cobra.Command{
		Use:   "list (--data DIR | --space DIR)",
		Short: "List drafts that were set aside",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			ds, err := store.ListHeldDrafts(cmd.Context(), st.Q())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SPACE\tPAGE\tUSER\tUPDATED\tCHARACTERS")
			for _, d := range ds {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", d.SpaceID, d.PageID, d.Username, d.Updated.Format("2006-01-02 15:04:05Z"), d.Chars)
			}
			return w.Flush()
		},
	}
	f.bind(cmd)
	return cmd
}

func newDraftExport() *cobra.Command {
	var f draftFlags
	var user string
	cmd := &cobra.Command{
		Use:   "export SPACE PAGE [--user NAME] (--data DIR | --space DIR)",
		Short: "Print the text of a draft (its header goes to standard error)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			ds, names, err := store.PageDrafts(cmd.Context(), st.Q(), args[0], args[1])
			if err != nil {
				return err
			}
			var pick []int
			for i, n := range names {
				if user == "" || n == strings.ToLower(user) {
					pick = append(pick, i)
				}
			}
			switch {
			case len(pick) == 0:
				return errors.New("no such draft")
			case len(pick) > 1:
				var who []string
				for _, i := range pick {
					who = append(who, names[i])
				}
				return fmt.Errorf("several people have a draft of this page (%s); choose one with --user", strings.Join(who, ", "))
			}
			d := ds[pick[0]]
			fmt.Fprintf(cmd.ErrOrStderr(), "draft of %s by %s, updated %s, header: %s\n", args[1], names[pick[0]], d.Updated.Format("2006-01-02 15:04:05Z"), d.Meta)
			_, err = fmt.Fprint(cmd.OutOrStdout(), d.Body)
			return err
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVar(&user, "user", "", "whose draft, when several people have one")
	return cmd
}

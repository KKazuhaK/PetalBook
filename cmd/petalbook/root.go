package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "petalbook",
		Short:         "Self-hosted notes and writing, kept as Markdown in Git",
		Long:          "PetalBook serves a folder of Markdown pages, kept in a Git repository, to a web app.\n\nEvery flag can also be set with an environment variable: PETALBOOK_ plus the flag name in upper case with dashes as underscores (--state-dir is PETALBOOK_STATE_DIR).",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return applyEnv(cmd.Flags())
		},
	}
	root.SetVersionTemplate("petalbook {{.Version}}\n")
	root.AddCommand(newServe(), newUser(), newSecurity(), newDraft(), newVersion())
	return root
}

func newVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "petalbook %s\n", version)
		},
	}
}

// applyEnv fills flags that were not given on the command line from PETALBOOK_<NAME>.
func applyEnv(fs *pflag.FlagSet) error {
	var err error
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Changed || err != nil {
			return
		}
		name := "PETALBOOK_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if v, ok := os.LookupEnv(name); ok && v != "" {
			if e := f.Value.Set(v); e != nil {
				err = fmt.Errorf("%s=%q: %w", name, v, e)
			} else {
				f.Changed = true
			}
		}
	})
	return err
}

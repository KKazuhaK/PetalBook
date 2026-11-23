package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSecurity() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Rescue the sign-in settings of a data directory",
		Long: `Commands for when the security settings keep everyone out. They work on the
data directory's state database directly, while the server runs or not; a
running server notices the change within a few minutes.`,
	}
	cmd.AddCommand(newSecurityReset())
	return cmd
}

func newSecurityReset() *cobra.Command {
	var f userFlags
	cmd := &cobra.Command{
		Use:   "reset --data DIR",
		Short: "Back to a sign-in that lets people in",
		Long: `Sets the login mode back to dual (password and single sign-on), switches off
"refuse password sign-in", the lockout and every captcha, and clears the
passkey requirement of roles. Providers, people and the requirements set for
single people are left alone ("petalbook user passwd" sets a password). The
change is recorded in the audit log.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer env.close()
			if err := env.svc.ResetSecurity(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "the sign-in settings are reset: login mode dual, password sign-in allowed, lockout and captcha off, no role needs a passkey")
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

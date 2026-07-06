package cli

import (
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/skill"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage the markdown skills tomo can follow",
		Long: "skills are folders under the data dir, each a SKILL.md with a name,\n" +
			"a description, and a permission manifest. Nothing installs them but\n" +
			"you: copy a skill in, lint it, and enable it. There is no remote hub.\n" +
			"The curator may draft one from a workflow it sees you repeat; those\n" +
			"wait under 'drafts' until you install them.",
	}
	cmd.AddCommand(
		newSkillsListCmd(), newSkillsLintCmd(), newSkillsEnableCmd(), newSkillsDisableCmd(),
		newSkillsDraftsCmd(), newSkillsInstallCmd(), newSkillsDiscardCmd(),
	)
	return cmd
}

// skillStore opens the skill store rooted in the configured data dir.
func skillStore(cmd *cobra.Command) (*skill.Store, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, err
	}
	return &skill.Store{Dir: filepath.Join(cfg.DataDir, "skills")}, nil
}

// draftStore opens the store of skills the curator has proposed but not
// installed. It is kept apart from the installed store so a draft never rides
// in the prompt until the user installs it.
func draftStore(cmd *cobra.Command) (*skill.Store, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, err
	}
	return &skill.Store{Dir: filepath.Join(cfg.DataDir, "skill-drafts")}, nil
}

func newSkillsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed skills and their state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := skillStore(cmd)
			if err != nil {
				return err
			}
			entries, err := st.Entries()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no skills installed")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tON\tPERMS\tDESCRIPTION")
			for _, e := range entries {
				if e.Err != nil {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Name, onOff(e.Enabled), "-", "broken: "+e.Err.Error())
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Name, onOff(e.Enabled), perms(e.Permissions), e.Description)
			}
			return w.Flush()
		},
	}
}

func newSkillsLintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lint",
		Short: "Scan skills for hidden instructions and undeclared capabilities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := skillStore(cmd)
			if err != nil {
				return err
			}
			findings, err := st.Lint()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(findings) == 0 {
				fmt.Fprintln(out, "no problems found")
				return nil
			}
			for _, f := range findings {
				fmt.Fprintf(out, "%s: %s: %s\n", f.Skill, f.Level, f.Message)
			}
			return fmt.Errorf("%d problem(s) found", len(findings))
		},
	}
}

func newSkillsEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a skill so it rides in the prompt",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := skillStore(cmd)
			if err != nil {
				return err
			}
			if err := st.Enable(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enabled %s\n", args[0])
			return nil
		},
	}
}

func newSkillsDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable a skill without removing it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := skillStore(cmd)
			if err != nil {
				return err
			}
			if err := st.Disable(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "disabled %s\n", args[0])
			return nil
		},
	}
}

func newSkillsDraftsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drafts",
		Short: "List skills the curator has proposed for you to review",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ds, err := draftStore(cmd)
			if err != nil {
				return err
			}
			entries, err := ds.Entries()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(entries) == 0 {
				fmt.Fprintln(out, "no drafts waiting")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPERMS\tDESCRIPTION")
			for _, e := range entries {
				if e.Err != nil {
					fmt.Fprintf(w, "%s\t%s\t%s\n", e.Name, "-", "broken: "+e.Err.Error())
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", e.Name, perms(e.Permissions), e.Description)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out, "\nreview one with: tomo skills install <name> (or discard <name>)")
			return nil
		},
	}
}

func newSkillsInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install <name>",
		Short: "Install a drafted skill so it rides in the prompt",
		Long: "install promotes a draft the curator proposed into your installed\n" +
			"skills. This is the explicit step: nothing a reflection drafts takes\n" +
			"effect until you install it. Lint it first if you want a closer look.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ds, err := draftStore(cmd)
			if err != nil {
				return err
			}
			ss, err := skillStore(cmd)
			if err != nil {
				return err
			}
			if err := skill.Promote(ds, ss, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s\n", args[0])
			return nil
		},
	}
}

func newSkillsDiscardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discard <name>",
		Short: "Throw away a drafted skill you do not want",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ds, err := draftStore(cmd)
			if err != nil {
				return err
			}
			if err := ds.Remove(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "discarded %s\n", args[0])
			return nil
		},
	}
}

// perms renders a manifest as a compact rnwx string, dashes for what is off.
func perms(p skill.Permissions) string {
	b := []byte("----")
	if p.Read {
		b[0] = 'r'
	}
	if p.Net {
		b[1] = 'n'
	}
	if p.Write {
		b[2] = 'w'
	}
	if p.Exec {
		b[3] = 'x'
	}
	return string(b)
}

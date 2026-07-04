package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/hook"
	"github.com/tamnd/ari/nest"
)

var trustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Trust this workspace so its repo hooks may run",
	Long: `Record whether this workspace is trusted to run the hooks its
committed config defines.

A repo can carry hooks in .ari/config.toml, and those never run until you
say so: no auto-trust, no prompt that trusting is the easy default. Run
ari trust after you have read the hooks (ari doctor lists them), and only
if you wrote them or vouch for whoever did.

  ari trust            trust this workspace
  ari trust --revoke   take the trust back
  ari trust --show     print the current decision and the hooks it gates`,
	RunE: func(c *cobra.Command, args []string) error {
		revoke, _ := c.Flags().GetBool("revoke")
		show, _ := c.Flags().GetBool("show")
		return runTrust(os.Stdout, revoke, show)
	},
}

func init() {
	trustCmd.Flags().Bool("revoke", false, "revoke this workspace's trust")
	trustCmd.Flags().Bool("show", false, "print the trust decision and the hooks it gates without changing it")
	rootCmd.AddCommand(trustCmd)
}

// runTrust records or reports the workspace trust decision. It never trusts
// silently or by default: the operator has to run it, which is the whole point
// of the gate (doc 05 section 12, D16).
func runTrust(out io.Writer, revoke, show bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	n, err := nest.Resolve(cwd)
	if err != nil {
		return err
	}
	if err := n.EnsureGlobal(); err != nil {
		return err
	}
	store := hook.LoadTrust(n.TrustFile())

	var b strings.Builder
	switch {
	case show:
		showTrust(&b, n, store)
	case revoke:
		if err := store.Revoke(n.Root, time.Now()); err != nil {
			return err
		}
		fmt.Fprintf(&b, "revoked trust for %s; its repo hooks will not run\n", n.Root)
	default:
		if err := store.Trust(n.Root, time.Now()); err != nil {
			return err
		}
		fmt.Fprintf(&b, "trusted %s; its repo hooks may now run\n", n.Root)
	}
	_, err = io.WriteString(out, b.String())
	return err
}

// showTrust writes the current decision and the repo hooks the gate governs,
// so the operator reads what trusting would let run before deciding.
func showTrust(b *strings.Builder, n nest.Nest, store *hook.TrustStore) {
	if store.IsTrusted(n.Root) {
		fmt.Fprintf(b, "%s is trusted\n", n.Root)
	} else {
		fmt.Fprintf(b, "%s is untrusted\n", n.Root)
	}
	cfg, err := config.Load(n, config.FlagOverrides{})
	if err != nil {
		fmt.Fprintf(b, "(config did not load, cannot list hooks: %v)\n", err)
		return
	}
	var repo []hook.Command
	for _, c := range cfg.Hooks() {
		if c.Layer != "user" {
			repo = append(repo, c)
		}
	}
	if len(repo) == 0 {
		b.WriteString("no repo hooks to gate\n")
		return
	}
	b.WriteString("repo hooks gated by this decision:\n")
	for _, c := range repo {
		fmt.Fprintf(b, "  %s\n", hook.Describe(c))
	}
}

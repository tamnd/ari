package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/doctor"
	"github.com/tamnd/ari/nest"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Audit the nest, permission rules, and listening surfaces",
	Long: `Audit the nest, the config, and any listening surface for the
mistakes that turn a coding agent into someone else's remote shell.

doctor runs a fixed list of checks and prints each one's status and reason.
--fix applies the safe repairs and leaves the judgment calls for you.
--audit runs the deeper integrity checks a reviewer would.

The exit code is a CI contract: 0 clean, 1 warnings only, 2 at least one
critical, 3 doctor could not run.`,
	RunE: func(c *cobra.Command, args []string) error {
		fix, _ := c.Flags().GetBool("fix")
		audit, _ := c.Flags().GetBool("audit")
		return runDoctor(c, os.Stdout, fix, audit)
	},
}

func init() {
	doctorCmd.Flags().Bool("fix", false, "apply the safe repairs and report the rest")
	doctorCmd.Flags().Bool("audit", false, "run the deeper integrity checks a reviewer would")
	rootCmd.AddCommand(doctorCmd)
}

// runDoctor assembles the check context, runs the checks, optionally
// applies fixes, prints the report, and returns a coded error so the
// process exit is doctor's own 0/1/2/3 contract, not the taxonomy the
// other commands use (doc 14 section 12.4).
func runDoctor(c *cobra.Command, out io.Writer, fix, audit bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return coded(3, err)
	}
	n, err := nest.Resolve(cwd)
	if err != nil {
		return coded(3, err)
	}

	cfg, loadErr := config.Load(n, config.FlagOverrides{})
	ctx := &doctor.Context{Nest: n, Config: cfg, LoadErr: loadErr, Audit: audit}

	doc := doctor.New()
	report := doc.Run(ctx)

	if fix {
		applyFixes(out, ctx, doc, &report)
	}

	printReport(out, report)
	return coded(doctorExit(report.Worst()), nil)
}

// applyFixes runs each finding's safe fix, then re-runs the checks so the
// printed report reflects the repaired state. A fix that fails is reported
// and the finding stays, because doctor tells the truth about what it could
// not repair (section 12.3).
func applyFixes(out io.Writer, ctx *doctor.Context, doc *doctor.Doctor, report *doctor.Report) {
	fixed := 0
	for _, f := range report.Findings {
		if f.Status == doctor.StatusOK || f.Fix == nil {
			continue
		}
		if err := f.Fix(ctx); err != nil {
			fmt.Fprintf(out, "fix %q failed: %v\n", f.Check, err)
			continue
		}
		fmt.Fprintf(out, "fixed: %s\n", f.Check)
		fixed++
	}
	if fixed > 0 {
		fmt.Fprintln(out)
		*report = doc.Run(ctx)
	}
}

// printReport writes the findings in check order with a one-line summary,
// human-voiced, no em-dashes (D24).
func printReport(out io.Writer, report doctor.Report) {
	for _, f := range report.Findings {
		fmt.Fprintf(out, "[%s] %s: %s\n", f.Status, f.Check, f.Reason)
		if f.Status != doctor.StatusOK && f.Manual != "" {
			fmt.Fprintf(out, "       fix: %s\n", f.Manual)
		}
	}
	fmt.Fprintln(out)
	switch report.Worst() {
	case doctor.StatusCritical:
		fmt.Fprintln(out, "result: critical findings, fix these before you trust this workspace")
	case doctor.StatusWarn:
		fmt.Fprintln(out, "result: warnings, review them when you can")
	default:
		fmt.Fprintln(out, "result: clean")
	}
}

// doctorExit maps the worst status to doctor's process exit code.
func doctorExit(worst doctor.Status) int {
	switch worst {
	case doctor.StatusCritical:
		return 2
	case doctor.StatusWarn:
		return 1
	default:
		return 0
	}
}

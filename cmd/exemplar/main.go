// Command exemplar is the Exemplar CLI: config validation, replay of
// recorded PRs, the adversarial set, sandbox checks, and `exemplar
// review` of the working tree.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// version is set at release (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "exemplar",
		Short:         "Exemplar: self-correcting AI code review",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.AddCommand(newValidateCmd(), newMutationsCmd(), newReplayCmd(), newAdversarialCmd(), newSandboxCmd(), newTryCmd(), newEvalCmd(), newVolumeCmd(), newLinterOverlapCmd(), newExportApprovalCmd(), newCalibrateCmd(), newLoginCmd(), newReviewCmd())
	return root
}

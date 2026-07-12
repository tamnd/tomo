package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/policy"
)

// yoloApprover approves every escalated action without asking. It is installed
// only when --yolo is set, which turns tomo fully autonomous: a gate that would
// otherwise stop to ask a human instead proceeds. Explicit deny rules are
// untouched, because the policy engine never routes a denied action through the
// approver, so --yolo widens ask to allow but never overrides a deny.
type yoloApprover struct{}

func (yoloApprover) Approve(context.Context, policy.Request) (bool, error) { return true, nil }

// approverFor picks the approver for a run. With --yolo tomo auto-approves every
// ask, which is how an agent runs unattended in a sandbox; without it, tomo
// prompts on the terminal and a headless run declines what it cannot ask about.
// The warning goes to stderr so even a piped `tomo -p` run leaves a record that
// protection was off.
func approverFor(cmd *cobra.Command, tio *termIO, warn io.Writer) policy.Approver {
	yolo, _ := cmd.Flags().GetBool("yolo")
	skip, _ := cmd.Flags().GetBool("dangerously-skip-permissions")
	if yolo || skip {
		fmt.Fprintln(warn, "tomo: --yolo is on, every action is auto-approved and the policy gate will not stop to ask")
		return yoloApprover{}
	}
	return tio
}

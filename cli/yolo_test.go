package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/policy"
)

// newYoloCmd mirrors how the root registers the two flags, so the picker is
// tested against the same flag set the binary ships.
func newYoloCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "t"}
	cmd.Flags().Bool("yolo", false, "")
	cmd.Flags().Bool("dangerously-skip-permissions", false, "")
	return cmd
}

func TestApproverForDefaultsToTerminal(t *testing.T) {
	cmd := newYoloCmd()
	tio := newTermIO(strings.NewReader(""), &bytes.Buffer{})
	var warn bytes.Buffer
	if got := approverFor(cmd, tio, &warn); got != tio {
		t.Fatalf("without --yolo the approver should be the terminal, got %T", got)
	}
	if warn.Len() != 0 {
		t.Fatalf("no warning expected when protection is on, got %q", warn.String())
	}
}

func TestApproverForYoloAutoApproves(t *testing.T) {
	for _, flag := range []string{"yolo", "dangerously-skip-permissions"} {
		cmd := newYoloCmd()
		if err := cmd.Flags().Set(flag, "true"); err != nil {
			t.Fatalf("set %s: %v", flag, err)
		}
		tio := newTermIO(strings.NewReader(""), &bytes.Buffer{})
		var warn bytes.Buffer
		got := approverFor(cmd, tio, &warn)
		if _, ok := got.(yoloApprover); !ok {
			t.Fatalf("--%s should install the auto-approver, got %T", flag, got)
		}
		ok, err := got.Approve(t.Context(), policy.Request{})
		if err != nil || !ok {
			t.Fatalf("--%s approver must approve, got (%v, %v)", flag, ok, err)
		}
		if !strings.Contains(warn.String(), "auto-approved") {
			t.Fatalf("--%s should warn on stderr, got %q", flag, warn.String())
		}
	}
}

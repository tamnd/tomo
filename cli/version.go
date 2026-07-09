package cli

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// pseudoVersionRe matches the tail of a Go pseudo-version, the 14-digit UTC
// timestamp and the 12-hex commit prefix the toolchain appends to an untagged
// build, as in v0.2.2-0.20260709142126-c2a23456aed8.
var pseudoVersionRe = regexp.MustCompile(`(\d{14})-([0-9a-f]{12})$`)

// resolveVersion returns the version, commit, and build date to report. A release
// binary carries them in the ldflags above, stamped by goreleaser. A binary from
// `go install` or `go build` has none of that, so this falls back to the build
// info the Go toolchain embeds in every binary: the module version and the VCS
// revision and time of the commit it was built from. Either way `tomo version`
// tells the truth about the build, rather than the placeholder "dev".
func resolveVersion() (version, commit, date string) {
	version, commit, date = Version, Commit, Date
	if version != "dev" {
		return version, commit, date
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version, commit, date
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		version = v
		// A binary from `go install pkg@commit` has no VCS settings, since it was
		// built from the module cache, not a checkout. The pseudo-version still
		// carries the commit and the date, so read them back out of it.
		if m := pseudoVersionRe.FindStringSubmatch(v); m != nil {
			commit = m[2]
			date = fmt.Sprintf("%s-%s-%s", m[1][0:4], m[1][4:6], m[1][6:8])
		}
	}
	// A local `go build` in a checkout does embed the VCS settings, and those are
	// the most precise, so let them win over anything the pseudo-version implied.
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				commit = s.Value[:7]
			} else if s.Value != "" {
				commit = s.Value
			}
		case "vcs.time":
			if len(s.Value) >= 10 {
				date = s.Value[:10]
			}
		}
	}
	return version, commit, date
}

// shortVersion is the one-line string fang shows for `tomo --version`: the
// version, and the commit and date in parentheses when they are known.
func shortVersion() string {
	version, commit, date := resolveVersion()
	var extra []string
	if commit != "none" && commit != "" {
		extra = append(extra, commit)
	}
	if date != "unknown" && date != "" {
		extra = append(extra, date)
	}
	if len(extra) == 0 {
		return version
	}
	return fmt.Sprintf("%s (%s)", version, strings.Join(extra, ", "))
}

// newVersionCmd prints the full build detail, the standard block a Go tool shows:
// version, commit, build date, the Go toolchain, and the target platform.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, and build details",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			version, commit, date := resolveVersion()
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "tomo %s\n", version)
			fmt.Fprintf(w, "commit:   %s\n", commit)
			fmt.Fprintf(w, "built:    %s\n", date)
			fmt.Fprintf(w, "go:       %s\n", runtime.Version())
			fmt.Fprintf(w, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}

package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"shitty-ci/internal/types"
)

var ansiRe = regexp.MustCompile(`\033\[[0-9;]*m`)

// StdoutUseColor reports whether ANSI styling is appropriate for stdout.
func StdoutUseColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return isatty.IsTerminal(os.Stdout.Fd())
}

// StdoutIsTerminal reports whether stdout is a TTY (pipes are not).
func StdoutIsTerminal() bool {
	return isatty.IsTerminal(os.Stdout.Fd())
}

// PrintBuildsCwdHint explains that builds output is scoped to the detected GitHub repo.
func PrintBuildsCwdHint(w io.Writer, color bool) {
	a := ansi{on: color}
	msg := "Showing builds for the current Git repository only. Use --all for every tracked repo."
	if color {
		fmt.Fprintln(w, a.dim(msg))
		return
	}
	fmt.Fprintln(w, msg)
}

// PrintBuilds renders a human-readable table of builds.
// If tips is true, a short footer explains id prefixes for logs/cancel.
func PrintBuilds(w io.Writer, builds []types.Build, color, tips bool) {
	a := ansi{on: color}

	fmt.Fprintf(w, "%s\n\n", a.title(fmt.Sprintf("Builds (%d)", len(builds))))
	if len(builds) == 0 {
		msg := "No matching builds."
		if color {
			msg = a.dim(msg)
		}
		fmt.Fprintln(w, msg)
		return
	}

	fmt.Fprintf(w, "%-9s  %-20s  %-8s  %-12s  %-40s  %s\n",
		"BUILD", "REPO", "SHA", "REF", "STATE", "CREATED")

	for _, b := range builds {
		created := "-"
		if b.CreatedAt != 0 {
			created = time.Unix(b.CreatedAt, 0).Local().Format("2006-01-02 15:04")
		}
		stateCell := a.stateWithStep(b.State, b.Step)
		fmt.Fprintf(w, "%-9s  %-20s  %-8s  %-12s  %s\n",
			shortBuildID(b.ID),
			b.Repo,
			shortSHA(b.SHA),
			shortRef(b.Ref),
			padVisible(stateCell, 40)+"  "+created,
		)
	}

	if !tips {
		return
	}
	fmt.Fprintln(w)
	tip := "Tip: run `shitty-ci logs` inside a tracked repo to open the latest build for the current branch, or pass a build id prefix (≥4 hex) / commit SHA prefix."
	if color {
		fmt.Fprintln(w, a.dim(tip))
	} else {
		fmt.Fprintln(w, tip)
	}
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func shortBuildID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// padVisible right-pads a string (possibly containing ANSI escape codes) to the
// given visible width by ignoring escape sequences in length calculations.
func padVisible(s string, width int) string {
	visible := ansiRe.ReplaceAllString(s, "")
	if len(visible) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(visible))
}

func shortRef(ref string) string {
	const origin = "refs/remotes/origin/"
	if strings.HasPrefix(ref, origin) {
		b := strings.TrimPrefix(ref, origin)
		if b == "HEAD" {
			return "HEAD"
		}
		return b
	}
	const tag = "refs/tags/"
	if strings.HasPrefix(ref, tag) {
		return "tag:" + strings.TrimPrefix(ref, tag)
	}
	return ref
}

package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"shitty-ci/internal/types"
)

// PrintStatus renders daemon status in a readable table.
func PrintStatus(w io.Writer, st types.DaemonStatus, color, tips bool) {
	a := ansi{on: color}

	fmt.Fprintf(w, "%s\n\n", a.title("Daemon status"))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "OK\t%s\n", a.yesNo(st.OK))
	fmt.Fprintf(tw, "Queue depth\t%d\n", st.QueueDepth)
	fmt.Fprintf(tw, "Running builds\t%d\n", st.RunningBuilds)
	fmt.Fprintf(tw, "Max concurrent\t%d\n", st.MaxConcurrent)
	fmt.Fprintf(tw, "Poll interval\t%s\n", st.PollInterval)
	fmt.Fprintf(tw, "Data directory\t%s\n", st.DataDir)
	_ = tw.Flush()

	if !tips {
		return
	}
	fmt.Fprintln(w)
	if color {
		fmt.Fprintln(w, a.dim("Tip: `shitty-ci config` shows the on-disk config path and reloadable settings."))
	} else {
		fmt.Fprintln(w, "Tip: `shitty-ci config` shows the on-disk config path and reloadable settings.")
	}
}

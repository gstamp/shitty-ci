package cli

import (
	"strings"

	"shitty-ci/internal/types"
)

type ansi struct {
	on bool
}

func (a ansi) wrap(code, s string) string {
	if !a.on || s == "" {
		return s
	}
	return code + s + "\033[0m"
}

func (a ansi) title(s string) string {
	return a.wrap("\033[1m", s)
}

func (a ansi) dim(s string) string {
	return a.wrap("\033[2m", s)
}

func (a ansi) yesNo(ok bool) string {
	if !a.on {
		if ok {
			return "yes"
		}
		return "no"
	}
	if ok {
		return "\033[32myes\033[0m"
	}
	return "\033[31mno\033[0m"
}

const maxStepLen = 28

func (a ansi) stateWithStep(st types.BuildState, step string) string {
	stateStr := a.state(st)
	step = strings.TrimSpace(step)
	if step == "" {
		return stateStr
	}
	if len(step) > maxStepLen {
		step = step[:maxStepLen-3] + "..."
	}
	if !a.on {
		return stateStr + " (" + step + ")"
	}
	return stateStr + " (\033[36m" + step + "\033[0m)"
}

func (a ansi) state(st types.BuildState) string {
	if !a.on {
		return string(st)
	}
	switch st {
	case types.BuildSuccess:
		return "\033[32m" + string(st) + "\033[0m"
	case types.BuildFailure:
		return "\033[31m" + string(st) + "\033[0m"
	case types.BuildTimedOut, types.BuildCancelled:
		return "\033[33m" + string(st) + "\033[0m"
	case types.BuildRunning:
		return "\033[36m" + string(st) + "\033[0m"
	case types.BuildPending:
		return "\033[90m" + string(st) + "\033[0m"
	default:
		return string(st)
	}
}

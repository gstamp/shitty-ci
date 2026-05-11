package cli

import "strings"

// TailLines returns the last n lines of s, using '\n' as the line separator.
// CR characters are stripped from input (\r\n normalized to \n for splitting only).
// If n <= 0, s is returned unchanged.
func TailLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	normalized := strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	hasTrailingNL := len(lines) > 0 && lines[len(lines)-1] == ""
	if hasTrailingNL {
		lines = lines[:len(lines)-1]
	}

	if len(lines) <= n {
		return s
	}

	lines = lines[len(lines)-n:]
	out := strings.Join(lines, "\n")
	if hasTrailingNL {
		return out + "\n"
	}
	return out
}

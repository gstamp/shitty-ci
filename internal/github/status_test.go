package github

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStepFailureDescription_keepsLogSuffix(t *testing.T) {
	id := strings.Repeat("a", 32)
	got := StepFailureDescription("Install dependencies", "exit status 1", id)
	if !strings.HasSuffix(got, id) {
		t.Fatalf("expected suffix build id, got %q", got)
	}
	if !strings.Contains(got, "Install dependencies") {
		t.Fatalf("expected step name, got %q", got)
	}
	if !strings.Contains(got, "exit status 1") {
		t.Fatalf("expected error text, got %q", got)
	}
	if utf8.RuneCountInString(got) > 140 {
		t.Fatalf("expected <=140 runes, got %d: %q", utf8.RuneCountInString(got), got)
	}
}

func TestStepFailureDescription_truncatesLongError(t *testing.T) {
	id := strings.Repeat("b", 32)
	longErr := strings.Repeat("x", 500)
	got := StepFailureDescription("Build", longErr, id)
	if utf8.RuneCountInString(got) > 140 {
		t.Fatalf("expected <=140 runes, got %d", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, id) {
		t.Fatalf("expected suffix build id, got %q", got)
	}
}

func TestCommitStatusTargetURL(t *testing.T) {
	got := CommitStatusTargetURL("octocat", "Hello-World", "deadbeef")
	want := "https://github.com/octocat/Hello-World/commit/deadbeef/checks"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := CommitStatusTargetURL("", "r", "s"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestStatusDescriptionWithLogsHint_keepsSuffix(t *testing.T) {
	id := strings.Repeat("c", 32)
	got := StatusDescriptionWithLogsHint("git fetch failed: boom", id)
	if !strings.Contains(got, "git fetch failed") {
		t.Fatalf("expected message, got %q", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), id) {
		t.Fatalf("expected build id suffix, got %q", got)
	}
	if utf8.RuneCountInString(got) > 140 {
		t.Fatalf("expected <=140 runes, got %d", utf8.RuneCountInString(got))
	}
}

func TestSanitizeGitHubStatusDescription(t *testing.T) {
	in := "  hello\nworld\t "
	got := sanitizeGitHubStatusDescription(in)
	if got != "hello world" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("é", 200) // 2-byte runes
	got = sanitizeGitHubStatusDescription(long)
	if utf8.RuneCountInString(got) != 140 {
		t.Fatalf("expected 140 runes, got %d", utf8.RuneCountInString(got))
	}
}

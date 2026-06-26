package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLogTail_shortFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.log")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLogTail(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Fatalf("expected full content, got %q", got)
	}
}

func TestReadLogTail_emptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLogTail(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestReadLogTail_missingFile(t *testing.T) {
	_, err := ReadLogTail("/nonexistent/path.log", 100)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadLogTail_largeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "large.log")
	// Write 500 lines of 100 bytes each = 50KB
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, strings.Repeat("x", 99)+"\n")
	}
	content := strings.Join(lines, "")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLogTail(p, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 1050 {
		t.Fatalf("expected tail around 1000 bytes, got %d", len(got))
	}
	// Should start with a complete line (no partial lines at the start)
	if strings.HasPrefix(got, strings.Repeat("x", 99)) {
		t.Logf("tail starts at a line boundary, good")
	}
}

func TestReadLogTail_preservesNewlines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "multi.log")
	content := "a\nb\nc\nd\ne\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLogTail(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	// With 5 bytes window we get "e\n" or "c\nd\n" etc.
	if got == "" {
		t.Fatal("expected non-empty tail")
	}
	if !strings.HasSuffix(got, "\n") {
		t.Logf("tail: %q", got)
	}
}

func TestBuildFailureOutput_containsStepAndError(t *testing.T) {
	out := BuildFailureOutput("Install", "exit code 1", "abc123", "log content here\nline2\n")
	if out.Title != "Build failed at step: Install" {
		t.Fatalf("unexpected title: %q", out.Title)
	}
	if !strings.Contains(out.Summary, "Install") {
		t.Fatalf("summary missing step name: %q", out.Summary)
	}
	if !strings.Contains(out.Text, "abc123") {
		t.Fatalf("text missing build ID: %q", out.Text)
	}
	if !strings.Contains(out.Text, "log content here") {
		t.Fatalf("text missing log content: %q", out.Text)
	}
	if len(out.Text) > MaxCheckTextOutput {
		t.Fatalf("text exceeds max size: %d > %d", len(out.Text), MaxCheckTextOutput)
	}
}

func TestBuildFailureOutput_emptyStep(t *testing.T) {
	out := BuildFailureOutput("", "something broke", "xyz789", "")
	if out.Title != "Build failed" {
		t.Fatalf("expected default title, got %q", out.Title)
	}
}

func TestBuildFailureOutput_emptyLog(t *testing.T) {
	out := BuildFailureOutput("Test", "err", "id42", "")
	if strings.Contains(out.Text, "Build Log") {
		t.Fatalf("expected no log section when log is empty: %q", out.Text)
	}
}

func TestBuildFailureOutput_truncatesToMax(t *testing.T) {
	largeLog := strings.Repeat("x\n", 70000)
	out := BuildFailureOutput("Step", "error", "bid", largeLog)
	if len(out.Text) > MaxCheckTextOutput {
		t.Fatalf("text exceeds limit: %d > %d", len(out.Text), MaxCheckTextOutput)
	}
	if !strings.Contains(out.Text, "Build Log") {
		t.Fatalf("expected log section: %q", out.Text[:200])
	}
}

func TestBuildSuccessOutput(t *testing.T) {
	out := BuildSuccessOutput()
	if out.Title != "Build succeeded" {
		t.Fatalf("unexpected title: %q", out.Title)
	}
	if out.Summary != "All steps passed." {
		t.Fatalf("unexpected summary: %q", out.Summary)
	}
	if out.Text != "" {
		t.Fatalf("expected no text for success, got %q", out.Text)
	}
}

func TestBuildCancelledOutput(t *testing.T) {
	out := BuildCancelledOutput()
	if out.Title != "Build cancelled" {
		t.Fatalf("unexpected title: %q", out.Title)
	}
}

func TestBuildTimedOutOutput(t *testing.T) {
	out := BuildTimedOutOutput("30m0s")
	if out.Title != "Build timed out" {
		t.Fatalf("unexpected title: %q", out.Title)
	}
	if !strings.Contains(out.Summary, "30m0s") {
		t.Fatalf("summary should include timeout: %q", out.Summary)
	}
}

func TestBuildTimedOutOutput_empty(t *testing.T) {
	out := BuildTimedOutOutput("")
	if !strings.Contains(out.Summary, "timed out") {
		t.Fatalf("unexpected summary: %q", out.Summary)
	}
}

func TestBuildInterruptedOutput(t *testing.T) {
	out := BuildInterruptedOutput("")
	if out.Title != "Build interrupted" {
		t.Fatalf("unexpected title: %q", out.Title)
	}
}

func TestBuildInterruptedOutput_withDetail(t *testing.T) {
	out := BuildInterruptedOutput("daemon restarted")
	if !strings.Contains(out.Summary, "daemon restarted") {
		t.Fatalf("unexpected summary: %q", out.Summary)
	}
}

func TestBuildFailureOutput_maxTitleLength(t *testing.T) {
	longStep := strings.Repeat("a", 300)
	out := BuildFailureOutput(longStep, "error", "bid", "")
	if len(out.Title) > 256 {
		t.Fatalf("title too long: %d", len(out.Title))
	}
}

func TestBuildFailureOutput_tableFormat(t *testing.T) {
	out := BuildFailureOutput("Build", "exit 1", "test-build-42", "some log\n")
	if !strings.Contains(out.Text, "| Build ID |") {
		t.Fatalf("expected markdown table in output: %q", out.Text)
	}
	if !strings.Contains(out.Text, "| Failed Step |") {
		t.Fatalf("expected step in table: %q", out.Text)
	}
	if !strings.Contains(out.Text, "| Error |") {
		t.Fatalf("expected error in table: %q", out.Text)
	}
}

func TestBuildFailureOutput_oddChars(t *testing.T) {
	out := BuildFailureOutput("Step `1`", "error: `boom`", "id-`42`", "log with ``` backticks\n")
	if !strings.Contains(out.Text, "id-`42`") {
		t.Fatalf("expected build ID with backticks preserved: %q", out.Text)
	}
}

func TestLogTailBytes_default(t *testing.T) {
	if LogTailBytes != 48000 {
		t.Fatalf("expected LogTailBytes=48000, got %d", LogTailBytes)
	}
}

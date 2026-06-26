package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// MaxCheckTextOutput is GitHub's limit for check run output.text (in bytes / runes,
// effectively 65535). We use string length which is close enough for Markdown ASCII
// and stays safely under the wire limit.
const MaxCheckTextOutput = 65535

// CheckRunOutput holds the GitHub Check Run output object.
// Text supports up to 64KB of Markdown content.
type CheckRunOutput struct {
	Title       string       `json:"title"`
	Summary     string       `json:"summary"`
	Text        string       `json:"text,omitempty"`
	Annotations []Annotation `json:"annotations,omitempty"`
}

// Annotation holds a file-level annotation for a check run.
type Annotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	StartColumn     int    `json:"start_column,omitempty"`
	EndColumn       int    `json:"end_column,omitempty"`
	AnnotationLevel string `json:"annotation_level"` // notice, warning, failure
	Message         string `json:"message"`
	Title           string `json:"title,omitempty"`
	RawDetails      string `json:"raw_details,omitempty"`
}

type createCheckRunPayload struct {
	Name       string          `json:"name"`
	HeadSHA    string          `json:"head_sha"`
	Status     string          `json:"status"`
	Conclusion string          `json:"conclusion,omitempty"`
	StartedAt  string          `json:"started_at,omitempty"`
	Output     *CheckRunOutput `json:"output,omitempty"`
	DetailsURL string          `json:"details_url,omitempty"`
}

type updateCheckRunPayload struct {
	Status      string          `json:"status"`
	Conclusion  string          `json:"conclusion,omitempty"`
	CompletedAt string          `json:"completed_at,omitempty"`
	Output      *CheckRunOutput `json:"output,omitempty"`
	DetailsURL  string          `json:"details_url,omitempty"`
}

type checkRunIDResponse struct {
	ID int64 `json:"id"`
}

// CreateCheckRun creates a new check run with the given name on the given
// commit and returns its ID. It starts in "in_progress" status so the UI
// shows activity immediately.
func CreateCheckRun(token, owner, repo, sha, checkName, detailsURL string) (int64, error) {
	if token == "" {
		return 0, fmt.Errorf("missing github_token")
	}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/check-runs", owner, repo)
	payload := createCheckRunPayload{
		Name:       checkName,
		HeadSHA:    sha,
		Status:     "in_progress",
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
		DetailsURL: strings.TrimSpace(detailsURL),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal check run: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return 0, fmt.Errorf("github create check run %s: %s", resp.Status, buf.String())
	}

	var cr checkRunIDResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return 0, fmt.Errorf("decode check run response: %w", err)
	}
	return cr.ID, nil
}

// UpdateCheckRun updates an existing check run with a new status and optional
// detailed output.
func UpdateCheckRun(token, owner, repo string, checkRunID int64, status, conclusion string, output *CheckRunOutput, detailsURL string) error {
	if token == "" {
		return fmt.Errorf("missing github_token")
	}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/check-runs/%d", owner, repo, checkRunID)

	payload := updateCheckRunPayload{
		Status:      status,
		Conclusion:  conclusion,
		DetailsURL:  strings.TrimSpace(detailsURL),
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
		Output:      output,
	}

	if conclusion == "" {
		// still in progress — don't set completed_at
		payload.CompletedAt = ""
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal update check run: %w", err)
	}

	req, err := http.NewRequest(http.MethodPatch, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("github update check run %s: %s", resp.Status, buf.String())
	}
	return nil
}

// LogTailBytes is the default number of bytes to read from the tail of a build
// log for inclusion in check run failure output.
const LogTailBytes = 48000

// ReadLogTail reads the last maxBytes of a log file. It seeks to the first
// newline within the read window so that the result starts on a complete line.
func ReadLogTail(path string, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", err
	}

	size := fi.Size()
	if size == 0 {
		return "", nil
	}

	if size <= int64(maxBytes) {
		buf := make([]byte, size)
		if _, err := f.Read(buf); err != nil {
			return "", err
		}
		return string(buf), nil
	}

	// Read from near the end.
	buf := make([]byte, maxBytes)
	if _, err := f.ReadAt(buf, size-int64(maxBytes)); err != nil {
		return "", err
	}

	s := string(buf)
	// Skip partial first line.
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[idx+1:]
	}
	return s, nil
}

// BuildFailureOutput constructs a CheckRunOutput with detailed failure
// information, including the tail of the build log.  The text field is
// truncated to MaxCheckTextOutput (64KB) to stay under GitHub's limit.
func BuildFailureOutput(stepName, errText, buildID, logTail string) *CheckRunOutput {
	title := "Build failed"
	if stepName != "" {
		title = fmt.Sprintf("Build failed at step: %s", stepName)
	}

	summary := fmt.Sprintf("Step **%s** exited with error.", stepName)
	if errText != "" {
		summary = fmt.Sprintf("Step **%s** failed: %s", stepName, errText)
	}

	var b strings.Builder
	b.WriteString("## Build Details\n\n")
	b.WriteString(fmt.Sprintf("| Field | Value |\n|-------|-------|\n"))
	b.WriteString(fmt.Sprintf("| Build ID | `%s` |\n", buildID))

	if stepName != "" {
		b.WriteString(fmt.Sprintf("| Failed Step | `%s` |\n", stepName))
	}
	if errText != "" {
		b.WriteString(fmt.Sprintf("| Error | `%s` |\n", truncateString(errText, 2000)))
	}
	b.WriteString("\n")

	if logTail != "" {
		b.WriteString("### Build Log (tail)\n\n")
		b.WriteString("```\n")

		logContent := logTail
		// Budget: leave room for headers and the closing fence.
		budget := MaxCheckTextOutput - b.Len() - 10
		if budget <= 0 {
			logContent = "(output too large)"
		} else if len(logContent) > budget {
			logContent = logContent[len(logContent)-budget:]
			if idx := strings.IndexByte(logContent, '\n'); idx >= 0 {
				logContent = logContent[idx+1:]
			}
		}

		b.WriteString(logContent)
		if !strings.HasSuffix(logContent, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n")
	}

	// Final truncation to 64KB.
	text := b.String()
	if len(text) > MaxCheckTextOutput {
		text = text[:MaxCheckTextOutput]
	}

	return &CheckRunOutput{
		Title:   truncateString(title, 256),
		Summary: truncateString(summary, MaxCheckTextOutput),
		Text:    text,
	}
}

// BuildSuccessOutput constructs a minimal CheckRunOutput for a successful build.
func BuildSuccessOutput() *CheckRunOutput {
	return &CheckRunOutput{
		Title:   "Build succeeded",
		Summary: "All steps passed.",
	}
}

// BuildCancelledOutput constructs a CheckRunOutput for a cancelled build.
func BuildCancelledOutput() *CheckRunOutput {
	return &CheckRunOutput{
		Title:   "Build cancelled",
		Summary: "Build was cancelled by a user.",
	}
}

// BuildTimedOutOutput constructs a CheckRunOutput for a timed-out build.
func BuildTimedOutOutput(timeout string) *CheckRunOutput {
	summary := "Build timed out."
	if timeout != "" {
		summary = fmt.Sprintf("Build timed out after %s.", timeout)
	}
	return &CheckRunOutput{
		Title:   "Build timed out",
		Summary: summary,
	}
}

// BuildInterruptedOutput constructs a CheckRunOutput for an interrupted build.
func BuildInterruptedOutput(detail string) *CheckRunOutput {
	summary := "Build was interrupted."
	if detail != "" {
		summary = fmt.Sprintf("Build was interrupted: %s", detail)
	}
	return &CheckRunOutput{
		Title:   "Build interrupted",
		Summary: summary,
	}
}

func truncateString(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max]
}

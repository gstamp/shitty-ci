package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const statusContext = "continuous-integration/shitty-ci"

// maxStatusDescription is GitHub's limit for commit status descriptions.
const maxStatusDescription = 140

type statusPayload struct {
	State       string `json:"state"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description"`
	Context     string `json:"context"`
}

// CommitStatusTargetURL returns a GitHub URL for the commit's "Checks" tab.
// The generic /commit/<sha> page often hides CI context; /checks surfaces status
// rows (including the description / failure text) much more readably.
func CommitStatusTargetURL(owner, repo, sha string) string {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	sha = strings.TrimSpace(sha)
	if owner == "" || repo == "" || sha == "" {
		return ""
	}
	// These segments come from our DB / git; refuse odd values rather than encoding.
	if strings.ContainsAny(owner, "/\\\r\n") || strings.ContainsAny(repo, "/\\\r\n") || strings.ContainsAny(sha, "/\\\r\n") {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/%s/commit/%s/checks", owner, repo, sha)
}

// PostStatus posts a commit status. state is pending|success|failure|error.
// targetURL is optional; when set it becomes the clickable "Details" link in the GitHub UI.
func PostStatus(token, owner, repo, sha, state, description, targetURL string) error {
	if token == "" {
		return fmt.Errorf("missing github_token in config")
	}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/statuses/%s", owner, repo, sha)
	body, _ := json.Marshal(statusPayload{
		State:       state,
		TargetURL:   strings.TrimSpace(targetURL),
		Description: sanitizeGitHubStatusDescription(description),
		Context:     statusContext,
	})
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
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
		return fmt.Errorf("github status %s: %s", resp.Status, buf.String())
	}
	return nil
}

func sanitizeOneLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.Join(strings.Fields(s), " ")
}

func truncateToRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// sanitizeGitHubStatusDescription trims, collapses whitespace/newlines to a
// single line, and truncates to maxStatusDescription runes so the API always
// accepts the payload.
func sanitizeGitHubStatusDescription(s string) string {
	return truncateToRunes(sanitizeOneLine(s), maxStatusDescription)
}

// StepFailureDescription builds a GitHub status description for a failed step.
// It keeps a stable " | shitty-ci logs <build-id>" suffix so the UI points at
// local logs even when stderr was streamed only to the build log file.
func StepFailureDescription(stepName, errText, buildID string) string {
	stepName = sanitizeOneLine(stepName)
	errText = sanitizeOneLine(errText)
	buildID = strings.TrimSpace(buildID)
	if stepName == "" {
		stepName = "step"
	}
	if errText == "" {
		errText = "(no error text)"
	}
	if buildID == "" {
		return truncateToRunes(stepName+": "+errText, maxStatusDescription)
	}

	suffix := " | shitty-ci logs " + buildID
	body := stepName + ": " + errText
	if utf8.RuneCountInString(body+suffix) <= maxStatusDescription {
		return body + suffix
	}

	budget := maxStatusDescription - utf8.RuneCountInString(suffix)
	if budget < 8 {
		return truncateToRunes(body+suffix, maxStatusDescription)
	}
	return truncateToRunes(body, budget) + suffix
}

// StatusDescriptionWithLogsHint appends a stable " | shitty-ci logs <build-id>" suffix when it fits
// within GitHub's commit status description limit.
func StatusDescriptionWithLogsHint(msg string, buildID string) string {
	msg = sanitizeOneLine(msg)
	buildID = strings.TrimSpace(buildID)
	if buildID == "" {
		return truncateToRunes(msg, maxStatusDescription)
	}

	suffix := " | shitty-ci logs " + buildID
	if utf8.RuneCountInString(msg+suffix) <= maxStatusDescription {
		return msg + suffix
	}

	budget := maxStatusDescription - utf8.RuneCountInString(suffix)
	if budget < 8 {
		return truncateToRunes(msg+suffix, maxStatusDescription)
	}
	return truncateToRunes(msg, budget) + suffix
}

// MapBuildState maps internal daemon state to GitHub status API state.
func MapBuildState(internal string) (gh string, desc string) {
	switch internal {
	case "running":
		return "pending", "Build in progress"
	case "success":
		return "success", "All steps passed"
	case "failure":
		return "failure", "A build step failed"
	case "timed_out":
		return "error", "Build timed out"
	case "cancelled":
		return "error", "Build cancelled by user"
	default:
		return "error", internal
	}
}

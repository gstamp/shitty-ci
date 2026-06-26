package github

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Manifest describes the GitHub App we want to create via the manifest flow.
// The manifest is base64-encoded and passed as a URL query parameter so that
// the GitHub App creation form is pre-filled with the correct settings.
type Manifest struct {
	Name          string              `json:"name"`
	URL           string              `json:"url"`
	HookAttributes *HookAttributes    `json:"hook_attributes,omitempty"`
	Public        bool                `json:"public,omitempty"`
	DefaultEvents []string            `json:"default_events,omitempty"`
	DefaultPermissions map[string]string `json:"default_permissions,omitempty"`
}

// HookAttributes controls the webhook settings for the GitHub App.
type HookAttributes struct {
	URL    string `json:"url,omitempty"`
	Active bool   `json:"active"`
}

// NewManifest creates a Manifest with sensible defaults for shitty-ci.
func NewManifest() *Manifest {
	return &Manifest{
		Name: "shitty-ci",
		URL:  "https://github.com",
		DefaultPermissions: map[string]string{
			"checks":   "write",
			"statuses": "write",
			"metadata": "read",
		},
	}
}

// ManifestURL returns the GitHub URL for creating the app via the manifest.
// The user opens this URL in a browser, reviews the pre-filled form, and
// clicks "Create GitHub App".
func (m *Manifest) ManifestURL() (string, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(body)
	encoded = url.QueryEscape(encoded)
	return "https://github.com/settings/apps/new?manifest=" + encoded, nil
}

// URLParamsURL returns a URL that pre-fills the GitHub App creation form
// using individual query parameters instead of a base64 manifest. This is
// more reliable because URL parameters survive login redirects, whereas
// the manifest parameter can be lost during a session redirect.
func (m *Manifest) URLParamsURL() string {
	params := url.Values{}
	params.Set("name", m.Name)
	params.Set("url", m.URL)
	params.Set("webhook_active", "false")
	for k, v := range m.DefaultPermissions {
		params.Set(k, v)
	}
	return "https://github.com/settings/apps/new?" + params.Encode()
}

// ManifestConversionResult holds the credentials returned by the manifest
// conversion API.
type ManifestConversionResult struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	PEM      string `json:"pem"`
	HTMLURL  string `json:"html_url"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// ConvertManifestCode exchanges a temporary code from the manifest flow for
// the app's permanent credentials (app ID, private key PEM, slug, etc.).
func ConvertManifestCode(code string) (*ManifestConversionResult, error) {
	apiURL := fmt.Sprintf("https://api.github.com/app-manifests/%s/conversions", code)
	req, err := http.NewRequest(http.MethodPost, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("manifest conversion %s: %s", resp.Status, buf.String())
	}

	var result ManifestConversionResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode manifest conversion: %w", err)
	}
	if result.PEM == "" {
		return nil, fmt.Errorf("manifest response missing private key")
	}
	return &result, nil
}

// Installation describes a GitHub App installation.
type Installation struct {
	ID   int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
}

// AppInfo describes a GitHub App's own metadata.
type AppInfo struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// GetApp returns metadata about the authenticated GitHub App.
// Requires a valid JWT (not an installation token).
func GetApp(appID int64, pemBytes []byte) (*AppInfo, error) {
	jwt, err := generateAppJWT(appID, pemBytes)
	if err != nil {
		return nil, fmt.Errorf("generate jwt: %w", err)
	}

	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/app", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("get app %s: %s", resp.Status, buf.String())
	}

	var result AppInfo
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode app info: %w", err)
	}
	return &result, nil
}

// ListInstallations lists all installations of a GitHub App.
// Requires a valid JWT (not an installation token) for the app.
func ListInstallations(appID int64, pemBytes []byte) ([]Installation, error) {
	jwt, err := generateAppJWT(appID, pemBytes)
	if err != nil {
		return nil, fmt.Errorf("generate jwt: %w", err)
	}

	apiURL := "https://api.github.com/app/installations"
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("list installations %s: %s", resp.Status, buf.String())
	}

	var result []Installation
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode installations: %w", err)
	}
	return result, nil
}

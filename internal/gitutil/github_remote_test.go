package gitutil

import "testing"

func TestParseGitHubRemote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in         string
		wantOwner  string
		wantName   string
		wantOK     bool
	}{
		{"git@github.com:acme/widget.git", "acme", "widget", true},
		{"git@github.com:acme/widget", "acme", "widget", true},
		{"ssh://git@github.com/acme/widget.git", "acme", "widget", true},
		{"https://github.com/acme/widget.git", "acme", "widget", true},
		{"HTTPS://GitHub.com/acme/widget", "acme", "widget", true},
		{"https://example.com/acme/widget.git", "", "", false},
		{"git@gitlab.com:acme/widget.git", "", "", false},
	}
	for _, tc := range cases {
		o, n, ok := ParseGitHubRemote(tc.in)
		if ok != tc.wantOK || o != tc.wantOwner || n != tc.wantName {
			t.Fatalf("ParseGitHubRemote(%q) = (%q,%q,%v) want (%q,%q,%v)", tc.in, o, n, ok, tc.wantOwner, tc.wantName, tc.wantOK)
		}
	}
}

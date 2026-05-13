package shittyyml

import (
	"strings"
	"testing"
)

func branchLegacy(globs ...string) *branchYAML {
	b := branchYAML{}
	b.legacy = append([]string(nil), globs...)
	return &b
}

func branchIE(include, exclude []string) *branchYAML {
	b := branchYAML{}
	b.include = append([]string(nil), include...)
	b.exclude = append([]string(nil), exclude...)
	b.useIncludeExclude = true
	return &b
}

func tagLegacy(globs ...string) *tagYAML {
	t := tagYAML{}
	t.legacy = append([]string(nil), globs...)
	return &t
}

func tagIE(include, exclude []string) *tagYAML {
	t := tagYAML{}
	t.include = append([]string(nil), include...)
	t.exclude = append([]string(nil), exclude...)
	t.useIncludeExclude = true
	return &t
}

func TestShouldBuildRefBranchesDefault(t *testing.T) {
	f := &File{}
	ok, err := f.ShouldBuildRef("refs/remotes/origin/main")
	if err != nil || !ok {
		t.Fatalf("default branches: ok=%v err=%v", ok, err)
	}
}

func TestShouldBuildRefBranchesFilter(t *testing.T) {
	f := &File{Branches: branchLegacy("main", "release/*")}
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"refs/remotes/origin/main", true},
		{"refs/remotes/origin/release/1.0", true},
		{"refs/remotes/origin/dev", false},
	} {
		ok, err := f.ShouldBuildRef(tc.ref)
		if err != nil {
			t.Fatalf("ref %q: %v", tc.ref, err)
		}
		if ok != tc.want {
			t.Fatalf("ref %q: got %v want %v", tc.ref, ok, tc.want)
		}
	}
}

func TestShouldBuildRefBranchesIncludeExclude(t *testing.T) {
	f := &File{Branches: branchIE([]string{"**"}, []string{"main", "staging", "production"})}
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"refs/remotes/origin/feature/foo", true},
		{"refs/remotes/origin/main", false},
		{"refs/remotes/origin/staging", false},
		{"refs/remotes/origin/production", false},
	} {
		ok, err := f.ShouldBuildRef(tc.ref)
		if err != nil {
			t.Fatalf("ref %q: %v", tc.ref, err)
		}
		if ok != tc.want {
			t.Fatalf("ref %q: got %v want %v", tc.ref, ok, tc.want)
		}
	}
}

func TestShouldBuildRefBranchesReleaseScenario(t *testing.T) {
	f := &File{Branches: branchIE([]string{"release/*"}, []string{"release/experimental-*"})}
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"refs/remotes/origin/release/1.0", true},
		{"refs/remotes/origin/release/experimental-foo", false},
		{"refs/remotes/origin/main", false},
	} {
		ok, err := f.ShouldBuildRef(tc.ref)
		if err != nil {
			t.Fatalf("ref %q: %v", tc.ref, err)
		}
		if ok != tc.want {
			t.Fatalf("ref %q: got %v want %v", tc.ref, ok, tc.want)
		}
	}
}

func TestShouldBuildRefBranchesIncludeExcludeEmptyInclude(t *testing.T) {
	f := &File{Branches: branchIE(nil, nil)}
	ok, err := f.ShouldBuildRef("refs/remotes/origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected no match with empty include")
	}
}

func TestShouldBuildRefBranchesLegacyEmptyStillMeansAll(t *testing.T) {
	f := &File{Branches: branchLegacy()}
	ok, err := f.ShouldBuildRef("refs/remotes/origin/anything")
	if err != nil || !ok {
		t.Fatalf("legacy empty: ok=%v err=%v", ok, err)
	}
}

func TestShouldBuildRefTagsDefaultOff(t *testing.T) {
	f := &File{}
	ok, err := f.ShouldBuildRef("refs/tags/v1.0.0")
	if err != nil || ok {
		t.Fatalf("tags default off: ok=%v err=%v", ok, err)
	}
}

func TestShouldBuildRefTagsOptIn(t *testing.T) {
	f := &File{Tags: tagLegacy("v*")}
	ok, err := f.ShouldBuildRef("refs/tags/v1.0.0")
	if err != nil || !ok {
		t.Fatalf("tags opt-in: ok=%v err=%v", ok, err)
	}
}

func TestShouldBuildRefTagsIncludeExclude(t *testing.T) {
	f := &File{Tags: tagIE([]string{"v*"}, []string{"v0.*"})}
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"refs/tags/v1.0.0", true},
		{"refs/tags/v0.9.0", false},
		{"refs/tags/wrong", false},
	} {
		ok, err := f.ShouldBuildRef(tc.ref)
		if err != nil {
			t.Fatalf("ref %q: %v", tc.ref, err)
		}
		if ok != tc.want {
			t.Fatalf("ref %q: got %v want %v", tc.ref, ok, tc.want)
		}
	}
}

func TestShouldBuildRefTagsLegacyEmptyMeansNone(t *testing.T) {
	f := &File{Tags: tagLegacy()}
	ok, err := f.ShouldBuildRef("refs/tags/v1.0.0")
	if err != nil || ok {
		t.Fatalf("tags legacy empty: ok=%v err=%v", ok, err)
	}
}

func TestParseBranchesYAMLModes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "map_empty",
			yaml:    "branches: {}\n",
			wantErr: "include is required",
		},
		{
			name:    "map_unknown_key",
			yaml:    "branches:\n  include: [\"*\"]\n  also: [x]\n",
			wantErr: "unknown key",
		},
		{
			name:    "invalid_glob",
			yaml:    "branches:\n  include: [\"[\"]\n",
			wantErr: "invalid glob",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tc.yaml + "\nsteps:\n  - name: x\n    run: \"true\"\n"))
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseBranchesYAMLRoundTrip(t *testing.T) {
	data := []byte(`
branches:
  include: ["**"]
  exclude: [main, staging]
tags:
  include: ["v*"]
  exclude: ["v0.*"]
steps:
  - name: x
    run: "true"
`)
	f, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := f.ShouldBuildRef("refs/remotes/origin/feature/x")
	if err != nil || !ok {
		t.Fatalf("feature branch: ok=%v err=%v", ok, err)
	}
	ok, err = f.ShouldBuildRef("refs/remotes/origin/main")
	if err != nil || ok {
		t.Fatalf("main: ok=%v err=%v", ok, err)
	}
	ok, err = f.ShouldBuildRef("refs/tags/v1.0.0")
	if err != nil || !ok {
		t.Fatalf("tag v1: ok=%v err=%v", ok, err)
	}
	ok, err = f.ShouldBuildRef("refs/tags/v0.1.0")
	if err != nil || ok {
		t.Fatalf("tag v0: ok=%v err=%v", ok, err)
	}
}

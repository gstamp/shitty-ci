package shittyyml

import "testing"

func TestShouldBuildRefBranchesDefault(t *testing.T) {
	f := &File{}
	ok, err := f.ShouldBuildRef("refs/remotes/origin/main")
	if err != nil || !ok {
		t.Fatalf("default branches: ok=%v err=%v", ok, err)
	}
}

func TestShouldBuildRefBranchesFilter(t *testing.T) {
	f := &File{Branches: []string{"main", "release/*"}}
	for _, tc := range []struct {
		ref string
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

func TestShouldBuildRefTagsDefaultOff(t *testing.T) {
	f := &File{}
	ok, err := f.ShouldBuildRef("refs/tags/v1.0.0")
	if err != nil || ok {
		t.Fatalf("tags default off: ok=%v err=%v", ok, err)
	}
}

func TestShouldBuildRefTagsOptIn(t *testing.T) {
	f := &File{Tags: []string{"v*"}}
	ok, err := f.ShouldBuildRef("refs/tags/v1.0.0")
	if err != nil || !ok {
		t.Fatalf("tags opt-in: ok=%v err=%v", ok, err)
	}
}

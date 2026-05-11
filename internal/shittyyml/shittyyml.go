package shittyyml

import (
	"fmt"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Step struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

type File struct {
	Branches     []string          `yaml:"branches"`
	Tags         []string          `yaml:"tags"`
	Steps        []Step            `yaml:"steps"`
	Env          map[string]string `yaml:"env"`
	BuildTimeout string            `yaml:"build_timeout"`
}

func Parse(data []byte) (*File, error) {
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// StepBuildTimeout returns per-file build timeout override, or zero if unset/invalid.
func (f *File) StepBuildTimeout() (time.Duration, error) {
	if f.BuildTimeout == "" {
		return 0, nil
	}
	return time.ParseDuration(f.BuildTimeout)
}

// ShouldBuildRef decides whether a ref (full git ref) should trigger builds given file rules.
func (f *File) ShouldBuildRef(ref string) (bool, error) {
	if strings.HasPrefix(ref, "refs/remotes/origin/") {
		branch := strings.TrimPrefix(ref, "refs/remotes/origin/")
		if branch == "HEAD" {
			return false, nil
		}
		if len(f.Branches) == 0 {
			return true, nil
		}
		return matchAnyGlob(branch, f.Branches)
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		tag := strings.TrimPrefix(ref, "refs/tags/")
		if len(f.Tags) == 0 {
			return false, nil
		}
		return matchAnyGlob(tag, f.Tags)
	}
	return false, nil
}

func matchAnyGlob(s string, globs []string) (bool, error) {
	for _, g := range globs {
		ok, err := path.Match(g, s)
		if err != nil {
			return false, fmt.Errorf("invalid glob %q: %w", g, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

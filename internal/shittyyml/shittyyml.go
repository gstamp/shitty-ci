package shittyyml

import (
	"fmt"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

type Step struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

// RefFilter selects remote branch names or tag names (after stripping ref prefixes).
// YAML may be either a sequence of doublestar globs (legacy OR semantics) or a mapping:
//
//	branches:
//	  include: ["*"]
//	  exclude: [main, staging]
//
// For branches, a legacy empty sequence means "all branches". For tags, a legacy empty
// sequence means "no tags" (same as omitting tags).
type RefFilter struct {
	legacy            []string
	include           []string
	exclude           []string
	useIncludeExclude bool
}

// branchYAML and tagYAML exist so YAML parse errors name the field (branches vs tags).
type branchYAML RefFilter

func (b *branchYAML) UnmarshalYAML(value *yaml.Node) error {
	return refFilterUnmarshal((*RefFilter)(b), value, "branches")
}

type tagYAML RefFilter

func (t *tagYAML) UnmarshalYAML(value *yaml.Node) error {
	return refFilterUnmarshal((*RefFilter)(t), value, "tags")
}

type File struct {
	Branches     *branchYAML       `yaml:"branches"`
	Tags         *tagYAML          `yaml:"tags"`
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

// refFilterUnmarshal accepts either a sequence of globs or a mapping with include (required) and exclude (optional).
func refFilterUnmarshal(r *RefFilter, value *yaml.Node, field string) error {
	if value == nil {
		return fmt.Errorf("%s: empty document", field)
	}
	n := resolveYAMLAlias(value)
	switch n.Kind {
	case yaml.SequenceNode:
		legacy, err := stringSeqFromYAML(n, field)
		if err != nil {
			return err
		}
		if err := validateGlobs(legacy); err != nil {
			return err
		}
		r.legacy = legacy
		r.useIncludeExclude = false
		return nil
	case yaml.MappingNode:
		if len(n.Content)%2 != 0 {
			return fmt.Errorf("%s: invalid mapping", field)
		}
		var (
			include, exclude []string
			haveInclude      bool
		)
		for i := 0; i < len(n.Content); i += 2 {
			kn := resolveYAMLAlias(n.Content[i])
			vn := resolveYAMLAlias(n.Content[i+1])
			if kn.Kind != yaml.ScalarNode {
				return fmt.Errorf("%s: mapping keys must be strings", field)
			}
			key := kn.Value
			switch key {
			case "include":
				if isYAMLNull(vn) {
					return fmt.Errorf("%s.include: must be a sequence of strings, not null", field)
				}
				sl, err := stringSeqFromYAML(vn, field+".include")
				if err != nil {
					return err
				}
				include = sl
				haveInclude = true
			case "exclude":
				if isYAMLNull(vn) {
					exclude = nil
					continue
				}
				sl, err := stringSeqFromYAML(vn, field+".exclude")
				if err != nil {
					return err
				}
				exclude = sl
			default:
				return fmt.Errorf("%s: unknown key %q (supported: include, exclude)", field, key)
			}
		}
		if !haveInclude {
			return fmt.Errorf("%s: include is required when using a mapping", field)
		}
		patterns := append(append([]string{}, include...), exclude...)
		if err := validateGlobs(patterns); err != nil {
			return err
		}
		r.include = include
		r.exclude = exclude
		r.useIncludeExclude = true
		return nil
	default:
		return fmt.Errorf("%s: expected sequence or mapping, got %s", field, yamlKindString(n.Kind))
	}
}

func resolveYAMLAlias(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

func isYAMLNull(n *yaml.Node) bool {
	if n == nil {
		return true
	}
	n = resolveYAMLAlias(n)
	if n.Kind == yaml.ScalarNode {
		return n.Tag == "!!null" || n.Value == "null" || n.Value == "~"
	}
	return false
}

func yamlKindString(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return fmt.Sprintf("kind(%d)", k)
	}
}

func stringSeqFromYAML(n *yaml.Node, ctx string) ([]string, error) {
	n = resolveYAMLAlias(n)
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: expected sequence of strings", ctx)
	}
	out := make([]string, 0, len(n.Content))
	for i, c := range n.Content {
		c = resolveYAMLAlias(c)
		if c.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s[%d]: expected string", ctx, i)
		}
		out = append(out, c.Value)
	}
	return out, nil
}

func validateGlobs(patterns []string) error {
	for _, p := range patterns {
		if !doublestar.ValidatePattern(p) {
			return fmt.Errorf("invalid glob %q", p)
		}
	}
	return nil
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
		if f.Branches == nil {
			return true, nil
		}
		return (*RefFilter)(f.Branches).match(branch, true)
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		tag := strings.TrimPrefix(ref, "refs/tags/")
		if f.Tags == nil {
			return false, nil
		}
		return (*RefFilter)(f.Tags).match(tag, false)
	}
	return false, nil
}

// legacyEmptyMeansAll: for branches, legacy empty list matches every branch; for tags, it matches none.
func (r *RefFilter) match(s string, legacyEmptyMeansAll bool) (bool, error) {
	if r.useIncludeExclude {
		in, err := matchAnyGlob(s, r.include)
		if err != nil {
			return false, err
		}
		if !in {
			return false, nil
		}
		ex, err := matchAnyGlob(s, r.exclude)
		if err != nil {
			return false, err
		}
		return !ex, nil
	}
	if len(r.legacy) == 0 {
		return legacyEmptyMeansAll, nil
	}
	return matchAnyGlob(s, r.legacy)
}

func matchAnyGlob(s string, globs []string) (bool, error) {
	for _, g := range globs {
		ok, err := doublestar.Match(g, s)
		if err != nil {
			return false, fmt.Errorf("invalid glob %q: %w", g, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

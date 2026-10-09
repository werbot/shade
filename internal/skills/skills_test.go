package skills_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/directive"
	"github.com/werbot/shade/internal/skills"
)

// skillKeys are the skills of the plugin and the paths they install to. The key is
// joined with the plugin directory by the installer, so it is a relative,
// slash-separated path ending in the file name — no leading slash, no trailing slash.
var skillKeys = []string{"skills/shade/SKILL.md", "skills/shade-rules/SKILL.md"}

func TestFilesCoverBothSkills(t *testing.T) {
	files := skills.Files()
	if len(files) != len(skillKeys) {
		t.Fatalf("got %d files, want exactly the %d skills: %v", len(files), len(skillKeys), keys(files))
	}
	for _, key := range skillKeys {
		body, ok := files[key]
		if !ok {
			t.Fatalf("missing %s, got %v", key, keys(files))
		}
		if len(body) == 0 {
			t.Errorf("%s is empty", key)
		}
	}
}

func TestSkillFrontmatterNamesMatchTheDirectory(t *testing.T) {
	files := skills.Files()
	for _, key := range skillKeys {
		dir := strings.Split(key, "/")[1]
		meta := frontmatter(t, key, string(files[key]))
		if meta["name"] != dir {
			t.Errorf("%s: name %q, want the directory name %q", key, meta["name"], dir)
		}
		if meta["description"] == "" {
			t.Errorf("%s: the frontmatter has no description", key)
		}
	}
}

// §8: a skill points at the directive, it does not carry a second copy of it. A
// copy would drift from internal/directive, and the model would then follow
// whichever text it happened to read last.
func TestSkillsDoNotDuplicateTheDirective(t *testing.T) {
	for key, body := range skills.Files() {
		if strings.Contains(string(body), directive.Text()) {
			t.Errorf("%s quotes the directive verbatim; refer to it in words instead", key)
		}
	}
}

// frontmatter returns the YAML block between the --- fences as a flat map. The block
// holds two scalar keys, so a YAML package would be more code than this reader.
func frontmatter(t *testing.T, key, body string) map[string]string {
	t.Helper()
	rest, ok := strings.CutPrefix(body, "---\n")
	if !ok {
		t.Fatalf("%s: the file must open with a frontmatter fence", key)
	}
	block, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		t.Fatalf("%s: the frontmatter fence is not closed", key)
	}
	meta := make(map[string]string)
	for _, line := range strings.Split(block, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		meta[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return meta
}

func keys(files map[string][]byte) []string {
	return slices.Sorted(maps.Keys(files))
}

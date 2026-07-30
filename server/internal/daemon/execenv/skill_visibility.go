package execenv

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// modelVisibleSkills returns the skills a model may invoke, with Name
// normalized to the on-disk slug.
//
// The normalization is not cosmetic. Every caller renders these entries into a
// listing the model reads to pick a skill, and the only name it can actually
// invoke is the directory slug writeSkillFiles lays down — sanitizeSkillName of
// the same field. A workspace skill's Name is its human display name ("PR
// review"), so listing it verbatim hands the model an identifier that does not
// resolve (MUL-5529). Normalizing here rather than at each call site keeps the
// four listings (runtime brief + the three issue_context renderers) from
// drifting apart, and is idempotent because sanitizeSkillName is.
//
// Known gap: this yields the natural slug, but writeSkillFiles falls back to
// `<slug>-multica` when a user-installed skill already occupies the directory
// (allocateCollisionFreeSkillDir). The listing then names the user's skill
// rather than Multica's. Closing that needs the allocated slug threaded back
// from Prepare, which these renderers deliberately cannot reach — they are pure
// so the brief stays byte-identical across runs. Tracked separately.
func modelVisibleSkills(skills []SkillContextForEnv) []SkillContextForEnv {
	if len(skills) == 0 {
		return nil
	}
	visible := make([]SkillContextForEnv, 0, len(skills))
	for _, skill := range skills {
		if skillModelInvocationVisible(skill) {
			skill.Name = sanitizeSkillName(skill.Name)
			visible = append(visible, skill)
		}
	}
	return visible
}

func skillModelInvocationVisible(skill SkillContextForEnv) bool {
	return !skillDisablesModelInvocation(skill.Content)
}

func skillDisablesModelInvocation(content string) bool {
	fmBody, _, ok := frontmatterParts(content)
	if !ok || strings.TrimSpace(fmBody) == "" {
		return false
	}
	var data map[string]any
	if err := yaml.Unmarshal([]byte(fmBody), &data); err != nil {
		return false
	}
	value, ok := data["disable-model-invocation"]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

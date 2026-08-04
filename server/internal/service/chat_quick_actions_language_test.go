package service

import (
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The reported failure: an all-English conversation whose pills came back in
// Chinese. The server must close the prompt with a directive that rules that
// out, without having to identify which Latin-script language it is.
func TestChatQuickActionsLanguageDirectiveRulesOutCJKForLatinWriting(t *testing.T) {
	out := chatQuickActionsLanguageDirective("", "Can you get Claude Engineer to review this PR for simplifications")

	if !strings.Contains(out, "Latin alphabet") {
		t.Fatalf("Latin-script writing must be named as such:\n%s", out)
	}
	if !strings.Contains(out, "Never emit Chinese, Japanese, or Korean characters.") {
		t.Fatalf("Latin-script writing must forbid CJK output:\n%s", out)
	}
}

// A stored preference is a statement of intent and outranks whatever the
// conversation happens to look like.
func TestChatQuickActionsLanguageDirectivePrefersStoredPreference(t *testing.T) {
	out := chatQuickActionsLanguageDirective("en", "帮我看下这个 PR 的 diff")

	if !strings.Contains(out, "OUTPUT LANGUAGE: English.") {
		t.Fatalf("stored preference must win over the conversation script:\n%s", out)
	}
	if strings.Contains(out, "Chinese,") {
		t.Fatalf("detection must not run when a preference is set:\n%s", out)
	}
}

func TestChatQuickActionsLanguageDirectiveNamesEachSupportedPreference(t *testing.T) {
	for preference, want := range map[string]string{
		"en":      "OUTPUT LANGUAGE: English.",
		"zh-Hans": "OUTPUT LANGUAGE: Simplified Chinese.",
		"ja":      "OUTPUT LANGUAGE: Japanese.",
		"ko":      "OUTPUT LANGUAGE: Korean.",
	} {
		out := chatQuickActionsLanguageDirective(preference, "")
		if !strings.Contains(out, want) {
			t.Fatalf("preference %q must render %q:\n%s", preference, want, out)
		}
	}
}

// The column has no CHECK constraint, so a legacy or hand-written value can be
// anything. An unrecognized one must fall through to detection rather than be
// echoed into the prompt as a language name.
func TestChatQuickActionsLanguageDirectiveIgnoresUnrecognizedPreference(t *testing.T) {
	out := chatQuickActionsLanguageDirective("zh_CN", "안녕하세요 이 PR 좀 봐주세요")

	if strings.Contains(out, "zh_CN") {
		t.Fatalf("an unrecognized preference must never reach the prompt:\n%s", out)
	}
	if !strings.Contains(out, "OUTPUT LANGUAGE: Korean.") {
		t.Fatalf("an unrecognized preference must fall through to detection:\n%s", out)
	}
}

// Simplified and Traditional share the script, so detection cannot pick one.
// Naming a variant here would rewrite half the Chinese-writing audience's pills.
func TestChatQuickActionsLanguageDirectiveLeavesChineseVariantToTheUser(t *testing.T) {
	out := chatQuickActionsLanguageDirective("", "帮我看下这个 PR 的 diff")

	if !strings.Contains(out, "same variant (Simplified or Traditional)") {
		t.Fatalf("detected Han must defer the variant to the user:\n%s", out)
	}
	if strings.Contains(out, "OUTPUT LANGUAGE: Simplified Chinese") {
		t.Fatalf("detection must not pin a Chinese variant:\n%s", out)
	}
}

// No preference and no script evidence: say only what is known. Banning CJK
// from a conversation the server cannot read would break the very users this
// change is meant to leave alone.
func TestChatQuickActionsLanguageDirectiveStaysSilentWithoutEvidence(t *testing.T) {
	out := chatQuickActionsLanguageDirective("", "   \n😀\n")

	if !strings.Contains(out, "OUTPUT LANGUAGE: the language of the [user] turns above.") {
		t.Fatalf("no evidence must fall back to the conversation:\n%s", out)
	}
	if strings.Contains(out, "Never emit") {
		t.Fatalf("no evidence must not forbid any script:\n%s", out)
	}
}

func TestDetectChatQuickActionsScript(t *testing.T) {
	cases := []struct {
		name string
		text string
		want chatQuickActionsScript
	}{
		{"english", "why is this slow", chatQuickActionsScriptLatin},
		{"chinese", "帮我看下这个 PR 的 diff", chatQuickActionsScriptHan},
		{"japanese", "このPRをレビューしてください", chatQuickActionsScriptKana},
		{"korean", "이 PR 좀 봐주세요", chatQuickActionsScriptHangul},
		{"empty", "", chatQuickActionsScriptUnknown},
		{"emoji only", "🎉🎉", chatQuickActionsScriptUnknown},
		// Non-CJK, non-Latin writing is Unknown, not Latin: the CJK ban that
		// rides on Latin is safe there too, but claiming a script the detector
		// did not see would be a lie the directive is built on.
		{"cyrillic", "почему это так медленно", chatQuickActionsScriptUnknown},
		// A single quoted CJK term inside an English sentence must not flip the
		// answer — this is the everyday case of an English user pasting a name.
		{"english quoting a chinese filename", "Please rename the 报告 file to something ASCII", chatQuickActionsScriptLatin},
		// The mirror case: Chinese prose carrying English identifiers is still
		// Chinese, because CJK runs are dense.
		{"chinese quoting english identifiers", "这个 GenerateChatQuickActionsForTask 的超时设置有问题", chatQuickActionsScriptHan},
		// Kana settles Japanese even when kanji outnumber it; Han alone never
		// outvotes an exclusive script.
		{"japanese with heavy kanji", "認証設定を確認して", chatQuickActionsScriptKana},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectChatQuickActionsScript(tc.text); got != tc.want {
				t.Fatalf("detectChatQuickActionsScript(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// An agent that answers in a different language than it was asked in is the
// case the directive exists to survive, so its text must not get a vote.
func TestChatQuickActionsUserTextIgnoresAgentReplies(t *testing.T) {
	msgs := []db.ChatMessage{
		chatMsg("user", "review this PR for simplifications"),
		chatMsg("assistant", "已创建工单 EFF-359，并分配给了 Claude Engineer。"),
	}

	text := chatQuickActionsUserText(msgs)
	if strings.Contains(text, "已创建工单") {
		t.Fatalf("agent replies must be excluded from the language signal: %q", text)
	}
	if detectChatQuickActionsScript(text) != chatQuickActionsScriptLatin {
		t.Fatalf("an English user with a Chinese-replying agent must still read as Latin: %q", text)
	}
}

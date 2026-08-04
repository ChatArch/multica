package service

import (
	"fmt"
	"strings"
	"unicode"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The suggestion pass used to leave its output language entirely to the model,
// with one sentence in the system prompt asking it to match the user. An
// inferred constraint is not a constraint: the same English conversation would
// come back with English pills on one turn and Chinese pills on the next, which
// is what users report as "quick actions sometimes aren't in English"
// (MUL-5689). Worse, the drift is sticky — ALREADY SUGGESTED replays the
// previous turn's labels, so one Chinese pass seeds the next.
//
// So the server decides the language and states it as a directive the model
// only has to obey. The decision is made from the strongest signal available:
//
//  1. The user's stored UI language preference, when they have set one.
//  2. Otherwise the script their own turns are written in.
//  3. Otherwise nothing new — the old infer-from-conversation wording, which is
//     still the honest answer when the server has no signal at all.

// chatQuickActionsLanguageNames maps a stored `"user".language` value to the
// language named in the directive. Keys mirror supportedLanguages in
// server/internal/handler/auth.go (and SUPPORTED_LOCALES in
// packages/core/i18n/types.ts). A legacy or unrecognized value falls through to
// script detection rather than pinning output to a language nobody asked for.
var chatQuickActionsLanguageNames = map[string]string{
	"en":      "English",
	"zh-Hans": "Simplified Chinese",
	"ja":      "Japanese",
	"ko":      "Korean",
}

type chatQuickActionsScript int

const (
	chatQuickActionsScriptUnknown chatQuickActionsScript = iota
	chatQuickActionsScriptLatin
	chatQuickActionsScriptHan
	chatQuickActionsScriptKana
	chatQuickActionsScriptHangul
)

// chatQuickActionsCJKRatio is how much CJK text it takes to call a mixed
// message CJK: the CJK run must be at least a quarter the length of the Latin
// run. CJK writing is far denser per character, so a genuine Chinese, Japanese,
// or Korean turn clears this easily even when it quotes English identifiers,
// while an English turn that happens to mention one CJK file name does not.
const chatQuickActionsCJKRatio = 4

// detectChatQuickActionsScript classifies the script a stretch of user writing
// is in. It answers a narrower question than language identification, which is
// deliberate: script is decidable from the runes themselves, so the answer is
// deterministic and testable, and the ambiguity it cannot resolve (Han is
// shared by Chinese and kana-free Japanese) is handled by what the directive
// says rather than by guessing.
//
// Latin is a real answer, not a fallback: knowing the user does NOT write in a
// CJK script is enough to rule out the failure this whole file exists for,
// without having to identify which Latin-script language it is. A message with
// neither Latin nor CJK (Cyrillic, Arabic, emoji only, or empty) is Unknown,
// and Unknown must stay silent — pinning a language from no evidence would
// break the very conversations this is meant to protect.
func detectChatQuickActionsScript(text string) chatQuickActionsScript {
	var latin, han, kana, hangul int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Hangul, r):
			hangul++
		case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
			kana++
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.Is(unicode.Latin, r):
			latin++
		}
	}

	cjk := han + kana + hangul
	if cjk == 0 || cjk*chatQuickActionsCJKRatio < latin {
		if latin == 0 {
			return chatQuickActionsScriptUnknown
		}
		return chatQuickActionsScriptLatin
	}
	// Kana and Hangul are exclusive to Japanese and Korean, so either one
	// settles it outright. Bare Han is the only genuinely ambiguous case and is
	// reported as Han, not as a language.
	switch {
	case hangul > 0:
		return chatQuickActionsScriptHangul
	case kana > 0:
		return chatQuickActionsScriptKana
	default:
		return chatQuickActionsScriptHan
	}
}

// chatQuickActionsUserText joins the user's own turns from the context window.
// The agent's replies are excluded on purpose: an agent replying in a different
// language than it was addressed in is precisely the case the directive has to
// survive, so letting its text vote would reintroduce the bug.
func chatQuickActionsUserText(msgs []db.ChatMessage) string {
	var b strings.Builder
	for _, msg := range msgs {
		if msg.Role != "user" {
			continue
		}
		b.WriteString(msg.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// chatQuickActionsLanguageDirective renders the OUTPUT LANGUAGE block that
// closes the pass's user message. preference is the raw `"user".language`
// value ("" when unset), userText is the user's own turns from the window.
//
// Only the preference path names a Chinese variant, because "zh-Hans" says
// which one. Detected Han does not: Simplified and Traditional share the
// script, so the directive asks for the user's own variant instead of picking
// one and rewriting half the audience's pills.
func chatQuickActionsLanguageDirective(preference, userText string) string {
	if name, ok := chatQuickActionsLanguageNames[strings.TrimSpace(preference)]; ok {
		return fmt.Sprintf("OUTPUT LANGUAGE: %s.\n"+
			"Write every \"label\" and \"prompt\" in %s, whatever language appears above.", name, name)
	}

	switch detectChatQuickActionsScript(userText) {
	case chatQuickActionsScriptHangul:
		return "OUTPUT LANGUAGE: Korean.\n" +
			"Write every \"label\" and \"prompt\" in Korean, whatever language appears above."
	case chatQuickActionsScriptKana:
		return "OUTPUT LANGUAGE: Japanese.\n" +
			"Write every \"label\" and \"prompt\" in Japanese, whatever language appears above."
	case chatQuickActionsScriptHan:
		return "OUTPUT LANGUAGE: Chinese, in the same variant (Simplified or Traditional) the user writes in.\n" +
			"Write every \"label\" and \"prompt\" in Chinese, whatever language appears above."
	case chatQuickActionsScriptLatin:
		return "OUTPUT LANGUAGE: the language of the [user] turns above, which is written in the Latin alphabet.\n" +
			"Write every \"label\" and \"prompt\" in that language. Never emit Chinese, Japanese, or Korean characters."
	default:
		return "OUTPUT LANGUAGE: the language of the [user] turns above.\n" +
			"Write every \"label\" and \"prompt\" in that language, whatever language appears above."
	}
}

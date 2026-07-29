-- Drop the generated emoji avatars (MUL-5399).
--
-- Agents created without an uploaded avatar used to be stamped with a random
-- `emoji:<glyph>` sentinel at CreateAgent time. That default is gone: an agent
-- with no avatar now renders the icon of the runtime it is bound to, derived at
-- render time so it stays correct when the agent is rebound.
--
-- The `emoji:` prefix ALONE is not proof that a row was machine-generated.
-- `acceptAvatarURL` deliberately passes foreign avatar values through untouched
-- (emoji markers, data: URIs, third-party profile URLs — see
-- TestAcceptAvatarURL_PassesThroughForeignValues), so a client that PUT an
-- explicit `avatar_url: "emoji:X"` got it stored verbatim. Prefix-matching alone
-- would destroy those, and the down migration cannot restore them.
--
-- So this matches on two independent provenance signals instead, and clears a
-- row only when BOTH hold:
--
--   1. The value is one of the exact 24 glyphs the generator could emit. It
--      never produced anything else, so any other emoji value is a client's.
--   2. The agent was created at or after the generator shipped (c6fb2c5c5,
--      2026-07-22 08:38:50 UTC). Before that commit no code path could write an
--      emoji avatar, so every earlier one is provably a client's.
--
-- What remains ambiguous is narrow: a client that, inside this window, chose one
-- of those exact 24 glyphs. Everything outside it is preserved.
--
-- Not clearing these at all is a defensible alternative — an un-cleared value
-- fails to load as an image and the UI falls back to the runtime icon anyway, so
-- this migration is data hygiene, not a correctness fix. It runs because the
-- residual risk above is smaller than leaving a dead sentinel in the column
-- permanently, with no later opportunity to tell the two apart.
UPDATE agent
SET avatar_url = NULL
WHERE created_at >= TIMESTAMPTZ '2026-07-22 08:38:50+00'
  AND avatar_url IN (
    'emoji:🐙', 'emoji:🦊', 'emoji:🦉', 'emoji:🐝',
    'emoji:🐼', 'emoji:🐸', 'emoji:🐯', 'emoji:🦁',
    'emoji:🐨', 'emoji:🐵', 'emoji:🐧', 'emoji:🐳',
    'emoji:🦋', 'emoji:🌞', 'emoji:🌙', 'emoji:⭐',
    'emoji:🔥', 'emoji:⚡', 'emoji:🍀', 'emoji:🌈',
    'emoji:🚀', 'emoji:🤖', 'emoji:👾', 'emoji:🧠'
  );

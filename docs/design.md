# Multica Design System

This document defines Multica's visual language and interaction conventions. All UI work follows it.

> 中文版本：[`design.zh.md`](./design.zh.md)。

---

## 1. Design philosophy

Three core principles:

1. **Restraint reads as quality.** Subtract by default. Every element must earn its place — a redundant divider, a decorative icon, a "just in case" hint is noise. Whitespace is itself a design decision.
2. **Hierarchy comes from grey; colour is a signal.** The interface is predominantly neutral. Colour appears only when it carries meaning (status, brand, error). If two regions compete for attention, the fix is to push one back, not to colour both.
3. **Consistency beats personality.** The same class of interaction must produce the same visual feedback. A hover effect should "feel identical" in the sidebar, in a dropdown, and on a table row. That consistency comes from tokens, never from hardcoded values.

---

## 2. Colour system

Built on the OKLCh colour space and exposed as CSS variables. All colours use shadcn tokens; **hardcoded Tailwind colour values are forbidden** (`text-gray-500`, `bg-blue-600`, and the like).

### 2.1 Surface hierarchy

The surface system describes the relationship *between containers* — it is not a licence to wrap every block of content in a card. Base tokens live in `packages/ui/styles/tokens.css` and cover light and dark mode together.

| Level | Token / class | Use for | Do not use for |
|---|---|---|---|
| App shell | `app-shell` / `bg-app-shell` | Outermost window chrome; the gutter between sidebar and page canvas | Page body, form groups |
| Page canvas | `page-canvas` / `bg-page-canvas` | The page body; continuously scrolling regions such as lists, boards, chat | Standalone settings groups, dialogs |
| Surface / card | `surface`, `surface-border`, `--surface-shadow` | Form groups, settings groups, and summary cards that need their own boundary | Every list row, every board column, whole-page body copy |
| Floating surface | `surface-raised`, `--floating-shadow` | Dialog, dropdown, popover, sheet, floating chat | Persistent page layout |

Rules:

- Page canvas is the default content plane. When you need grouping, reach for spacing and dividers first; use a surface only for a self-contained action or information block.
- In light mode, `app-shell` and the sidebar use `#f3f3f4`, `page-canvas` uses `#fbfbfb`, and cards use `#ffffff`. Never rely on a border alone to separate a settings group from the page background. The three together form a restrained, Linear-like hierarchy that brightens from the outside in.
- Cards use `bg-surface border-surface-border shadow-[var(--surface-shadow)]`; floating layers use `bg-surface-raised ring-surface-border shadow-[var(--floating-shadow)]`.
- `surface-hover` means the pointer is passing over. `surface-selected` means a persistent selection and stays a neutral grey — do not layer brand colour on top. A selected item that is hovered must keep `surface-selected`; it must not fall back to the hover state.
- Focus always uses a `focus-visible` ring. Never substitute a shadow, a size change, or a large brand fill for keyboard focus.
- Do not hand-write a parallel set of classes for light and dark mode. The base surface tokens already resolve per theme and sync native controls through `color-scheme`.

### 2.2 The neutral ramp

Neutrals cover 90% of the interface. Grey level *is* information hierarchy:

| Role | Light token | Dark token | Use for |
|---|---|---|---|
| Background | `page-canvas` / `background` | `page-canvas` / `background` | Page body |
| Card / floating layer | `surface` / `surface-raised` | `surface` / `surface-raised` | Bounded content groups and transient overlays |
| Secondary surface | `muted` / `secondary` | `muted` / `secondary` | Hover backgrounds, tag fills |
| Border | `border` | `border` | Dividers, input borders |
| Input border | `input` | `input` | Slightly heavier than `border` |
| Primary text | `foreground` | `foreground` | Headings, body copy |
| Secondary text | `muted-foreground` | `muted-foreground` | Descriptions, metadata, placeholders |
| Strongest text | `primary` | `primary` | Button labels (inverted), key tags |

**Rule:** within one screen, use at most three text-colour levels (`foreground` / `muted-foreground` / one semantic colour). More than three means the hierarchy itself is wrong.

### 2.3 Semantic colours

Colour conveys meaning; it never decorates:

| Token | Meaning | Where it appears |
|---|---|---|
| `brand` | Brand identity | Logo, brand buttons, a very small amount of emphasis |
| `destructive` | Danger / error | Delete buttons, form validation errors, destructive actions |
| `success` | Success | Status tags (done, resolved) |
| `warning` | Warning | Attention states, due-date reminders |
| `info` | Information | Hints, links, secondary markers |
| `priority` | Priority | High-priority tags |

**Rules:**

- Semantic colours belong on small elements (badge, icon, border). For a large fill, use a 10–20% alpha variant of the same colour (e.g. `bg-destructive/10`).
- No more than two or three semantic colours on screen at once. If an interface shows red, yellow, green, blue, and purple simultaneously, the information density is too high and the content needs reorganising.

### 2.4 Dark mode

Dark mode is not an inversion. It is a separately designed palette:

- Backgrounds use a deep grey (`oklch(0.18 ...)`), not pure black — pure black is harsh on LCD panels.
- Borders use `oklch(1 0 0 / 10%)` (white at 10% alpha), subtler than in light mode.
- Semantic colours are lifted in dark mode (e.g. `success` from `0.55` to `0.65`) to hold contrast.
- Every UI change must be verified in both modes.

---

## 3. Typography

### 3.1 Font families

| Role | Font | Use for |
|---|---|---|
| Body / UI | Inter (`--font-sans`) | Default for all interface text; CJK characters fall back automatically to the system font (PingFang SC / Microsoft YaHei / Noto Sans CJK SC) |
| Code / data | Geist Mono (`--font-mono`) | Code blocks, IDs, timestamps, monospaced data |
| Headings | `--font-heading` (= `--font-sans`) | Page and section headings |

The font stack is declared in two places — `apps/web/app/layout.tsx` and `apps/desktop/src/renderer/src/globals.css`. Keep them in sync.

### 3.2 Type-size discipline

**The entire project uses three core sizes plus one special case:**

| Tailwind class | Size | Role | Where |
|---|---|---|---|
| `text-base` (16px) | Body | Page titles, primary content | Page titles, editor body, empty-state copy |
| `text-sm` (14px) | Default | The workhorse size | Menu items, buttons, forms, list rows, body copy |
| `text-xs` (12px) | Supporting | Metadata, tags | Badge text, timestamps, status bar, secondary information |
| `text-[0.8rem]` | Transitional | `sm` buttons only | Reserved for shadcn `button size="sm"` |

**Forbidden:**

- `text-lg`, `text-xl`, `text-2xl`, and friends — a task-management tool optimises for information density and does not need large type.
- Arbitrary pixel values such as `text-[11px]` or `text-[13px]` — stay on Tailwind's built-in scale.
- More than two sizes inside one block. If you need a third size to express hierarchy, try `font-medium` vs `font-normal`, or `text-muted-foreground`, first.

### 3.3 Font weight

Two weights only:

| Weight | Use for |
|---|---|
| `font-normal` (400) | Body copy, descriptions, most text |
| `font-medium` (500) | Labels, buttons, navigation items, headings, selected state |

**`font-bold` and `font-semibold` are forbidden.** A density-oriented tool needs to feel light, and heavy weights break the rhythm of the hierarchy. When you need stronger emphasis, use a larger size or the `foreground` colour — not more weight.

---

## 4. Spacing

Built on Tailwind's 4px base grid. Spacing carries information: it is not "what looks nice", it tells the user what belongs to what.

### 4.1 Spacing semantics

| Spacing | Tailwind | Meaning |
|---|---|---|
| 4px | `gap-1` / `p-1` | **Tightly bound** — icon and label, label and value |
| 6px | `gap-1.5` / `p-1.5` | **Inside a component** — button padding, list-row spacing |
| 8px | `gap-2` / `p-2` | **Sibling items in a group** — between form fields, between list rows |
| 12px | `gap-3` / `p-3` | **Within a section** — card padding |
| 16px | `gap-4` / `p-4` | **Between groups** — separating distinct blocks |
| 24px | `gap-6` / `p-6` | **Between major sections** — top-level page regions |

**Rule: if you need a divider, your spacing is too tight.** Prefer increasing spacing over adding a `<Separator />`. A divider is the last resort.

### 4.2 Container strategy, in order of preference

When two regions need visual separation:

1. **Spacing alone** — increase the gap (preferred)
2. **A single divider** — one hairline, `border-border`
3. **A background shift** — give one region `bg-surface-hover` or `bg-surface`
4. **A full card** — border + radius + padding (heaviest)

Use the lightest tool that achieves the separation.

---

## 5. Interaction states

This is the core of visual consistency. Every state must look the same across every component.

### 5.1 State progression

```
rest → hover → active/pressed → selected/active → focused → disabled
```

### 5.2 Hover

Hover says "I noticed you". The change should be slight and immediate:

| Element | Hover effect | Token |
|---|---|---|
| List row / menu item | Background lightens to grey | `hover:bg-muted` |
| Ghost button | Grey background + text to foreground | `hover:bg-muted hover:text-foreground` |
| Secondary button | Background darkens 20% | `hover:bg-secondary/80` |
| Primary button | Background darkens 20% | `hover:bg-primary/80` |
| Text link | Underline appears | `hover:underline` |
| Tab | Text goes from secondary to primary | `hover:text-foreground` (from `text-muted-foreground`) |
| Icon button | Grey background | `hover:bg-muted` |
| Destructive button | Background alpha deepens | `hover:bg-destructive/20` |

**Rules:**

- Hover never changes size (no `scale`) and never adds a shadow.
- A hover background is always lighter than selected/active, so the user can tell "hovering" from "selected".
- Use `transition-colors`, `transition-shadow`, or an explicit property list — never `transition-all`. Duration comes from Tailwind's default (150ms); do not customise it.

### 5.3 Active / selected

Active says "I am the chosen one". It is visually heavier than hover:

| Element | Active effect | Token |
|---|---|---|
| Sidebar menu item | Background + heavier text + `font-medium` | `data-active:bg-sidebar-accent data-active:font-medium` |
| Tab | Underline indicator + foreground text + `font-medium` | `data-[state=active]:text-foreground` |
| Selected list row | Deeper background | `bg-muted` or `bg-accent` |
| Toggle (on) | Inverted background | `data-[state=on]:bg-primary data-[state=on]:text-primary-foreground` |

**The key distinction:** hover = `bg-muted`; active = `bg-muted` + `font-medium` + `text-foreground`. Active always adds a visual dimension beyond a darker background — weight or colour.

### 5.3.1 Active must survive hover

This is the most common source of bugs: the user hovers an already-selected item, the hover style overrides the active style, and the selection appears to "flash back" to a plain hover — visually reading as a deselection.

**Principle: an active state must stay identifiable at all times, including while hovered.**

Three ways to achieve it:

**Option 1 — express active on a dimension hover does not touch.**

If hover only changes the background, express active through weight and text colour. Even when the hover background lands on top, weight and colour are unchanged and the user still reads "this one is selected":

```
// ✅ hover owns the background; active owns weight and colour
hover:bg-muted                                       // hover: light grey background
data-active:font-medium data-active:text-foreground  // active: weight + colour (hover cannot override)
```

**Option 2 — an explicit active+hover compound style.**

When active also uses a background colour, define the "active and hovered" compound state explicitly so hover cannot drag the active background back down a level:

```tsx
// ✅ compound state handled explicitly
cn(
  "hover:bg-muted/50",                                // plain hover
  "data-active:bg-muted data-active:text-foreground", // active
  "data-active:hover:bg-muted"                        // active+hover: hold the active background, no downgrade
)
```

```tsx
// ❌ anti-pattern: hover overrides active
cn(
  "hover:bg-muted/50",           // hover background is lighter than active
  "data-active:bg-muted",        // active background
  // compound state unhandled → hovering an active row flashes from muted back to muted/50
)
```

**Option 3 — CSS selector specificity.**

Use `:not()` so hover only applies to non-active elements:

```
// ✅ hover does not apply to the active item
[data-active]:bg-muted [data-active]:text-foreground
not-data-active:hover:bg-muted/50
```

**How to check:** after writing any component with both hover and active states, verify by hand — select an item, move the pointer onto it and off again, and confirm nothing flickers or downgrades.

### 5.4 Pressed

Physical feedback — a tiny displacement on press:

```
active:not-aria-[haspopup]:translate-y-px
```

This 1px shift is configured globally on the shadcn button. It is excluded for buttons that open a popup, because the popup appears on release and the shift would flicker.

### 5.5 Focus

Focus serves keyboard navigation. Every interactive element uses:

```
focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50
```

- Use `focus-visible`, not `focus`, so a mouse click does not produce a focus ring.
- The ring uses the `ring` token (mid grey) and does not follow the component's own colour — it stays globally consistent.

### 5.6 Disabled

```
disabled:pointer-events-none disabled:opacity-50
```

Simple and uniform. Do not customise disabled styling per component.

### 5.7 Error / invalid

```
aria-invalid:border-destructive aria-invalid:ring-destructive/20
```

- Triggered by the `aria-invalid` attribute, which maps naturally onto form-validation libraries.
- Only the border and ring change; the background does not. Error messages render as inline text, never as a toast or alert banner.

---

## 6. Icons

### 6.1 Icon library

**Lucide React** (`lucide-react`) exclusively.

Do not mix in other icon libraries (Heroicons, Phosphor, etc.), and do not hand-roll SVG icons unless Lucide genuinely lacks a suitable one.

### 6.2 Icon sizing

Icon size is bound to component size:

| Component size | Icon size | Example |
|---|---|---|
| xs (`h-6`) | `size-3` (12px) | Compact buttons, icons inside a badge |
| sm (`h-7`) | `size-3.5` (14px) | Small buttons, compact lists |
| default (`h-8`) | `size-4` (16px) | Standard buttons, menu items, table actions |
| lg (`h-9`) | `size-4` (16px) | Large buttons (the icon does not need to grow) |

**Rules:**

- A standalone decorative icon (an empty-state illustration, say) is at most `size-8` (32px).
- Icons inherit the parent's text colour by default. To de-emphasise, use `text-muted-foreground`.
- Icon-to-text spacing: `gap-1` (xs) / `gap-1.5` (sm and default) / `gap-2` (loose arrangements).

### 6.3 Icon colour

- **Navigation / action icons:** `text-muted-foreground`, following the text to `text-foreground` on hover
- **Status icons:** the matching semantic colour (`text-success`, `text-destructive`, …)
- **Active-state icons:** `text-foreground`

---

## 7. Border radius

A dynamic scale derived from `--radius: 0.625rem` (10px):

| Token | Value | Use for |
|---|---|---|
| `rounded-sm` | 6px | Checkboxes, small tags |
| `rounded-md` | 8px | Inputs, small buttons, dropdown items |
| `rounded-lg` | 10px | Standard buttons, cards, dialogs |
| `rounded-xl` | 14px | Large cards, sheets |
| `rounded-full` | 999px | Avatars, pill badges |

**Hardcoded pixel values such as `rounded-[6px]` are forbidden** — the exception is shadcn internals that need a responsive calculation, e.g. `rounded-[min(var(--radius-md),12px)]`.

---

## 8. Motion

### 8.1 Principles

- **Fast and restrained.** Motion exists to help the user understand a change, not to show off.
- **Fade first.** Prefer an opacity transition over a slide when something appears or disappears.
- **No bounce.** No spring or bounce easing. Easing is uniformly `ease-out`.

### 8.2 Duration

| Case | Duration | Example |
|---|---|---|
| Colour / opacity change | 150ms | Hover background, text colour |
| Expand / collapse | 200ms | Accordion, collapsible |
| Overlay enter / exit | 150–200ms | Dialog, dropdown, popover |
| Page change | none | Route transitions have no animation |

### 8.3 Transitions in use

| Tailwind class | Use for |
|---|---|
| `transition-colors` | Pure colour change (hover, active) — preferred |
| `transition-all` | Several properties changing together |
| `transition-opacity` | Fading elements in and out |
| `transition-transform` | Displacement (the pressed effect) |

---

## 9. Component conventions

### 9.1 shadcn first

Prefer the already-installed shadcn components (55 available). For a new UI need:

1. Check whether shadcn has it → `npx shadcn add <component>`
2. Need a variant → extend the existing component with CVA
3. Genuinely absent → build your own, but follow the tokens and interaction states in this document

### 9.2 Button hierarchy

From strongest to weakest:

| Variant | Visual weight | Use for |
|---|---|---|
| `default` (primary) | ██████ | The page's primary action (at most one per screen) |
| `outline` | ████░░ | Secondary actions |
| `secondary` | ███░░░ | Supporting actions, toolbars |
| `ghost` | █░░░░░ | Icon buttons, inline actions, compact toolbars |
| `destructive` | ████░░ | Delete and other destructive actions (red) |
| `link` | █░░░░░ | Inline text links |

**Rule:** at most one primary button per view. Everything else uses a weaker variant. If several actions are equally important, make them all `outline` or all `secondary`.

### 9.3 Dropdown / popover

- Content width uses `w-auto`. **Fixed widths such as `w-52` or `w-56` are forbidden** — they force text to wrap.
- Menu items are uniformly `text-sm` with `size-4` icons.
- Mark the selected item with a checkmark icon or a leading indicator, not a background change.
- Destructive items use `text-destructive`, sit at the bottom, and are separated by a divider above.

### 9.4 Form inputs

- Inputs use a `border-input` border, switching to `border-ring` plus a ring on focus.
- Labels use `text-sm font-medium`.
- Description and help text use `text-xs text-muted-foreground`.
- Error messages use `text-xs text-destructive`, directly below the input.

---

## 10. Anti-patterns

The following are **forbidden** in the codebase:

| Forbidden | Why | Instead |
|---|---|---|
| Hardcoded colours `text-red-500`, `bg-gray-100` | Breaks theme consistency | Use tokens: `text-destructive`, `bg-muted` |
| Arbitrary values `text-[11px]`, `w-[137px]` | Escapes the design system | Use Tailwind's built-in scale |
| `font-bold` / `font-semibold` | Too heavy; breaks the light feel | `font-medium` + `text-foreground` |
| `text-lg` / `text-xl` / `text-2xl` | A density-oriented tool does not need large type | `text-base` is already the maximum |
| `shadow-sm` / `shadow-md` / `shadow-lg` | Skeuomorphic; conflicts with a flat design | Use `border` to separate levels |
| `scale-105` on hover | Jarring; conflicts with a restrained style | `hover:bg-muted` |
| Multi-colour gradient backgrounds | Decorative; distracting | A solid token colour |
| Skeleton loading | Does not match the minimal style | A spinner (`Loader2Icon animate-spin`) or inline loading text |
| A toast to confirm an action | Transient; easy to miss | Inline status text; reserve Sonner for errors and important notices |
| Fixed-width dropdown `w-52` | Text wrapping is uncontrollable | `w-auto` |
| Pure black background `#000` / `oklch(0 0 0)` | Harsh on LCD | Use the deep-grey `background` token in dark mode |

---

## 11. Checklist

Run through this before submitting any UI change:

- [ ] Are all colours tokens? Anything hardcoded?
- [ ] Are type sizes confined to `text-xs` / `text-sm` / `text-base`?
- [ ] Are weights confined to `font-normal` and `font-medium`?
- [ ] Is the hover state lighter than the active state?
- [ ] When an active item is hovered, does the active style stay identifiable (not overridden by hover)?
- [ ] Do icon sizes match their component sizes?
- [ ] Is spacing on Tailwind's built-in scale (no arbitrary values)?
- [ ] Does it work in dark mode?
- [ ] Are there unnecessary dividers that spacing could replace?
- [ ] Are dropdowns and popovers `w-auto`?
- [ ] Is there at most one primary button in the view?

import { readFileSync, readdirSync, statSync } from "node:fs";
import { resolve, join, relative } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * Can every piece of product text actually be read, and is hierarchy still
 * expressed with tokens rather than with alpha?
 *
 * The two questions belong in one guard because the answer to the first used to
 * depend on the second. `text-muted-foreground/70` reads as "muted, a bit
 * quieter"; on a light surface it is 2.69:1, which is not quiet, it is gone.
 * 152 call sites spread across /30 to /80 and every one of them failed AA in
 * light mode, because alpha was the only tool available for "weaker than
 * muted" — the palette had no step below it.
 *
 * So the palette got the step, and it is deliberately a NON-TEXT step:
 * --faint-foreground clears 3:1 (WCAG 1.4.11, for icons and glyphs) but not
 * 4.5:1, because there is no room for a third readable text tone. AA caps a
 * lighter text tone at L 0.523 on our darkest light surface and muted already
 * sits at 0.505 — a tier 0.018 apart is a tier nobody can see. Text has one
 * floor and it is --muted-foreground.
 *
 * The contrast assertions recompute from tokens.css rather than hard-coding
 * ratios, so editing a token value is what fails the test — which is the only
 * way a colour guard stays true. Ratios are WCAG 2.x relative luminance.
 */

const repoRoot = resolve(process.cwd(), "../..");
const tokensPath = resolve(repoRoot, "packages/ui/styles/tokens.css");

// ── colour maths ───────────────────────────────────────────────────────────

type Rgb = [number, number, number];

/** OKLCH -> linear sRGB -> gamma-encoded sRGB, clamped to gamut. */
function oklchToSrgb(l: number, c: number, hDeg: number): Rgb {
  const h = (hDeg * Math.PI) / 180;
  const a = c * Math.cos(h);
  const b = c * Math.sin(h);
  const lc = (l + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const mc = (l - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const sc = (l - 0.0894841775 * a - 1.291485548 * b) ** 3;
  const linear = [
    4.0767416621 * lc - 3.3077115913 * mc + 0.2309699292 * sc,
    -1.2684380046 * lc + 2.6097574011 * mc - 0.3413193965 * sc,
    -0.0041960863 * lc - 0.7034186147 * mc + 1.707614701 * sc,
  ];
  return linear.map((v) => {
    const x = Math.min(1, Math.max(0, v));
    return x <= 0.0031308 ? 12.92 * x : 1.055 * x ** (1 / 2.4) - 0.055;
  }) as Rgb;
}

function relativeLuminance([r, g, b]: Rgb): number {
  const lin = (v: number) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

function contrastRatio(fg: Rgb, bg: Rgb): number {
  const [hi, lo] = [relativeLuminance(fg), relativeLuminance(bg)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

// ── token parsing ──────────────────────────────────────────────────────────

const tokens = readFileSync(tokensPath, "utf8");

/**
 * Pull one theme block's own declarations. `:root` also contains the `@theme`
 * blocks' siblings, so the match is anchored on the selector and stops at the
 * first closing brace at column 0.
 */
function themeBlock(selector: string): string {
  const start = tokens.indexOf(`${selector} {`);
  if (start === -1) throw new Error(`no ${selector} block in tokens.css`);
  const end = tokens.indexOf("\n}", start);
  return tokens.slice(start, end);
}

function readOklch(block: string, name: string): Rgb {
  const match = block.match(
    new RegExp(String.raw`--${name}:\s*oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)`),
  );
  if (!match) throw new Error(`--${name} is not a literal oklch() in this theme block`);
  return oklchToSrgb(Number(match[1]), Number(match[2]), Number(match[3]));
}

/**
 * Every surface product text sits on. Aliases are omitted: --background is
 * --page-canvas, --card and --popover are --surface, and --accent/--secondary
 * share --muted's value, so covering the bases covers them.
 */
const SURFACES = [
  "surface",
  "page-canvas",
  "app-shell",
  "muted",
  "surface-selected",
  "sidebar",
  "sidebar-accent",
] as const;

const THEMES = [
  ["light", ":root"],
  ["dark", ".dark"],
] as const;

/** WCAG 1.4.3 for body and label text; 1.4.11 for icons and other marks. */
const AA_TEXT = 4.5;
const AA_NON_TEXT = 3.0;

// ── source scanning ────────────────────────────────────────────────────────

const scanRoots = ["packages/ui", "packages/views", "apps/web", "apps/desktop/src"];
const skipDirs = new Set(["node_modules", ".next", "dist", "out", "build", ".turbo"]);
const sourceExtensions = [".ts", ".tsx", ".css"];

/**
 * Colours whose hierarchy is fully expressed by solid tokens, so a fraction of
 * one is always someone inventing a tier. `white` and `background` are absent
 * on purpose: they paint text on photos, gradients, and inverted cards, where
 * no solid secondary token exists and alpha is the honest mechanism.
 */
const guardedColors = ["foreground", "muted-foreground", "sidebar-foreground", "destructive", "current"];

/**
 * Alpha behind an interaction variant is transient feedback, not a hierarchy
 * level — the resting state is what carries the contrast obligation, and it is
 * solid. `dark:` is not on this list: it paints a resting state.
 */
const stateVariants =
  /\b(?:hover|focus|focus-visible|focus-within|active|disabled|visited|group-hover|group-focus|peer-hover|peer-focus|aria-disabled|data-disabled|data-\[disabled)\b/;

const alphaOnText = new RegExp(
  String.raw`[\w:\[\]./-]*\btext-(?:${guardedColors.join("|")})/\d+`,
  "g",
);

/**
 * Comments name the old classes when explaining what replaced them, so they are
 * stripped first. Over-stripping can only hide a violation, never invent one.
 */
function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/[^\n]*/g, "$1");
}

function collectSourceFiles(dir: string, found: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (skipDirs.has(entry)) continue;
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) {
      collectSourceFiles(path, found);
      continue;
    }
    if (!sourceExtensions.some((ext) => entry.endsWith(ext))) continue;
    if (/\.test\.tsx?$/.test(entry)) continue;
    found.push(path);
  }
  return found;
}

// ── the guard ──────────────────────────────────────────────────────────────

describe("text contrast", () => {
  describe.each(THEMES)("%s theme", (_theme, selector) => {
    const block = themeBlock(selector);
    const surfaces = SURFACES.map((name) => [name, readOklch(block, name)] as const);

    it.each(surfaces)("muted-foreground clears AA text contrast on --%s", (_name, bg) => {
      expect(contrastRatio(readOklch(block, "muted-foreground"), bg)).toBeGreaterThanOrEqual(
        AA_TEXT,
      );
    });

    it.each(surfaces)("faint-foreground clears non-text contrast on --%s", (_name, bg) => {
      expect(contrastRatio(readOklch(block, "faint-foreground"), bg)).toBeGreaterThanOrEqual(
        AA_NON_TEXT,
      );
    });

    it("faint-foreground is quieter than muted-foreground, not louder", () => {
      const surface = readOklch(block, "surface");
      expect(contrastRatio(readOklch(block, "faint-foreground"), surface)).toBeLessThan(
        contrastRatio(readOklch(block, "muted-foreground"), surface),
      );
    });
  });

  it("product UI expresses text hierarchy with tokens, not alpha", () => {
    const violations: string[] = [];

    for (const root of scanRoots) {
      for (const path of collectSourceFiles(resolve(repoRoot, root))) {
        const lines = stripComments(readFileSync(path, "utf8")).split("\n");
        lines.forEach((line, index) => {
          alphaOnText.lastIndex = 0;
          for (const match of line.matchAll(alphaOnText)) {
            if (stateVariants.test(match[0])) continue;
            violations.push(
              `${relative(repoRoot, path)}:${index + 1}  ${match[0]}  ` +
                `(use a solid tone: text-foreground / text-muted-foreground, ` +
                `or text-faint-foreground for icons and glyphs)`,
            );
          }
        });
      }
    }

    expect(violations, `Alpha standing in for a text tone:\n${violations.join("\n")}`).toEqual([]);
  });
});

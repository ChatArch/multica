import { readFileSync, readdirSync, statSync } from "node:fs";
import { resolve, join, relative } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * Does the product still have exactly one type scale?
 *
 * Before `--text-*` existed in tokens.css there was no baseline to align to, so
 * font sizes grew wherever they were needed: 51 distinct sizes across web and
 * desktop, 370 written as arbitrary `text-[Npx]` values, six of those at half a
 * pixel (10.5 / 11.5 / 12.5 / 13.5 / 14.5 / 15.5px). A scale only holds if
 * going off it is harder than staying on it, and nothing in a Tailwind build
 * makes `text-[13px]` look wrong — it compiles exactly like a token does.
 *
 * So the guard lives here. This asserts on source text rather than on rendered
 * components because the failure mode is authorial, not behavioural: a
 * component test would prove a class name renders, never that the class was one
 * of the ten the design system actually defines.
 *
 * Scope is product UI. `apps/mobile` builds through its own NativeWind config
 * and `apps/docs` rides fumadocs' type system; both keep Tailwind's default
 * scale on purpose and are not scanned.
 */

const repoRoot = resolve(process.cwd(), "../..");

/** The scale, as tokens.css defines it. Sizes in px. */
const SCALE = [
  ["micro", 11, 15],
  ["caption", 12, 16],
  ["label", 13, 18],
  ["body", 14, 20],
  ["body-lg", 15, 22],
  ["title-sm", 16, 24],
  ["title", 18, 28],
  ["title-lg", 20, 28],
  ["display-sm", 24, 32],
  ["display", 36, 40],
] as const;

const scanRoots = ["packages/ui", "packages/views", "apps/web", "apps/desktop/src"];
const skipDirs = new Set(["node_modules", ".next", "dist", "out", "build", ".turbo"]);
const sourceExtensions = [".ts", ".tsx", ".css"];

/**
 * Tailwind's default steps that the role scale replaces one-for-one. `text-4xl`
 * and larger are not listed: their only call sites are decorative emoji and the
 * serif onboarding hero, which are not UI text and do not sit on this ramp.
 */
const replacedDefaults = ["xs", "sm", "base", "lg", "xl", "2xl", "3xl"];

const bannedPatterns = [
  {
    label: "arbitrary font size",
    regex: /\btext-\[\d+(?:\.\d+)?px\]/g,
    hint: "use a scale step (text-micro … text-display)",
  },
  {
    label: "Tailwind default size",
    regex: new RegExp(String.raw`\btext-(?:${replacedDefaults.join("|")})\b`, "g"),
    hint: "use the role-named equivalent, e.g. text-sm -> text-body",
  },
];

/**
 * Comments legitimately name the old classes when explaining what a step
 * replaced, so they are stripped before scanning. Over-stripping would only
 * ever hide a violation, never invent one, and a class name never follows a
 * comment marker on the same line in practice.
 */
function stripComments(source: string): string {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|[^:])\/\/[^\n]*/g, "$1");
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

describe("type scale", () => {
  const tokens = readFileSync(resolve(repoRoot, "packages/ui/styles/tokens.css"), "utf8");

  it.each(SCALE)("defines --text-%s with a paired line-height", (name, size, lineHeight) => {
    expect(tokens).toContain(`--text-${name}: ${size}px;`);
    expect(tokens).toContain(`--text-${name}--line-height: ${lineHeight}px;`);
  });

  it("steps ascend in size and never tighten line-height going up", () => {
    for (let i = 1; i < SCALE.length; i += 1) {
      const [name, size, lineHeight] = SCALE[i]!;
      const [, previousSize, previousLineHeight] = SCALE[i - 1]!;
      expect(size, `${name} must be larger than the step below it`).toBeGreaterThan(previousSize);
      expect(lineHeight, `${name} line-height must not drop below the step below it`)
        .toBeGreaterThanOrEqual(previousLineHeight);
    }
  });

  it("product UI writes no font size outside the scale", () => {
    const violations: string[] = [];

    for (const root of scanRoots) {
      for (const path of collectSourceFiles(resolve(repoRoot, root))) {
        const lines = stripComments(readFileSync(path, "utf8")).split("\n");
        lines.forEach((line, index) => {
          for (const { label, regex, hint } of bannedPatterns) {
            regex.lastIndex = 0;
            for (const match of line.matchAll(regex)) {
              violations.push(
                `${relative(repoRoot, path)}:${index + 1}  ${match[0]}  (${label} — ${hint})`,
              );
            }
          }
        });
      }
    }

    expect(violations, `Font sizes off the scale:\n${violations.join("\n")}`).toEqual([]);
  });
});

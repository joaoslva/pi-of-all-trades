#!/usr/bin/env node
// WCAG contrast of every colour pair the pages use, in both themes. Exits 1 on any failure.
//   node hub/web/check-contrast.mjs [theme.css]
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const css = readFileSync(process.argv[2] || fileURLToPath(new URL("theme.css", import.meta.url)), "utf8");

function tokens(block) {
  const out = {};
  for (const [, k, v] of block.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})\b/g)) out[k] = v;
  return out;
}
const light = tokens(css.match(/:root\s*{([^}]*)}/)[1]);
const darkBlock = css.match(/prefers-color-scheme:\s*dark\)\s*{\s*:root\s*{([^}]*)}/);
const themes = { light, dark: { ...light, ...tokens(darkBlock[1]) } };

const lum = hex => {
  const c = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map(v => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };

const TEXT = 4.5, UI = 3;
// [foreground, background, minimum, where it is used]
const pairs = [
  ["text", "bg", TEXT, "page text, top bar"],
  ["text", "card", TEXT, "card text"],
  ["text", "card-hi", TEXT, "service button name"],
  ["text", "sunk", TEXT, "inputs, list rows"],
  ["text", "warn-bg", TEXT, "Keep banner"],
  ["text", "danger-bg", TEXT, "warning banner"],
  ["text", "info-bg", TEXT, "hint bubble"],
  ["muted", "bg", TEXT, "hub subtitle, footer"],
  ["muted", "card", TEXT, "labels, small print"],
  ["muted", "card-hi", TEXT, "service button description"],
  ["muted", "sunk", TEXT, "list row details"],
  ["accent-ink", "card", TEXT, "links, section accents"],
  ["accent-ink", "bg", TEXT, "links on the page"],
  ["on-accent", "accent", TEXT, "primary button label"],
  ["on-danger", "danger", TEXT, "Power off button label"],
  ["danger-ink", "card", TEXT, "error text, stopped label"],
  ["danger-ink", "danger-bg", TEXT, "warning banner title"],
  ["ok", "card", TEXT, "running label"],
  ["ok", "sunk", TEXT, "running label in list rows"],
  ["info", "card", TEXT, "hint and info text"],
  ["info", "info-bg", TEXT, "hint bubble label"],
  ["field", "card", UI, "input and button borders"],
  ["field", "sunk", UI, "input borders on the sunk fill"],
  ["accent-ink", "bg", UI, "focus ring on the page"],
  ["accent-ink", "card", UI, "focus ring on cards"],
  ["ok", "sunk", UI, "running status dot"],
  ["danger-ink", "sunk", TEXT, "stopped label and status dot in list rows"],
  ["accent-ink", "card-hi", UI, "lock icon and light bulb strokes"],
];

let failed = 0;
for (const [name, t] of Object.entries(themes)) {
  console.log(`\n${name}`);
  for (const [fg, bg, min, use] of pairs) {
    if (!t[fg] || !t[bg]) { console.log(`FAIL  --${fg} or --${bg} is not defined`); failed++; continue; }
    const r = ratio(t[fg], t[bg]);
    const ok = r >= min;
    if (!ok) failed++;
    console.log(`${ok ? "PASS" : "FAIL"}  ${r.toFixed(2).padStart(5)} >= ${min}  --${fg} on --${bg}  (${use})`);
  }
}
console.log(failed ? `\n${failed} pair(s) below WCAG AA` : "\nall pairs meet WCAG AA");
process.exit(failed ? 1 : 0);

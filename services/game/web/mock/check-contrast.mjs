// Fails (exit 1) on any text/background pair under 4.5:1, including card ink on every character colour. node mock/check-contrast.mjs [style.css] [characters.json]
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const css = readFileSync(process.argv[2] || fileURLToPath(new URL("../style.css", import.meta.url)), "utf8");
const t = Object.fromEntries([...css.match(/:root\s*{([^}]*)}/)[1].matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})\b/g)].map(m => [m[1], m[2]]));
t.glow = "#2b1530"; t.btnTop = "#f2c460"; t.canvasTop = "#2e211b"; // literal gradient stops in style.css
const blend = (a, b, k) => "#" + [1, 3, 5].map(i => Math.round(parseInt(a.slice(i, i + 2), 16) * k + parseInt(b.slice(i, i + 2), 16) * (1 - k)).toString(16).padStart(2, "0")).join("");
for (const b of ["panel", "canvas", "canvasTop", "bg"]) t[`btn-${b}`] = blend(t.gold, t[b], 0x14 / 255); // ghost-button fill #e8b04a14
const lum = hex => {
  const c = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(v => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
const T = 4.5, UI = 3;
const pairs = [
  ["text", "bg", T], ["text", "glow", T], ["text", "panel", T], ["text", "panel-hi", T],
  ["muted", "bg", T], ["muted", "panel", T], ["muted", "panel-hi", T],
  ["gold", "bg", T], ["gold", "panel", T], ["gold", "panel-hi", T],
  ["on-gold", "gold", T], ["on-gold", "btnTop", T],
  ["danger-ink", "danger-bg", T], ["danger-ink", "panel", T], ["ok-ink", "bg", T],
  ["field", "panel", UI], ["field", "bg", UI],
  // Text that sits on the page, not a panel, can land on a background rune stroke.
  ["text", "rune-ink", T], ["muted", "rune-ink", T], ["gold", "rune-ink", T], ["ok-ink", "rune-ink", T],
  // Home: the title is inlaid in the mantel lintel, the join and lobby text sits on the painting's canvas.
  ["gold", "lintel", T], ["gold", "lintel-hi", T], ["muted", "lintel", T], ["muted", "lintel-hi", T],
  ...["canvas", "canvasTop"].flatMap(b => [["text", b, T], ["muted", b, T], ["gold", b, T], ["field", b, UI]]),
  ...["panel", "canvas", "canvasTop", "bg"].flatMap(b => [["text", `btn-${b}`, T], ["gold", `btn-${b}`, T], ["field", `btn-${b}`, UI]]),
];
// Same rule as darkInk in app.js: the ink with the higher ratio.
const chars = JSON.parse(readFileSync(process.argv[3] || fileURLToPath(new URL("../../content/characters.json", import.meta.url)), "utf8"));
for (const c of new Set([...chars.map(c => c.color || t.stone)])) {
  t[c] = c;
  pairs.push([ratio(t["on-gold"], c) > ratio(t.text, c) ? "on-gold" : "text", c, T]);
}
let bad = 0;
for (const [f, b, min] of pairs) {
  const r = ratio(t[f], t[b]), pass = r >= min;
  if (!pass) bad++;
  console.log(`${pass ? "PASS" : "FAIL"}  ${f} on ${b}  ${r.toFixed(2)} (min ${min})`);
}
process.exit(bad ? 1 : 0);

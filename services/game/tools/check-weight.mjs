// Fails when the served page (web/ minus mock/) outgrows the Pi's budget.
import { readdirSync, statSync } from 'node:fs';
const LIMIT = +process.env.LIMIT || 150000;
const root = new URL('../web/', import.meta.url).pathname;
const files = [];
(function walk(d) {
  for (const e of readdirSync(d, { withFileTypes: true })) {
    const p = d + e.name;
    if (e.isDirectory()) { if (p !== root + 'mock') walk(p + '/'); }
    else files.push([statSync(p).size, p.slice(root.length)]);
  }
})(root);
const total = files.reduce((s, f) => s + f[0], 0);
if (total > LIMIT) {
  console.error(`web/ is ${total} bytes, limit ${LIMIT}. Largest:`);
  for (const [n, p] of files.sort((a, b) => b[0] - a[0]).slice(0, 5)) console.error(`  ${n}  ${p}`);
  process.exit(1);
}
console.log(`web/ weighs ${total} bytes (limit ${LIMIT})`);

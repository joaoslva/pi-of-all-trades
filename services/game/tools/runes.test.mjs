import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
const file = new URL('../web/runes.js', import.meta.url);
const src = readFileSync(file, 'utf8');
const { runeSvg } = new Function(src + '; return {runeSvg};')();
for (const c of 'ABCDEFGHIJKLMNOPQRSTUVWXYZ') {
  assert.match(runeSvg(c), /<path /, c);
  assert.equal(runeSvg(c.toLowerCase()), runeSvg(c));
}
assert.match(runeSvg(' '), /<svg/);
assert.equal(runeSvg('-'), '-');
assert.equal(runeSvg('<'), '&#60;');
assert.ok(Buffer.byteLength(src) < 6144, 'runes.js over 6 KB');
console.log('runes ok,', Buffer.byteLength(src), 'bytes');

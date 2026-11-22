// Two phones against the real server: [ROUNDS=n] node mock/live.mjs <base-url> <shots-dir>
import { chromium } from "/home/joao/.claude/tools/playwright/node_modules/playwright/index.mjs";
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";

const [BASE, OUT] = process.argv.slice(2), ROUNDS = +(process.env.ROUNDS || 1);
mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();
const phone = async () => {
  const ctx = await browser.newContext({ viewport: { width: 412, height: 915 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  const page = await ctx.newPage();
  page.on("pageerror", e => { console.log("PAGEERROR", e.message); process.exitCode = 1; });
  await page.goto(BASE);
  return page;
};
const ok = m => console.log("ok  ", m);
const text = p => p.locator("body").innerText();
const secretOf = p => p.locator(".secret .sname").innerText();
const shots = async (p, name) => {
  for (const [w, hh] of [[412, 915], [360, 640]]) { await p.setViewportSize({ width: w, height: hh }); await p.screenshot({ path: `${OUT}/${name}-${w}x${hh}.png` }); }
  await p.setViewportSize({ width: 412, height: 915 });
};

const A = await phone(), B = await phone(), C = await phone();
for (const [p, n] of [[A, "Ana"], [B, "Bo"]]) { await p.fill("input", n); await p.click("text=Join"); }
await A.waitForSelector("text=Start");
await C.fill("input", "Cy"); await C.click("text=Join");
await C.waitForSelector("text=dragons have decided");
ok("third phone refused with the dragons text");
await A.screenshot({ path: `${OUT}/lobby.png` });
for (let n = 2; n >= ROUNDS; n--) { await A.click('[aria-label="Fewer rounds"]'); await A.waitForFunction(n => document.querySelector("output")?.textContent === String(n), n); }
await A.click("text=Start");

for (let round = 1; round <= ROUNDS; round++) {
  await A.click("text=Draw your character"); await A.waitForSelector(".secret .card");
  await A.screenshot({ path: `${OUT}/r${round}-A-mid.png` });
  await B.click("text=Draw your character");
  await A.waitForSelector("text=Got it"); await B.waitForSelector("text=Got it");
  const sa = await secretOf(A), sb = await secretOf(B);
  await A.waitForSelector(".secret .card:not(.revealing)"); await B.waitForSelector(".secret .card:not(.revealing)");
  await shots(A, `r${round}-A-${sa}`); await shots(B, `r${round}-B-${sb}`);
  assert.ok(sa && sb && sa !== sb, `secrets ${sa} / ${sb}`);
  assert.ok(!(await text(A)).includes(sb) && !(await text(B)).includes(sa), "a page shows the other secret");
  ok(`round ${round}: A holds "${sa}", B holds "${sb}", neither page shows the other's`);
  await A.screenshot({ path: `${OUT}/playing-A.png` });
  await A.click("text=Got it"); await B.click("text=Forfeit");
  await A.waitForSelector("text=Next round"); await B.waitForSelector("text=Next round");
  assert.ok((await text(A)).includes(sb) && (await text(B)).includes(sa)); ok("roundEnd reveals both secrets");
  await A.screenshot({ path: `${OUT}/roundEnd-A.png` });
  await A.reload(); await A.waitForSelector("text=Next round"); ok("reload mid-game restores the screen from the token");
  await A.click("text=Next round");
  if (round < ROUNDS) { await A.waitForSelector("text=Draw your character"); await B.waitForSelector("text=Draw your character"); }
}
await A.waitForSelector("text=Back to lobby", { timeout: 8000 }); await B.waitForSelector("text=Back to lobby");
await A.screenshot({ path: `${OUT}/final.png` });
ok("final screen on both phones");
await A.click("text=Back to lobby"); await A.waitForSelector("text=Start"); await A.click("text=Past games");
await A.waitForFunction(() => /Ana \d+ - Bo 0/.test(document.body.innerText)); assert.match(await text(A), /Ana \d+ - Bo 0/); ok("history scroll lists the finished game");
await browser.close();
console.log(process.exitCode ? "FAILED" : "ALL PASS");

// Two phones against the mock. Start it first: MOCK_SECONDS=6 node mock/mock.mjs
// node mock/e2e.mjs <shots-dir>
import { chromium } from "/home/joao/.claude/tools/playwright/node_modules/playwright/index.mjs";
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";

const BASE = "http://localhost:18091", OUT = process.argv[2];
mkdirSync(OUT, { recursive: true });
await fetch(`${BASE}/mock/reset`);
const browser = await chromium.launch();
const phone = async (size = { width: 412, height: 915 }, extra = {}) => {
  const ctx = await browser.newContext({ viewport: size, deviceScaleFactor: 2, isMobile: true, hasTouch: true, ...extra });
  const page = await ctx.newPage();
  page.on("pageerror", e => { console.log("PAGEERROR", e.message); process.exitCode = 1; });
  await page.goto(BASE);
  return page;
};
const shot = (p, n) => p.screenshot({ path: `${OUT}/${n}.png` });
const text = p => p.locator("body").innerText();
const ok = m => console.log("ok  ", m);
const fits = async (p, label, vertical) => {
  const [sw, sh, w, hh] = await p.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.scrollHeight, innerWidth, innerHeight]);
  assert.ok(sw <= w, `${label}: sideways scroll ${sw}>${w}`);
  if (vertical) assert.ok(sh <= hh, `${label}: vertical scroll ${sh}>${hh}`);
  ok(`${label}: scroll ${sw}x${sh} in ${w}x${hh}`);
};

const A = await phone(), B = await phone();
await shot(A, "01-join");
for (const [p, n] of [[A, "Ana"], [B, "Bo"]]) { await p.fill("input", n); await p.click("text=Join"); }
await A.waitForSelector("text=Bo"); await shot(A, "02-lobby");
await A.click("text=Past games"); await A.waitForSelector("text=No games yet."); await shot(A, "03-history");
await A.click("text=Roll up the scroll");
await A.click("text=Start");
await A.waitForSelector("text=Draw your character"); await shot(A, "04-drawing");

// 4: clip vs no clip
await A.click("text=Draw your character");
await A.waitForSelector(".stage video"); ok("clip present: A plays a <video>");
assert.equal(await A.locator(".stage video").evaluate(v => v.muted), true); ok("video is muted");
await shot(A, "05-reveal-clip");
await B.click("text=Draw your character");
await B.waitForSelector(".stage .flame"); assert.equal(await B.locator(".stage video").count(), 0); ok("no clip: B gets the CSS ember reveal, no <video>");
await shot(B, "06-reveal-ember");
await A.waitForSelector(".stage img", { timeout: 5000 }); ok("after the clip ends A holds the poster");
await A.waitForSelector(".time"); await shot(A, "07-playing-A"); await shot(B, "07-playing-B");

// 5: no leak of the other's secret
const tA = await text(A), tB = await text(B);
assert.ok(tA.includes("Tairn") && !tA.includes("Violet Sorrengail")); assert.ok(tB.includes("Violet Sorrengail") && !tB.includes("Tairn"));
ok("playing: A shows only Tairn, B shows only Violet Sorrengail");
await A.click(".secret > button"); assert.ok(await A.locator(".secret.collapsed").count()); await shot(A, "08-secret-collapsed"); await A.click(".secret > button");

// 2: fit
await fits(A, "playing 412x915", true);
await A.setViewportSize({ width: 360, height: 640 }); await fits(A, "playing 360x640", true); await shot(A, "09-playing-360x640");
await A.setViewportSize({ width: 1366, height: 768 }); await fits(A, "playing 1366x768 (sideways only)", false); await shot(A, "10-playing-desktop");
await A.setViewportSize({ width: 412, height: 915 });

// A got it, B forfeits
await A.click("text=Got it"); await A.waitForSelector("text=You got it"); await shot(A, "11-playing-resolved");
await B.waitForSelector("text=Ana got it"); await B.click("text=Forfeit");
await A.waitForSelector("text=Next round"); await shot(A, "12-roundEnd-A"); await shot(B, "12-roundEnd-B");
assert.ok((await text(A)).includes("Violet Sorrengail")); ok("roundEnd reveals the other secret");

// refusal for a third phone
const C = await phone(); await C.fill("input", "Late"); await C.click("text=Join");
await C.waitForSelector(".refused"); await shot(C, "13-refused"); ok("third player refused: " + (await C.locator(".refused p").innerText()));

// round 2, and a rejoin by token mid-round
await B.click("text=Next round"); await A.waitForSelector("text=Draw your character");
await A.click("text=Draw your character"); await B.click("text=Draw your character");
await A.waitForSelector(".time");
await A.reload(); await A.waitForSelector(".time"); ok("rejoin: reload restored the playing screen from the token");
assert.ok(await A.evaluate(() => { try { return !!localStorage.getItem("gameToken"); } catch { return false; } }));
await A.click("text=Got it"); await B.click("text=Got it");
await A.waitForSelector("text=Next round"); await A.click("text=Next round");

// round 3: timeout
await A.click("text=Draw your character"); await B.click("text=Draw your character");
await A.waitForSelector(".time");
await A.waitForSelector("text=Back to lobby", { timeout: 15000 }); ok("timeout: nobody pressed, round ended by the clock");
assert.match(await A.locator(".verdict").innerText(), /\+0[\s\S]*\+0/); ok("timeout gains are +0 and +0");
await shot(A, "14-final-timeout");
await shot(A, "15-final-A"); await shot(B, "15-final-B");
await B.click("text=Back to lobby"); await A.waitForSelector("text=Start"); 
await A.click("text=Past games"); await A.waitForSelector(".hist"); await shot(A, "16-history-with-game");

// lost banner: offline while open
await A.context().setOffline(true); await A.evaluate(() => document.getElementById("lost").hidden = false);
await shot(A, "17-connection-lost"); await A.context().setOffline(false);

// 6: reduced motion
const normal = await phone(); const reduced = await phone({ width: 412, height: 915 }, { reducedMotion: "reduce" });
const anim = p => p.evaluate(() => getComputedStyle(document.querySelector(".embers"), "::before").animationName);
const [an, ar] = [await anim(normal), await anim(reduced)];
assert.equal(an, "drift"); assert.equal(ar, "none"); ok(`ember animation: normal=${an}, reduced-motion=${ar}`);
await browser.close();
console.log("ALL PASS");

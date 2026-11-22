// Two phones against the mock. Start it first: MOCK_SECONDS=6 node mock/mock.mjs
// node mock/e2e.mjs <shots-dir>
import { chromium } from "/home/joao/.claude/tools/playwright/node_modules/playwright/index.mjs";
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";

const BASE = process.env.BASE || "http://localhost:18091", OUT = process.argv[2];
mkdirSync(OUT, { recursive: true });
await fetch(`${BASE}/mock/reset`);
const browser = await chromium.launch();
const phone = async (size = { width: 412, height: 915 }, extra = {}) => {
  const ctx = await browser.newContext({ viewport: size, deviceScaleFactor: 2, isMobile: true, hasTouch: true, ...extra });
  await ctx.addInitScript(() => {
    const live = new Set(), si = setInterval, ci = clearInterval;
    window.setInterval = (...a) => { const id = si(...a); live.add(id); return id; };
    window.clearInterval = id => { live.delete(id); ci(id); };
    window.liveTimers = () => live.size;
  });
  const page = await ctx.newPage();
  page.on("pageerror", e => { console.log("PAGEERROR", e.message); process.exitCode = 1; });
  await page.goto(BASE);
  return page;
};
const shot = (p, n) => p.screenshot({ path: `${OUT}/${n}.png` });
const text = p => p.locator("body").innerText();
const ok = m => console.log("ok  ", m);
const rgb = hex => `rgb(${[1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16)).join(", ")})`;
const cardStyle = (p, sel = ".secret .card") => p.locator(sel).first().evaluate(e => [getComputedStyle(e).backgroundColor, getComputedStyle(e).color]);
const settled = p => p.locator(".secret .r:not(.spin)").count();
const timers = p => p.evaluate(() => window.liveTimers());
const fits = async (p, label, vertical) => {
  // Against the configured size: a mobile layout viewport widens to fit overflowing content, so innerWidth would hide it.
  const { width: w, height: hh } = p.viewportSize();
  const [sw, sh] = await p.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.scrollHeight]);
  assert.ok(sw <= w, `${label}: sideways scroll ${sw}>${w}`);
  if (vertical) {
    assert.ok(sh <= hh, `${label}: vertical scroll ${sh}>${hh}`);
    const covered = await p.evaluate(() => [...document.querySelectorAll("#screen button, #screen input")].filter(e => e.getClientRects().length).filter(e => {
      const r = e.getBoundingClientRect(), t = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
      return !(t && e.contains(t));
    }).map(e => e.textContent || e.placeholder));
    assert.deepEqual(covered, [], `${label}: controls covered or off screen`);
  }
  ok(`${label}: scroll ${sw}x${sh} in ${w}x${hh}`);
};
const anims = p => p.evaluate(() => document.getAnimations().map(a => ({
  cls: a.effect.target.getAttribute("class"), fire: !!a.effect.target.closest(".hearth"), state: a.playState,
  props: [...new Set(a.effect.getKeyframes().flatMap(Object.keys))].filter(k => !["offset", "computedOffset", "easing", "composite"].includes(k)),
})));
const setHidden = (p, hidden) => p.evaluate(h => {
  if (h) Object.defineProperty(document, "hidden", { configurable: true, get: () => true }); else delete document.hidden;
  document.dispatchEvent(new Event("visibilitychange"));
}, hidden);
const PHONES = [{ width: 360, height: 640 }, { width: 412, height: 915 }];
const fitsPhones = async (p, label, vertical) => {
  const back = p.viewportSize();
  for (const s of PHONES) {
    await p.setViewportSize(s); await fits(p, `${label} ${s.width}x${s.height}`, vertical);
    await shot(p, `fit-${label.replace(/\W+/g, "-")}-${s.width}x${s.height}`);
  }
  await p.setViewportSize(back);
};

const A = await phone(), B = await phone();
await shot(A, "01-join"); await fitsPhones(A, "join", true);
const fire = await anims(A);
assert.ok(fire.length >= 1 && fire.length <= 6 && fire.every(a => a.fire && a.state === "running"), JSON.stringify(fire));
assert.deepEqual([...new Set(fire.flatMap(a => a.props))].sort(), ["opacity", "transform"]);
ok(`join: ${fire.length} running animations, all in the fireplace, animating only opacity and transform`);
await setHidden(A, true); assert.ok((await anims(A)).every(a => a.state === "paused")); ok("hidden tab: fire paused");
await setHidden(A, false); assert.ok((await anims(A)).every(a => a.state === "running")); ok("visible again: fire running");
await A.fill("input", "Ana"); await A.click("text=Join");
await A.waitForSelector("text=Waiting for a second rider"); await shot(A, "02a-lobby-alone"); await fitsPhones(A, "lobby one player", true);
await B.fill("input", "Bo"); await B.click("text=Join");
await A.waitForSelector("text=Bo"); await shot(A, "02-lobby"); await fitsPhones(A, "lobby", true);
await A.click("text=Past games"); await A.waitForSelector("text=No games yet."); await shot(A, "03-history"); await fitsPhones(A, "history", false);
await A.click("text=Roll up the scroll");
await A.click("text=Start");
await A.waitForSelector("text=Draw your character"); await shot(A, "04-drawing"); await fitsPhones(A, "drawing", true);
assert.ok((await anims(A)).every(a => !a.fire)); ok("drawing: the fireplace is gone and animates nothing");

// 4: B (no clip) reveals by runes; A (clip) draws mid-animation and must not restart it
await B.click("text=Draw your character");
await B.waitForSelector(".secret .card.revealing"); ok("no clip: B gets the rune reveal");
assert.equal(await B.locator(".secret .sname").innerText(), "Violet Sorrengail"); ok("name is real text from the start");
assert.equal(await B.locator(".runes[aria-hidden=true] .r").count(), 16); ok("16 rune spans, aria-hidden");
assert.deepEqual(await cardStyle(B), [rgb("#1F2421"), rgb("#f3e5c8")]); ok("Violet card #1F2421 with light ink");
assert.ok(await B.locator(".hints").isHidden()); ok("no Hint while drawing");
await B.waitForFunction(() => document.querySelectorAll(".secret .r:not(.spin)").length >= 2);
await B.evaluate(() => { document.querySelector(".secret").marked = true; });
const before = await settled(B);
await shot(B, "05-reveal-runes-mid");
await A.click("text=Draw your character");
await A.waitForSelector(".stage video"); ok("clip present: A plays a <video>");
assert.equal(await A.locator(".stage video").evaluate(v => v.muted), true); ok("video is muted");
await shot(A, "06-reveal-clip");
await B.waitForSelector(".time");
assert.ok(await B.evaluate(() => document.querySelector(".secret").marked)); assert.ok(await B.locator(".card.revealing").count());
assert.ok((await settled(B)) >= before); ok(`opponent's draw mid-animation: same card node, still revealing, settled runes ${before} -> ${await settled(B)}`);
await B.click("text=Hint (3 left)");
assert.deepEqual(await B.locator(".tips li").allInnerTexts(), ["Violet vague tip."]); await B.waitForSelector("text=Hint (2 left)");
assert.ok(await B.locator(".card.revealing").count()); ok("Hint works during the animation, vague tip first");
assert.ok(!(await text(A)).includes("Violet vague tip")); ok("A never sees B's tips");
await B.waitForSelector(".secret .card:not(.revealing)", { timeout: 4000 });
assert.equal(await B.locator(".secret .r.spin").count(), 0); assert.equal(await timers(B), 1); ok("reveal settles, only the clock interval left");
await B.reload(); await B.waitForSelector(".time");
assert.equal(await B.locator(".card.revealing").count(), 0); assert.equal(await B.locator(".r.spin").count(), 0);
assert.equal(await B.locator(".tips li").count(), 1); await B.waitForSelector("text=Hint (2 left)"); ok("reload: settled card, no animation, revealed tip kept");
await B.click("text=Hint (2 left)"); await B.click("text=Hint (1 left)");
assert.equal(await B.locator(".tips li").count(), 3); assert.equal(await B.locator("button.hint:visible").count(), 0); ok("all tips shown, Hint gone");
await fitsPhones(B, "playing, three tips open", true);
await A.waitForSelector(".stage img", { timeout: 5000 }); ok("after the clip ends A holds the poster");
await A.waitForSelector(".time"); await shot(A, "07-playing-A"); await shot(B, "07-playing-B");
await B.setViewportSize({ width: 360, height: 640 }); await shot(B, "07-playing-B-tips-360x640"); await B.setViewportSize({ width: 412, height: 915 });

// 5: no leak of the other's secret
const tA = await text(A), tB = await text(B);
assert.ok(tA.includes("Tairn") && !tA.includes("Violet Sorrengail")); assert.ok(tB.includes("Violet Sorrengail") && !tB.includes("Tairn"));
ok("playing: A shows only Tairn, B shows only Violet Sorrengail");
await A.click(".secret > button"); assert.ok(await A.locator(".secret .card").isHidden()); ok("Hide hides the card"); await shot(A, "08-secret-collapsed"); await A.click(".secret > button");

// 2: fit
await fits(A, "playing 412x915", true);
await A.setViewportSize({ width: 360, height: 640 }); await fits(A, "playing 360x640", true); await shot(A, "09-playing-360x640");
await A.setViewportSize({ width: 1366, height: 768 }); await fits(A, "playing 1366x768 (sideways only)", false); await shot(A, "10-playing-desktop");
await A.setViewportSize({ width: 412, height: 915 });

// A got it, B forfeits
await A.click("text=Got it"); await A.waitForSelector("text=You got it"); await shot(A, "11-playing-resolved");
await B.waitForSelector("text=Ana got it"); await B.click("text=Forfeit");
await A.waitForSelector("text=Next round"); await shot(A, "12-roundEnd-A"); await shot(B, "12-roundEnd-B"); await fitsPhones(A, "roundEnd", true);
assert.ok((await text(A)).includes("Violet Sorrengail")); ok("roundEnd reveals the other secret");
assert.equal(await B.locator(".tips, .hint").count(), 0); ok("no tips at roundEnd");
assert.deepEqual(await cardStyle(A, ".verdict .card"), [rgb("#1F2421"), rgb("#f3e5c8")]); ok("roundEnd cards carry the reveal colour");
assert.deepEqual([await timers(A), await timers(B)], [1, 1]); ok("roundEnd: no reveal timer left on either phone");

// refusal for a third phone
const C = await phone(); await C.fill("input", "Late"); await C.click("text=Join");
await C.waitForSelector(".refused"); await shot(C, "13-refused"); await fitsPhones(C, "refused", true); ok("third player refused: " + (await C.locator(".refused p").innerText()));

// round 2: B reduced motion, A animates, then A reloads mid-round
await B.click("text=Next round"); await A.waitForSelector("text=Draw your character");
await B.emulateMedia({ reducedMotion: "reduce" });
await B.click("text=Draw your character"); await B.waitForSelector(".secret .card");
assert.equal(await B.locator(".card.revealing, .r.spin").count(), 0); assert.equal(await timers(B), 1); ok("reduced motion: settled at once, no timer");
assert.equal((await anims(B)).length, 0); ok("reduced motion: no embers or other animation while playing");
await B.emulateMedia({ reducedMotion: "no-preference" });
await A.click("text=Draw your character"); await A.waitForSelector(".card.revealing");
assert.deepEqual(await cardStyle(A), [rgb("#FFD400"), rgb("#1a0e05")]); ok("Andarna card #FFD400 with dark ink");
await A.waitForSelector(".time"); await shot(A, "13b-andarna-mid");
await A.reload(); await A.waitForSelector(".time");
assert.equal(await A.locator(".card.revealing, .r.spin").count(), 0); ok("rejoin: reload restored the playing screen, settled card");
assert.ok(await A.evaluate(() => { try { return !!localStorage.getItem("gameToken"); } catch { return false; } }));
await A.click("text=Got it"); await B.click("text=Got it");
await A.waitForSelector("text=Next round"); await A.click("text=Next round");

// round 3: timeout; B holds an "other" with a long name
await A.click("text=Draw your character"); await B.click("text=Draw your character");
await A.waitForSelector(".time"); await B.waitForSelector(".card.revealing");
assert.deepEqual(await cardStyle(B), [rgb("#3b3540"), rgb("#f3e5c8")]); ok("other: neutral stone card, light ink");
await B.setViewportSize({ width: 360, height: 640 }); await shot(B, "13c-long-name-mid-360x640"); await fitsPhones(B, "long name with Hint", true);
await B.waitForSelector(".secret .card:not(.revealing)", { timeout: 4000 }); await shot(B, "13d-long-name-settled-360x640"); await B.setViewportSize({ width: 412, height: 915 });
await A.waitForSelector("text=Back to lobby", { timeout: 15000 }); ok("timeout: nobody pressed, round ended by the clock");
assert.match(await A.locator(".verdict").innerText(), /\+0[\s\S]*\+0/); ok("timeout gains are +0 and +0");
await shot(A, "14-final-timeout");
await shot(A, "15-final-A"); await shot(B, "15-final-B"); await fitsPhones(A, "final", true);
await B.click("text=Back to lobby"); await A.waitForSelector("text=Start"); 
await A.click("text=Past games"); await A.waitForSelector(".hist"); await shot(A, "16-history-with-game"); await fitsPhones(A, "history with a game", false);

// lost banner: offline while open
await A.context().setOffline(true); await A.evaluate(() => document.getElementById("lost").hidden = false);
await shot(A, "17-connection-lost"); await A.context().setOffline(false);

// 6: reduced motion
const normal = await phone(); const reduced = await phone({ width: 412, height: 915 }, { reducedMotion: "reduce" });
const [an, ar] = [(await anims(normal)).length, (await anims(reduced)).length];
assert.ok(an > 0); assert.equal(ar, 0); ok(`join animations: normal=${an}, reduced-motion=${ar}`);
await browser.close();
console.log("ALL PASS");

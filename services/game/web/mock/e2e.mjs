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
const cardStyle = (p, sel = "#screen > .hall") => p.locator(sel).first().evaluate(e => [getComputedStyle(e).backgroundColor, getComputedStyle(e).color]);
// The card art: the kind's emblem, then the dark banner carrying the name on top of it, and nothing else (no rune watermark).
const carts = (p, sel = ".secret .cart") => p.locator(sel).evaluateAll(cs => cs.map(c => {
  const b = c.querySelector(".banner"), e = c.querySelector(".emblem"), r = b.getBoundingClientRect(), er = e.getBoundingClientRect();
  const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
  return [[...c.children].map(k => k.getAttribute("class")).join(" + "), getComputedStyle(b).backgroundColor, !!b.querySelector(".sname"),
    !!hit && b.contains(hit) && er.top < r.top && er.bottom > r.bottom, c.querySelectorAll("svg.rune, text").length];
}));
const art = (emblem) => [`emblem ${emblem} + banner`, rgb("#140d18"), true, true, 0];
// Every name stays inside its banner at 320 and 360 wide with the big font; it wraps on spaces, never runs under the swallowtail.
const namesFit = async (p, label) => {
  const back = p.viewportSize(), tag = await p.addStyleTag({ content: BIG });
  for (const width of [360, 320]) {
    await p.setViewportSize({ width, height: 640 });
    const bad = await p.evaluate(() => [...document.querySelectorAll(".sname")].filter(n => n.scrollWidth > n.clientWidth || n.getBoundingClientRect().right > n.parentElement.getBoundingClientRect().right).map(n => n.textContent));
    assert.deepEqual(bad, [], `${label} ${width}px big font: names overflow their banner`);
  }
  await shot(p, `names-${label.replace(/\W+/g, "-")}-320x640-big`); await tag.evaluate(t => t.remove()); await p.setViewportSize(back);
  ok(`${label}: every name fits its banner at 360 and 320 with the big font`);
};
const settled = p => p.locator(".secret .slot:not(.spin)").count();
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
const PHONES = [{ width: 320, height: 640 }, { width: 360, height: 640 }, { width: 360, height: 780 }, { width: 412, height: 915 }];
// A Samsung-like phone: bigger root font and a wider serif than desktop Chromium's.
const BIG = 'html{font-size:125% !important;font-family:"DejaVu Serif",serif !important}';
const scene = p => p.evaluate(() => [".title", ".hearth"].map(s => !!document.querySelector(s).getClientRects().length));
// Every control stays inside the painting and nothing scrolls sideways, at narrow widths with the big font.
const wraps = async (p, label) => {
  const back = p.viewportSize(), tag = await p.addStyleTag({ content: BIG });
  for (const width of [320, 360, 412]) {
    await p.setViewportSize({ width, height: 780 });
    const bad = await p.evaluate(w => {
      const box = document.getElementById("screen"), b = box.getBoundingClientRect(), out = [];
      if (document.documentElement.scrollWidth > w) out.push(`page ${document.documentElement.scrollWidth}>${w}`);
      if (box.scrollWidth > box.clientWidth) out.push(`panel ${box.scrollWidth}>${box.clientWidth}`);
      for (const e of box.querySelectorAll("button, output, input")) {
        const r = e.getBoundingClientRect();
        if (r.left < b.left - 0.5 || r.right > b.right + 0.5) out.push(`"${e.textContent}" ${r.left | 0}..${r.right | 0} outside ${b.left | 0}..${b.right | 0}`);
        if (e.scrollWidth > e.clientWidth) out.push(`"${e.textContent}" text ${e.scrollWidth}>${e.clientWidth}`);
      }
      return out;
    }, width);
    assert.deepEqual(bad, [], `${label} ${width}px big font`);
    await shot(p, `wrap-${label.replace(/\W+/g, "-")}-${width}-big`);
  }
  await tag.evaluate(t => t.remove()); await p.setViewportSize(back);
  ok(`${label}: wraps inside the panel at 320, 360, 412 with the big font`);
};
const end = p => p.locator("#screen button.end");
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
assert.deepEqual(await scene(A), [true, true]); ok("join: title and fireplace shown");
assert.equal(await end(A).count(), 0); ok("join: no End game");
const fire = await anims(A);
assert.ok(fire.length >= 1 && fire.length <= 6 && fire.every(a => a.fire && a.state === "running"), JSON.stringify(fire));
assert.deepEqual([...new Set(fire.flatMap(a => a.props))].sort(), ["opacity", "transform"]);
ok(`join: ${fire.length} running animations, all in the fireplace, animating only opacity and transform`);
await setHidden(A, true); assert.ok((await anims(A)).every(a => a.state === "paused")); ok("hidden tab: fire paused");
await setHidden(A, false); assert.ok((await anims(A)).every(a => a.state === "running")); ok("visible again: fire running");
await A.fill("input", "Ana"); await A.click("text=Join");
await A.waitForSelector("text=Waiting for a second rider"); await shot(A, "02a-lobby-alone"); assert.ok(!(await text(A)).includes("null"), "no stray null on screen"); await fitsPhones(A, "lobby one player", true);
await B.fill("input", "Bo"); await B.click("text=Join");
await A.waitForSelector("text=Bo"); await shot(A, "02-lobby"); await fitsPhones(A, "lobby", true);
assert.deepEqual(await scene(A), [false, false]); assert.equal(await end(A).count(), 0); ok("lobby: no title, no fireplace, no End game");
await wraps(A, "lobby");

// settings: either phone changes the options, the other sees them live
const summary = p => p.locator(".summary").innerText();
const pressed = p => p.locator('#screen [aria-pressed="true"]').allInnerTexts();
const tap = (p, label) => p.click(`#screen button:text-is("${label}")`);
assert.equal(await summary(B), "3 rounds, 3:00, hints on, all"); ok("lobby summary shows the defaults");
await tap(A, "Settings"); await A.waitForSelector("text=Length"); await tap(B, "Settings");
assert.deepEqual(await pressed(A), ["3:00", "On", "All"]); ok("settings: defaults pressed");
assert.equal(await A.locator("#screen button:visible").count(), 10); ok("settings: 10 controls (stepper, 3 lengths, hints, 3 pools, back)");
await fitsPhones(A, "settings", true);
assert.deepEqual(await scene(A), [false, false]); ok("settings: no title, no fireplace");
await wraps(A, "settings");
await tap(A, "2:00"); await tap(A, "Dragons"); await A.click('button[aria-label="Hints"]'); await A.click('[aria-label="More rounds"]');
await B.waitForSelector('button[aria-label="Hints"][aria-pressed="false"]');
await B.waitForFunction(() => document.querySelector("#screen output")?.textContent === "4");
assert.deepEqual(await pressed(B), ["2:00", "Dragons"]); ok("A's changes show on B's settings screen");
await fitsPhones(B, "settings changed", true);
await tap(B, "Back"); assert.equal(await summary(B), "4 rounds, 2:00, hints off, dragons"); ok("B's lobby summary reflects the settings");
await tap(A, "3:00"); await tap(A, "All"); await A.click('button[aria-label="Hints"]'); await A.click('[aria-label="Fewer rounds"]');
await B.waitForFunction(() => document.querySelector(".summary")?.textContent === "3 rounds, 3:00, hints on, all"); ok("changes back to defaults reach B's lobby");
await tap(A, "Back"); await A.waitForSelector(".summary");
await A.click("text=Past games"); await A.waitForSelector("text=No games yet."); await shot(A, "03-history"); await fitsPhones(A, "history", false);
await A.click("text=Roll up the scroll");
await A.click("text=Start"); await A.waitForSelector("#fly:not([hidden])", { state: "attached" }); ok("the dragon shadow crosses on a screen change");
await A.evaluate(() => document.getAnimations().filter(a => a.effect.getTiming().iterations !== Infinity).forEach(a => a.finish())); // the animation ending before the swap timer must still paint the new screen
await A.waitForSelector("text=Draw your character", { timeout: 300 }); assert.equal(await A.locator("#fly").isHidden(), true); ok("sweep ended early: the new screen is painted, the overlay is gone");
await A.waitForSelector("text=Draw your character"); await shot(A, "04-drawing"); await fitsPhones(A, "drawing", true);
assert.equal(await end(A).innerText(), "End game"); ok("drawing: End game shown");
assert.ok((await anims(A)).every(a => !a.fire)); ok("drawing: the fireplace is gone and animates nothing");

// 4: B (no clip) reveals by runes; A (clip) draws mid-animation and must not restart it
await B.click("text=Draw your character");
await B.waitForSelector(".secret.revealing"); ok("no clip: B gets the rune reveal");
assert.equal(await B.locator(".secret .sname").innerText(), "Violet Sorrengail"); ok("name is real text from the start");
assert.equal(await B.locator(".secret .sname .slot").count(), 16); ok("one line of 16 letter slots, in the name itself");
assert.deepEqual(await cardStyle(B), [rgb("#1F2421"), rgb("#f3e5c8")]); ok("Violet card #1F2421 with light ink");
assert.deepEqual(await carts(B), [art("blades")]); ok("rider: crossed blades behind the dark banner, name on the banner, no rune watermark");
assert.ok(await B.locator(".hints").isHidden()); ok("no Hint while drawing");
await B.waitForFunction(() => document.querySelectorAll(".secret .slot:not(.spin)").length >= 2);
await B.evaluate(() => { document.querySelector(".secret").marked = true; });
const mid = await B.evaluate(() => [...document.querySelectorAll(".secret .slot")].map(s => [s.classList.contains("spin"), getComputedStyle(s).backgroundImage.includes("svg"), s.textContent]));
assert.ok(mid.some(m => m[0]) && mid.some(m => !m[0]), JSON.stringify(mid));
assert.ok(mid.every(([spin, rune]) => spin === rune), "a spinning slot shows a rune, a settled slot only its letter");
const runeInk = await B.evaluate(() => decodeURIComponent(document.querySelector(".secret .slot.spin")?.style.backgroundImage || ""));
assert.match(runeInk, /color="#[0-9a-f]{6}"/, "the cycling rune is painted in the card ink, not left black"); ok("cycling runes carry the card ink");
assert.equal(mid.map(m => m[2]).join(""), "VioletSorrengail");
assert.equal(await B.locator(".secret .sname").innerText(), "Violet Sorrengail"); ok(`mid-reveal: ${mid.filter(m => !m[0]).length} letters settled, ${mid.filter(m => m[0]).length} runes still turning, all in the name line`);
const before = await settled(B);
await shot(B, "05-reveal-runes-mid");
await A.click("text=Draw your character");
await A.waitForSelector(".stage video"); ok("clip present: A plays a <video>");
assert.equal(await A.locator(".stage video").evaluate(v => v.muted), true); ok("video is muted");
assert.deepEqual(await carts(A), [art("claws")]); ok("dragon: claw gouges behind the banner");
await shot(A, "06-reveal-clip");
await B.waitForSelector(".time");
assert.ok(await B.evaluate(() => document.querySelector(".secret").marked)); assert.ok(await B.locator(".secret.revealing").count());
assert.ok((await settled(B)) >= before); ok(`opponent's draw mid-animation: same card node, still revealing, settled runes ${before} -> ${await settled(B)}`);
await B.click("text=Hint (3 left)");
assert.match((await B.locator(".tips li").allInnerTexts()).join("|"), /^Violet vague tip\.[^|]*$/); await B.waitForSelector("text=Hint (2 left)");
assert.ok(await B.locator(".secret.revealing").count()); ok("Hint works during the animation, vague tip first");
assert.ok(!(await text(A)).includes("Violet vague tip")); ok("A never sees B's tips");
await B.waitForSelector(".secret:not(.revealing)", { timeout: 4000 });
assert.equal(await B.locator(".secret .slot").count(), 0); assert.equal(await timers(B), 1);
assert.equal(await B.locator(".secret .sname").innerText(), "Violet Sorrengail"); ok("reveal settles to the plain name, no rune left, only the clock interval left");
await B.reload(); await B.waitForSelector(".time");
assert.equal(await B.locator(".secret.revealing").count(), 0); assert.equal(await B.locator(".slot").count(), 0);
assert.equal(await B.locator(".tips li").count(), 1); await B.waitForSelector("text=Hint (2 left)"); ok("reload: settled card, no animation, revealed tip kept");
await B.click("text=Hint (2 left)"); await B.click("text=Hint (1 left)");
assert.equal(await B.locator(".tips li").count(), 3); assert.equal(await B.locator("button.hint:visible").count(), 0); ok("all tips shown, Hint gone");
await fitsPhones(B, "playing, three tips open", true);
{
  await B.setViewportSize({ width: 360, height: 560 }); const tag = await B.addStyleTag({ content: BIG });
  const [sh, ch] = await B.locator(".well").evaluate(e => [e.scrollHeight, e.clientHeight]);
  assert.ok(sh > ch, `card and tips must overflow for this check to mean anything: ${sh} <= ${ch}`);
  const seen = new Set(), box = await B.locator(".well").boundingBox();
  await B.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  for (let i = 0; i < 30 && seen.size < 4; i++) {
    const vis = await B.evaluate(() => [...document.querySelectorAll(".tips li")].map(li => {
      const r = li.getBoundingClientRect(), s = li.closest(".well").getBoundingClientRect(), y = r.y + r.height / 2;
      const hit = document.elementFromPoint(r.x + r.width / 2, y);
      return [y > s.top && y < s.bottom && !!hit && li.contains(hit), r.bottom <= s.bottom];
    }));
    vis.forEach(([v], k) => v && seen.add(k)); if (vis[2][1]) seen.add("end");
    await B.mouse.wheel(0, 60); await B.waitForTimeout(50);
  }
  assert.equal(seen.size, 4, `tips (and the end of the last) reached by scrolling: ${[...seen]}`);
  await shot(B, "07-playing-B-tips-scrolled-360x560-big");
  const fixed = await B.evaluate(() => ["#timer", "#screen .plinth button"].flatMap(s => [...document.querySelectorAll(s)]).map(e => {
    const r = e.getBoundingClientRect(), hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
    return [e.textContent, r.top >= 0 && r.bottom <= innerHeight && !!hit && e.contains(hit)];
  }));
  assert.equal(fixed.length, 3); assert.ok(fixed.every(f => f[1]), JSON.stringify(fixed));
  assert.ok(await B.evaluate(() => document.documentElement.scrollWidth <= 360));
  ok("360x560, big font, three long tips: all reachable by scrolling, timer, Got it and Forfeit always visible, no sideways scroll");
  await tag.evaluate(t => t.remove()); await B.setViewportSize({ width: 412, height: 915 });
}
await A.waitForSelector(".stage img", { timeout: 5000 }); ok("after the clip ends A holds the poster");
await A.waitForSelector(".time"); await shot(A, "07-playing-A"); await shot(B, "07-playing-B");
await B.setViewportSize({ width: 360, height: 640 }); await shot(B, "07-playing-B-tips-360x640"); await B.setViewportSize({ width: 412, height: 915 });

// 5: no leak of the other's secret
const tA = await text(A), tB = await text(B);
assert.ok(tA.includes("Tairn") && !tA.includes("Violet Sorrengail")); assert.ok(tB.includes("Violet Sorrengail") && !tB.includes("Tairn"));
ok("playing: A shows only Tairn, B shows only Violet Sorrengail");

// 2: fit
await fits(A, "playing 412x915", true);
await A.setViewportSize({ width: 360, height: 640 }); await fits(A, "playing 360x640", true); await shot(A, "09-playing-360x640");
await A.setViewportSize({ width: 1366, height: 768 }); await fits(A, "playing 1366x768 (sideways only)", false); await shot(A, "10-playing-desktop");
await A.setViewportSize({ width: 412, height: 915 });

// Hide on B: name, emblem, kind and tips blank, the card face stays, and it holds through A's Got it
const blank = p => p.evaluate(() => [".secret .sname", ".secret .emblem", ".secret .kind", ".tips"].map(s => getComputedStyle(document.querySelector(s)).visibility));
await B.click("button.conceal");
assert.deepEqual(await blank(B), ["hidden", "hidden", "hidden", "hidden"]);
assert.deepEqual(await cardStyle(B), [rgb("#1F2421"), rgb("#f3e5c8")]);
assert.ok(!(await text(B)).includes("Violet")); assert.equal(await B.locator("button.conceal").textContent(), "Show"); ok("Hide blanks name, emblem, kind and tips; the card face stays");
await shot(B, "08-hide-on");

// A got it, B forfeits
await A.click("text=Got it"); await A.waitForSelector("text=You got it"); await shot(A, "11-playing-resolved");
await B.waitForSelector("text=Ana got it");
assert.deepEqual(await blank(B), ["hidden", "hidden", "hidden", "hidden"]); ok("Hide survives a re-render from the other phone's event");
await B.click("button.conceal"); assert.deepEqual(await blank(B), ["visible", "visible", "visible", "visible"]); ok("a second tap shows it again");
await B.click("text=Forfeit");
await A.waitForSelector("text=Next round"); await shot(A, "12-roundEnd-A"); await shot(B, "12-roundEnd-B"); await fitsPhones(A, "roundEnd", true);
assert.ok((await text(A)).includes("Violet Sorrengail")); ok("roundEnd reveals the other secret");
assert.equal(await end(A).innerText(), "End game"); ok("roundEnd: End game shown");
assert.equal(await B.locator(".tips, .hint").count(), 0); ok("no tips at roundEnd");
assert.deepEqual(await cardStyle(A, ".verdict .mini"), [rgb("#1F2421"), rgb("#f3e5c8")]); ok("roundEnd cards carry the reveal colour");
assert.deepEqual(await carts(A, ".verdict .cart"), [art("blades"), art("claws")]); ok("roundEnd minis: the same emblem and banner per kind");
await namesFit(A, "roundEnd");
assert.deepEqual([await timers(A), await timers(B)], [1, 1]); ok("roundEnd: no reveal timer left on either phone");

// refusal for a third phone
const C = await phone(); await C.fill("input", "Late"); await C.click("text=Join");
await C.waitForSelector(".refused"); await shot(C, "13-refused"); await fitsPhones(C, "refused", true); ok("third player refused: " + (await C.locator(".refused p").innerText()));

// round 2: B reduced motion, A animates, then A reloads mid-round
await B.emulateMedia({ reducedMotion: "reduce" });
await B.click("text=Next round"); await A.waitForSelector("text=Draw your character");
await B.click("text=Draw your character"); await B.waitForSelector(".secret");
assert.equal(await B.locator(".secret.revealing, .slot").count(), 0); assert.equal(await timers(B), 1); ok("reduced motion: settled at once, no timer");
assert.equal((await anims(B)).length, 0); ok("reduced motion: no embers or other animation while playing");
await B.emulateMedia({ reducedMotion: "no-preference" });
await A.click("text=Draw your character"); await A.waitForSelector(".secret.revealing");
assert.deepEqual(await cardStyle(A), [rgb("#FFD400"), rgb("#1a0e05")]); ok("Andarna card #FFD400 with dark ink");
await A.waitForSelector(".time"); await shot(A, "13b-andarna-mid");
await A.reload(); await A.waitForSelector(".time");
assert.equal(await A.locator("#fly").isHidden(), true); ok("reload: the restored screen is painted at once, no sweep over the join form");
assert.equal(await A.locator(".secret.revealing, .slot").count(), 0); ok("rejoin: reload restored the playing screen, settled card");
assert.ok(await A.evaluate(() => { try { return !!localStorage.getItem("gameToken"); } catch { return false; } }));
await A.click("text=Got it"); await B.click("text=Got it");
await A.waitForSelector("text=Next round"); await A.click("text=Next round");

// round 3: timeout; B holds an "other" with a long name
await A.click("text=Draw your character"); await B.click("text=Draw your character");
await A.waitForSelector(".time"); await B.waitForSelector(".secret.revealing");
assert.deepEqual(await cardStyle(B), [rgb("#3b3540"), rgb("#f3e5c8")]); ok("other: neutral stone card, light ink");
assert.deepEqual(await carts(B), [art("slab")]); ok("other: the plain cracked slab behind the banner");
await B.setViewportSize({ width: 360, height: 640 }); await shot(B, "13c-long-name-mid-360x640"); await fitsPhones(B, "long name with Hint", true);
await B.waitForSelector(".secret:not(.revealing)", { timeout: 4000 }); await shot(B, "13d-long-name-settled-360x640"); await B.setViewportSize({ width: 412, height: 915 });
{
  const tag = await B.addStyleTag({ content: BIG });
  for (const width of [360, 320]) {
    await B.setViewportSize({ width, height: 640 });
    const [sw, cw, lines] = await B.locator(".secret .banner").evaluate(b => { const n = b.querySelector(".sname"); return [b.scrollWidth, b.clientWidth, Math.round(n.clientHeight / parseFloat(getComputedStyle(n).lineHeight))]; });
    assert.ok(sw <= cw && lines >= 2, `${width}px: banner ${sw}>${cw} or ${lines} line(s)`);
  }
  await shot(B, "13e-long-name-320x640-big"); await tag.evaluate(t => t.remove()); await B.setViewportSize({ width: 412, height: 915 });
  ok("a 21-letter name wraps on spaces inside the banner at 320 and 360 with the big font");
}
await A.waitForSelector("text=Back to lobby", { timeout: 15000 }); ok("timeout: nobody pressed, round ended by the clock");
const verdict = p => p.locator(".finale").getAttribute("data-result");
const [va, vb] = [await verdict(A), await verdict(B)];
assert.deepEqual([va, vb], { win: ["win", "lose"], lose: ["lose", "win"], tie: ["tie", "tie"] }[va]); ok(`the finale agrees on both phones: ${va} / ${vb}`);
await shot(A, "14-final-timeout");
await shot(A, "15-final-A"); await shot(B, "15-final-B"); await namesFit(B, "final"); await fitsPhones(A, "final", true);
assert.equal(await end(A).count(), 0); ok("final: no End game");
await B.click("text=Back to lobby"); await A.waitForSelector("text=Start"); 
await A.click("text=Past games"); await A.waitForSelector(".hist"); await shot(A, "16-history-with-game"); await fitsPhones(A, "history with a game", false);

// hints off: no Hint button and no tips on either phone; options survived back_to_lobby
await A.click("text=Roll up the scroll");
assert.equal(await summary(A), "3 rounds, 3:00, hints on, all");
await tap(A, "Settings"); await A.click('button[aria-label="Hints"]'); await A.waitForSelector('button[aria-label="Hints"][aria-pressed="false"]'); await tap(A, "Back");
await B.waitForFunction(() => document.querySelector(".summary")?.textContent === "3 rounds, 3:00, hints off, all");
await A.click("text=Start"); await A.click("text=Draw your character"); await B.click("text=Draw your character");
await A.waitForSelector(".time"); await B.waitForSelector(".time");
assert.deepEqual([await A.locator(".hints, button.hint").count(), await B.locator(".hints, button.hint").count()], [0, 0]); ok("hints off: no Hint button on either phone");
await shot(B, "16b-playing-hints-off");

// lost banner: offline while open
await A.context().setOffline(true); await A.evaluate(() => document.getElementById("lost").hidden = false);
await shot(A, "17-connection-lost"); await A.context().setOffline(false);
await A.waitForSelector("#lost", { state: "hidden" });

// End game: one tap only arms it, it reverts after 4 s, two taps end it for both phones
await A.waitForSelector("text=Next round", { timeout: 15000 }); await A.click("text=Next round"); await B.waitForSelector("text=Draw your character");
const games = async () => (await (await fetch(`${BASE}/api/history`)).json()).length, before0 = await games();
await end(A).click(); const armedAt = Date.now();
assert.equal(await end(A).innerText(), "Really end?");
await B.click("text=Draw your character"); await A.waitForSelector("text=Bo has drawn.");
assert.equal(await end(A).innerText(), "Really end?"); ok("one tap arms End game; an opponent event does not disarm it");
await A.waitForFunction(() => document.querySelector("button.end")?.textContent === "End game", null, { timeout: 6000 });
const armedFor = Date.now() - armedAt;
assert.ok(armedFor > 3500 && armedFor < 5000, `reverted after ${armedFor} ms`);
assert.ok(await A.locator("text=Draw your character").isVisible()); ok(`a single tap reverts after ${armedFor} ms and the game goes on`);
await end(B).click(); await shot(B, "18-end-armed"); await end(B).click();
await A.waitForSelector("text=Bo ended the game"); await B.waitForSelector("text=You ended the game");
await shot(A, "19-ended-A"); await shot(B, "19-ended-B");
assert.deepEqual([await end(A).count(), await end(B).count(), await games()], [0, 0, before0]); ok("two taps: both phones in the lobby with the right notice, no End game, no history line");
await fitsPhones(A, "lobby after end", true);
await tap(B, "Settings"); await B.click('button[aria-label="Hints"]'); await tap(B, "Back");
await A.waitForFunction(() => !document.body.innerText.includes("ended the game")); ok("the next lobby change clears the notice");

// Leave: the stream ending must not raise the banner or rejoin, and no screen state survives
await A.click("text=Past games"); await A.waitForSelector(".hist");
// The server acts (and ends the stream) at once; only the response is late, as on a slow phone network.
await A.route("**/api/action**", async r => { const res = await r.fetch(); await new Promise(f => setTimeout(f, 1500)); await r.fulfill({ response: res }).catch(() => {}); });
await A.click("text=Leave"); await A.waitForTimeout(4000);
assert.deepEqual([await A.locator("#lost").isHidden(), await A.locator("input").isVisible(), await A.evaluate(() => localStorage.getItem("gameToken"))], [true, true, null]);
ok("leave with a slow response: banner hidden and still on the join screen 4 s later"); await A.unroute("**/api/action**");
await A.fill("input", "Ana"); await A.click("text=Join"); await A.waitForSelector(".summary");
assert.equal(await A.locator(".hist").count() + await A.locator("text=The scroll").count(), 0); ok("rejoin after leave: the history scroll is closed");
await B.route("**/api/action**", r => r.abort());
await B.click("text=Leave"); await B.waitForTimeout(4000);
assert.deepEqual([await B.locator("#lost").isHidden(), await B.locator("input").isVisible()], [true, true]);
ok("leave with a failed request: banner hidden and still on the join screen 4 s later");
// The leave never arrived, so Join must reclaim the same slot (same token) instead of adding a ghost beside it.
const tokB = await B.evaluate(() => localStorage.getItem("gameToken")); assert.ok(tokB, "token kept after a lost leave");
await B.unroute("**/api/action**");
await B.fill("input", "Bo"); await B.click("text=Join"); await B.waitForSelector(".summary");
assert.equal(await B.evaluate(() => localStorage.getItem("gameToken")), tokB); ok("lost leave: the next Join reclaims the same slot");

// 6: reduced motion
const normal = await phone(); const reduced = await phone({ width: 412, height: 915 }, { reducedMotion: "reduce" });
const [an, ar] = [(await anims(normal)).length, (await anims(reduced)).length];
assert.ok(an > 0); assert.equal(ar, 0); ok(`join animations: normal=${an}, reduced-motion=${ar}`);
await browser.close();
console.log("ALL PASS");

#!/usr/bin/env node
// Drives the admin page in the sandbox the way the operator would, accepting every confirm dialog,
// and checks what the page shows and what the fakes were asked to do. Run by hub/web/check.sh.
//   node hub/dev/flows.mjs http://127.0.0.1:18090 <screenshot dir>
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const { openPage } = await import(join(homedir(), ".claude/tools/playwright/lib.mjs"));
const [base, shots] = process.argv.slice(2);
const dev = process.env.PIHUB_DEV || join(process.env.TMPDIR || tmpdir(), "pihub-dev");
const scenario = (...a) => execFileSync(fileURLToPath(new URL("scenario.sh", import.meta.url)), a);
const fakeLog = () => readFileSync(join(dev, "fake/log"), "utf8");

const { browser, page, problems } = await openPage(base + "/admin");
page.on("dialog", d => d.accept());
const ok = (cond, what) => { if (!cond) throw new Error("FAILED: " + what); console.log("ok    " + what); };
const textOf = sel => page.locator(sel).innerText();
const until = async (sel, re) => page.waitForFunction(([s, r]) => new RegExp(r).test(document.querySelector(s)?.innerText || ""), [sel, re.source], { timeout: 8000 });
const row = (list, name) => page.locator(`#${list} li`, { has: page.locator("strong", { hasText: new RegExp(`^${name}$`) }) });
const shot = name => page.screenshot({ path: join(shots, `flow-${name}.png`) });

try {
  await page.goto(base + "/admin");
  await page.fill("#pw", "wrong-password");
  await page.click("#login button");
  await until("#err", /Wrong password/);
  ok(true, "wrong login password shows an error");
  await page.fill("#pw", "sandbox-pass");
  await page.click("#login button");
  await page.waitForSelector("#panel:not([hidden])");
  ok(await page.isHidden("#login"), "login form hides after logging in");
  ok(!(await textOf("body")).includes("hint text"), "the hint is not on the page before the bulb is used");

  await row("services", "Music").getByRole("button", { name: "Switch to" }).click();
  await until("#svcmsg", /Done/);
  await until("#services", /Music\s*exclusive\s*Running/);
  ok(/navidrome.service active/.test(readFileSync(join(dev, "fake/units"), "utf8")) && /camera-drive-web inactive/.test(readFileSync(join(dev, "fake/units"), "utf8")),
    "Switch to Music: two-step confirm stops Camera and starts Music");

  await row("saved", "Cafe Free").getByRole("button", { name: "Switch" }).click();
  await page.waitForSelector("#keep:not([hidden])");
  await until("#wifimsg", /Switched\. Open http:\/\/192\.0\.2\.10\/admin/);
  await shot("switch-waiting-for-keep");
  ok(true, "Wi-Fi Switch arms the revert and shows Keep");
  await page.click("#keepbtn");
  await until("#wifimsg", /Kept/);
  await page.waitForSelector("#keep[hidden]", { state: "attached" });
  ok(/systemctl stop pihub-wifi-revert.timer/.test(fakeLog()), "Keep stops the revert timer");

  await page.click("#adddetails summary");
  await page.fill("#ssid", "Test Net");
  await page.fill("#wpw", "long-enough-1");
  await page.click("#add button");
  await until("#saved", /Test Net/);
  ok(/connection add type wifi con-name Test Net/.test(fakeLog()), "Add network saves a profile");
  await row("saved", "Test Net").getByRole("button", { name: "Forget" }).click();
  await page.waitForFunction(() => !/Test Net/.test(document.querySelector("#saved").innerText));
  ok(true, "Forget removes it");

  await page.click("#hotspotbtn");
  await until("#wifimsg", /Hotspot on\. Join camera-drive/);
  await until("#wifimode", /hotspot/);
  ok(await page.isDisabled("#scanbtn"), "hotspot on: scan disabled, mode chip says hotspot");
  await shot("hotspot-on");
  await page.click("#hotspotbtn");
  await page.waitForSelector("#keep:not([hidden])");
  await page.click("#keepbtn");
  await until("#wifimode", /client/);
  ok(true, "Leave hotspot switches back and Keep finishes it");

  scenario("wifi", "fail-up");
  await page.click("#refresh");
  await row("saved", "Office 5G").getByRole("button", { name: "Switch" }).click();
  await until("#wifimsg", /Failed: .*back on "camera-drive-wifi"/);
  await shot("switch-failed");
  ok(true, "a failed switch says where the Pi ended");
  scenario("wifi", "client");

  await page.click('[data-power="reboot"]');
  await until("#powermsg", /going down/);
  ok(/systemctl reboot/.test(fakeLog()), "Reboot goes through the confirm and reaches (fake) systemctl");

  await page.hover("#bulb");
  await until("#hint", /hint text/);
  ok(await page.getAttribute("#bulb", "aria-expanded") === "true", "hovering the bulb shows the hint, aria-expanded true");
  await page.mouse.move(0, 0);
  await page.waitForSelector("#hint", { state: "hidden" });
  await page.focus("#bulb");
  await page.waitForSelector("#hint:not([hidden])");
  await page.keyboard.press("Escape");
  await page.waitForSelector("#hint", { state: "hidden" });
  ok(true, "focus shows it, Escape hides it");

  await page.fill("#cur", "sandbox-pass");
  await page.fill("#new1", "short");
  await page.fill("#new2", "other");
  await page.click("#pwform button");
  ok(/At least 8/.test(await textOf("#e-new1")) && /Does not match/.test(await textOf("#e-new2")) &&
    await page.getAttribute("#new1", "aria-invalid") === "true", "client checks: length and repeat, inline");
  await page.fill("#cur", "not-the-password");
  await page.fill("#new1", "sandbox-pass-2");
  await page.fill("#new2", "sandbox-pass-2");
  await page.click("#pwform button");
  await until("#e-cur", /Wrong current password/);
  await shot("password-wrong-current");
  ok(true, "server 401 shows under Current password");
  await page.fill("#cur", "sandbox-pass");
  await page.fill("#hintin", "hint text 2");
  await page.click("#pwform button");
  await until("#pwmsg", /Password changed/);
  ok(await page.inputValue("#cur") === "" && readFileSync(join(dev, "state/password-hint.txt"), "utf8").trim() === "hint text 2",
    "password changed: fields cleared, new hint stored");
  await shot("password-changed");
  for (let i = 0; i < 6; i++) {
    await page.fill("#cur", "wrong-" + i);
    await page.fill("#new1", "sandbox-pass-3");
    await page.fill("#new2", "sandbox-pass-3");
    await page.click("#pwform button");
    await page.waitForTimeout(300);
  }
  await until("#pwmsg", /Too many attempts/);
  await shot("password-rate-limited");
  ok(true, "server 429 says to wait");

  await page.click("#logout");
  await page.waitForSelector("#login:not([hidden])");
  ok(await page.isHidden("#panel"), "Log out returns to the login form");
  const real = problems.filter(p => !/status of (401|400|429|500)/.test(p));
  ok(real.length === 0, "no page errors" + (real.length ? ": " + real.join("; ") : ""));
} catch (e) {
  console.error(e.message);
  await shot("failure");
  process.exitCode = 1;
} finally {
  await browser.close();
}

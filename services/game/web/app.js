"use strict";
const KEY = "gameToken";
const store = {
  get() { try { return localStorage.getItem(KEY); } catch { return null; } },
  set(v) { try { localStorage.setItem(KEY, v); } catch { /* private mode: the token lives only in memory */ } },
  del() { try { localStorage.removeItem(KEY); } catch { /* nothing to remove */ } },
};
const $ = id => document.getElementById(id);
const h = (tag, attrs = {}, ...kids) => {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (v === true) el.setAttribute(k, "");
    else if (v !== false && v != null) el.setAttribute(k, v);
  }
  el.append(...kids.flat().filter(k => k != null && k !== false));
  return el;
};

let token = store.get(), view = null, receivedAt = 0, es = null, refusal = null, showHistory = false, history = null, showSettings = false;
let secretNode = null, secretKey = "", armedRound = 0, revealTimer = 0, rejoinTimer = 0, endArmed = 0;

async function api(path, body) {
  const r = await fetch(path, body ? { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) } : undefined);
  const j = await r.json().catch(() => ({}));
  return { ok: r.ok, status: r.status, j };
}
// The stream is the only writer of the view: an action's own response can land after a newer event and roll the screen back.
const act = (type, extra) => api(`/api/action?token=${encodeURIComponent(token)}`, { type, ...extra });

function setView(v) {
  view = v; receivedAt = Date.now(); refusal = null;
  render(v);
}

async function join(name) {
  const r = await api("/api/join", { name, token: token || store.get() || undefined });
  if (r.status === 409 && r.j.error === "bonded") { refusal = r.j.message; render(view); return; }
  if (!r.ok) { toJoin(); return; }
  token = r.j.token; store.set(token);
  setView(r.j.view); connect();
}

function connect() {
  if (es) es.close();
  const src = es = new EventSource(`/api/events?token=${encodeURIComponent(token)}`);
  src.addEventListener("view", e => { $("lost").hidden = true; setView(JSON.parse(e.data)); });
  src.onerror = () => {
    if (src !== es) return; // a stream we closed on purpose (leave) still reports its end
    $("lost").hidden = false;
    // A closed stream means the browser gave up (401 after a server restart): rejoin by token ourselves.
    if (src.readyState === EventSource.CLOSED) { clearTimeout(rejoinTimer); rejoinTimer = setTimeout(() => join(view ? view.you.name : ""), 2000); }
  };
}

// Every way back to the join screen comes through here, so no screen state outlives the player.
function toJoin() {
  clearTimeout(rejoinTimer); clearTimeout(endArmed); rejoinTimer = endArmed = 0;
  if (es) es.close();
  es = null; $("lost").hidden = true;
  store.del(); token = null; view = null; refusal = null;
  showHistory = false; history = null; showSettings = false; armedRound = 0;
  try { sessionStorage.removeItem("gameTips"); } catch { /* nothing stored */ }
  render(null);
}

function leave() {
  const t = token;
  toJoin();
  // A leave that never arrived leaves the player in the lobby: keep the token so the next Join reclaims that slot.
  const keep = () => store.set(t);
  api(`/api/action?token=${encodeURIComponent(t)}`, { type: "leave" }).then(r => { if (!r.ok && r.status !== 401) keep(); }, keep);
}

const remaining = () => view.deadline - (view.serverNow + Date.now() - receivedAt);
const fmt = ms => { const s = Math.max(0, Math.ceil(ms / 1000)); return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`; };

function renderBar(v) {
  const bar = $("bar");
  const show = v && v.phase !== "lobby" && v.other;
  bar.hidden = !show;
  if (!show) return;
  const side = (p, cls) => h("div", { class: `who ${cls}` }, h("b", {}, p.name), h("span", {}, String(p.score)),
    p.connected === false ? h("div", { class: "away" }, "away") : null);
  bar.replaceChildren(
    side(v.you, "l"),
    h("div", { class: "mid" }, `Round ${v.round} / ${v.options.rounds}`,
      v.phase === "playing" ? h("span", { class: "time", id: "timer", "aria-live": "off" }, fmt(remaining())) : null),
    side(v.other, "r"));
}

const lum = hex => {
  const c = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(x => (x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
// Ink is whichever of --on-gold and --text contrasts more with the card; mock/check-contrast.mjs runs this rule over every real colour.
const darkInk = c => (lum(c) + 0.05) ** 2 > (lum("#1a0e05") + 0.05) * (lum("#f3e5c8") + 0.05);
const FRAME = '<svg class="frame" viewBox="0 0 1080 1512" fill="none" stroke="currentColor" stroke-width="10" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
  + '<path d="M481 60H204L60 204V651L165 756L60 861V1308L204 1452H876L1020 1308V861L915 756L1020 651V204L876 60H599M483 60L603 180L540 244L476 180L596 60M56 704L108 756L56 808M1024 704L972 756L1024 808"/>'
  + '<path d="M540 138L582 180L540 222L498 180Z" fill="currentColor" stroke="none"/></svg>';
const card = (c, ...kids) => {
  const el = h("div", { class: `card${c.color && darkInk(c.color) ? " ink-dark" : ""}`, style: c.color ? `--c:${c.color}` : null }, ...kids);
  el.insertAdjacentHTML("afterbegin", FRAME);
  return el;
};

const ABC = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";
// Each letter becomes a slot that keeps its real letter (unpainted) under a cycling rune background, so the line is the final name from the start.
function startReveal(card, name) {
  const el = card.querySelector(".sname"), slots = [], ink = getComputedStyle(el).color;
  const img = [...ABC].map(c => `url("data:image/svg+xml,${encodeURIComponent(runeSvg(c).replace("<svg", `<svg xmlns="http://www.w3.org/2000/svg" color="${ink}"`))}")`);
  el.replaceChildren(...name.split(" ").flatMap((w, i) => [i ? " " : null, h("span", { class: "word" }, [...w].map(ch => {
    const s = h("span", { class: "slot spin" }, ch);
    slots.push([s, ch]);
    return s;
  }))]).filter(Boolean));
  const n = slots.length, total = Math.min(3500, 1600 + n * 90), step = (total - 900) / n, t0 = performance.now();
  card.classList.add("revealing");
  const tick = () => {
    const t = performance.now() - t0;
    slots.forEach(([s, ch], i) => {
      if (!s.classList.contains("spin")) return;
      if (t >= 600 + (i + 1) * step) { s.style.backgroundImage = ""; s.classList.remove("spin"); return; }
      s.style.backgroundImage = img[Math.floor(Math.random() * 26)];
    });
    if (t >= total) stopReveal();
  };
  revealTimer = setInterval(tick, 70);
  tick();
}
function stopReveal() {
  clearInterval(revealTimer); revealTimer = 0;
  const c = secretNode && secretNode.querySelector(".card");
  if (!c || !c.classList.contains("revealing")) return;
  c.classList.remove("revealing");
  const sn = c.querySelector(".sname");
  sn.textContent = sn.textContent; // every slot holds its real letter, so this leaves the plain name

}

function hints(tips, key) {
  let n = 0;
  try { const t = JSON.parse(sessionStorage.getItem("gameTips")); if (t && t.k === key) n = t.n; } catch { /* no storage: tips reset on reload */ }
  const list = h("ol", { class: "tips", "aria-live": "polite" });
  const more = btn("", () => {
    n++;
    try { sessionStorage.setItem("gameTips", JSON.stringify({ k: key, n })); } catch { /* kept in memory for this page only */ }
    draw();
  }, "hint");
  const draw = () => {
    list.replaceChildren(...tips.slice(0, n).map(t => h("li", {}, t)));
    more.textContent = `Hint (${tips.length - n} left)`;
    more.hidden = n >= tips.length;
  };
  draw();
  return h("div", { class: "hints" }, list, more);
}

function secretCard(v) {
  const s = v.secret, key = `${v.round}:${s.name}`;
  if (!secretNode || secretKey !== key) {
    stopReveal();
    secretKey = key;
    let face = null;
    if (s.clip) {
      const stage = h("div", { class: "stage" });
      const fallback = () => stage.remove();
      const video = h("video", { src: s.clip, poster: s.poster, muted: true, playsinline: true, autoplay: true, "aria-hidden": "true" });
      video.muted = true; // the attribute alone is not honoured for autoplay in every browser
      video.addEventListener("ended", () => s.poster ? stage.replaceChildren(h("img", { src: s.poster, alt: "" })) : fallback());
      video.addEventListener("error", fallback);
      stage.append(video);
      face = stage;
    }
    const front = card(s, h("div", { class: "skind" }, s.kind), face, h("div", { class: "sname" }, s.name));
    const body = h("div", { class: "sbody" }, front, s.tips ? hints(s.tips, key) : null);
    const toggle = h("button", { "aria-expanded": "true", onclick: () => {
      const c = secretNode.classList.toggle("collapsed");
      toggle.setAttribute("aria-expanded", String(!c));
      toggle.lastChild.textContent = c ? "show" : "hide";
    } }, h("span", {}, "Your secret"), h("span", {}, "hide"));
    secretNode = h("div", { class: "secret" }, toggle, body);
    // Animate only a draw this page saw happen: a reload mid-round shows the settled card.
    if (!s.clip && armedRound === v.round && !matchMedia("(prefers-reduced-motion: reduce)").matches) startReveal(front, s.name);
  }
  const hb = secretNode.querySelector(".hints");
  if (hb) hb.hidden = v.phase !== "playing";
  return secretNode;
}

const btn = (label, onclick, cls = "", extra = {}) => h("button", { class: cls, onclick, ...extra }, label);
const panel = (...kids) => h("div", { class: "panel" }, ...kids);
// Two taps on purpose: the first arms it for 4 s, only the second ends the game for both phones.
const endGame = () => h("button", { class: "link end", onclick: () => {
  if (endArmed) { clearTimeout(endArmed); endArmed = 0; act("end_game"); return; }
  endArmed = setTimeout(() => { endArmed = 0; render(view); }, 4000);
  render(view);
} }, endArmed ? "Really end?" : "End game");

const reveals = v => h("div", { class: "verdict" },
  [[v.reveal.yourGuess, "You guessed", v.gain.you], [v.reveal.theirGuess, `${v.other.name} guessed`, v.gain.other]].map(([c, label, gain]) =>
    panel(h("small", {}, label), card(c, h("div", { class: "skind" }, c.kind), h("strong", { class: "sname" }, c.name)), h("p", {}, `+${gain}`))));

const screens = {
  join() {
    const input = h("input", { maxlength: "20", autocomplete: "nickname", "aria-label": "Your name", placeholder: "Your name", required: true });
    return h("form", { class: "panel", onsubmit: e => { e.preventDefault(); if (input.value.trim()) join(input.value.trim()); } },
      h("h2", {}, "Take your place"), input, h("p"), h("button", { class: "primary big" }, "Join"));
  },
  lobby(v) {
    const o = v.options;
    const hist = showHistory ? panel(h("h2", {}, "The scroll"), history == null ? h("p", { class: "muted" }, "Unrolling...")
      : history.length === 0 ? h("p", { class: "muted" }, "No games yet.")
      : h("ul", { class: "hist" }, history.map(g => h("li", {}, h("span", {}, g.players.map(p => `${p.name} ${p.score}`).join(" - ")),
        h("span", { class: "muted" }, `${g.rounds}r, ${new Date(g.finished).toLocaleDateString()}`))))) : null;
    return [
      panel(h("h2", {}, "Lobby"), h("p", {}, `${v.you.name} (you)`),
        h("p", { class: v.other ? "" : "muted" }, v.other ? v.other.name : "Waiting for a second rider..."),
        h("p", { class: "muted summary" }, `${o.rounds} round${o.rounds > 1 ? "s" : ""}, ${fmt(o.seconds * 1000)}, hints ${o.hints ? "on" : "off"}, ${o.pool}`),
        v.endedBy ? h("p", { class: "notice" }, v.endedBy === v.you.name ? "You ended the game" : `${v.endedBy} ended the game`) : null),
      btn("Settings", () => { showSettings = true; showHistory = false; render(view); }),
      btn("Start", () => act("start"), "primary big", { disabled: !v.canStart }),
      hist,
      h("div", { class: "row" },
        btn(showHistory ? "Roll up the scroll" : "Past games", toggleHistory, "link"),
        btn("Leave", leave, "link")),
    ];
  },
  settings(v) {
    const o = v.options, set = x => act("set_options", x);
    const row = (label, ...kids) => h("div", { class: "opt", role: "group", "aria-label": label }, h("span", {}, label), h("div", { class: "seg" }, ...kids));
    const choice = (key, val, label) => btn(label, () => set({ [key]: val }), "", { "aria-pressed": String(o[key] === val) });
    return panel(h("h2", {}, "Settings"),
      row("Rounds",
        btn("-", () => set({ rounds: o.rounds - 1 }), "", { disabled: o.rounds <= 1, "aria-label": "Fewer rounds" }),
        h("output", {}, String(o.rounds)),
        btn("+", () => set({ rounds: o.rounds + 1 }), "", { disabled: o.rounds >= o.maxRounds, "aria-label": "More rounds" })),
      row("Length", choice("seconds", 120, "2:00"), choice("seconds", 180, "3:00"), choice("seconds", 300, "5:00")),
      row("Hints", btn(o.hints ? "On" : "Off", () => set({ hints: !o.hints }), "", { "aria-pressed": String(o.hints), "aria-label": "Hints" })),
      row("Pool", choice("pool", "all", "All"), choice("pool", "riders", "Riders"), choice("pool", "dragons", "Dragons")),
      btn("Back", () => { showSettings = false; render(view); }, "back"));
  },
  drawing(v) {
    if (!v.drawn.you) armedRound = v.round;
    return [
      v.drawn.you ? secretCard(v) : panel(h("h2", {}, "A new round"), h("p", { class: "muted" }, "Your secret is waiting. Keep it from the other player."),
        btn("Draw your character", () => act("draw"), "primary big")),
      h("p", { class: "status" }, v.drawn.other ? `${v.other.name} has drawn.` : `Waiting for ${v.other.name} to draw...`),
    ];
  },
  playing(v) {
    const done = v.resolved.you;
    const them = v.resolved.other;
    return [
      secretCard(v),
      h("p", { class: `status ${them === "got_it" ? "ok" : ""}` },
        them === "got_it" ? `${v.other.name} got it.` : them === "forfeit" ? `${v.other.name} gave up.` : `${v.other.name} is still guessing...`),
      h("div", { class: "spacer" }),
      done ? h("p", { class: "status ok" }, done === "got_it" ? "You got it. Waiting for the round to end..." : "You forfeited. Waiting for the round to end...")
        : h("div", { class: "row" }, btn("Got it", () => act("got_it"), "primary big"), btn("Forfeit", () => act("forfeit"), "big")),
    ];
  },
  roundEnd: v => [reveals(v), btn("Next round", () => act("next"), "primary big")],
  final(v) {
    const f = v.final;
    return [
      reveals(v),
      panel(h("h2", {}, f.tie ? "A tie" : `${f.winner} wins`), h("p", {}, `${v.you.name} ${v.you.score} - ${v.other.score} ${v.other.name}`)),
      btn("Back to lobby", () => act("back_to_lobby"), "primary big"),
    ];
  },
};

async function toggleHistory() {
  showHistory = !showHistory;
  if (showHistory) { history = null; render(view); const r = await api("/api/history"); history = r.ok ? r.j : []; }
  render(view);
}

function render(v) {
  document.body.classList.toggle("home", !v || v.phase === "lobby");
  document.body.classList.toggle("scene", !v);
  renderBar(v);
  const screen = $("screen");
  const endable = !!v && (v.phase === "drawing" || v.phase === "playing" || v.phase === "roundEnd");
  if (!v || !(v.phase === "drawing" || v.phase === "playing")) { stopReveal(); secretNode = null; secretKey = ""; }
  if (!endable) { clearTimeout(endArmed); endArmed = 0; }
  if (refusal) {
    screen.replaceChildren(h("div", { class: "panel refused" }, h("h2", {}, "Refused"), h("p", {}, refusal),
      btn("Back", () => { refusal = null; render(view); }, "big")));
    return;
  }
  if (!v) { screen.replaceChildren(screens.join()); return; }
  if (v.phase !== "lobby") showSettings = false;
  const out = screens[showSettings ? "settings" : v.phase](v);
  screen.replaceChildren(...[endable ? endGame() : null, out].flat().filter(Boolean));
}

setInterval(() => {
  const t = $("timer");
  if (!t || !view || view.phase !== "playing") return;
  const r = remaining();
  t.textContent = fmt(r);
  t.classList.toggle("low", r <= 30000);
}, 250);

// A fixed-seed scatter of tilted runes, one per 80px cell, kept inside the 240px tile so no rune is cut at a seam.
(function runeBackground() {
  const ink = getComputedStyle(document.documentElement).getPropertyValue("--rune-ink").trim(), L = Object.keys(RUNES);
  let seed = 11, g = "";
  const rnd = () => (seed = seed * 16807 % 2147483647) / 2147483647;
  for (let i = 0; i < 9; i++) {
    const x = (i % 3) * 80 + 25 + rnd() * 30, y = Math.floor(i / 3) * 80 + 25 + rnd() * 30, k = 0.4 + rnd() * 0.25, rot = rnd() * 50 - 25;
    g += `<path transform="translate(${x} ${y}) rotate(${rot}) scale(${k}) translate(-22 -32)" d="${RUNES[L[Math.floor(rnd() * 26)]]}"/>`;
  }
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="240" height="240" fill="none" stroke="${ink}" stroke-width="4">${g}</svg>`;
  document.body.style.setProperty("--runes", `url("data:image/svg+xml,${encodeURIComponent(svg)}")`);
})();

// Staggered rows of jittered stones in a 160x120 tile; a row's first stone is drawn again past the right edge so the tile wraps.
(function cobbleWall() {
  let seed = 7, g = "";
  const rnd = () => (seed = seed * 16807 % 2147483647) / 2147483647, j = v => (v + rnd() * 3 - 1.5).toFixed(1);
  const stone = (x, y, w, k) => g += `<path fill="url(#s${k})" d="M${j(x + 2)} ${j(y + 2)}L${j(x + w / 2)} ${j(y + 1)}L${j(x + w - 2)} ${j(y + 2)}L${j(x + w - 1)} ${j(y + 15)}L${j(x + w - 2)} ${j(y + 28)}L${j(x + w / 2)} ${j(y + 29)}L${j(x + 2)} ${j(y + 28)}L${j(x + 1)} ${j(y + 15)}Z"/>`;
  for (let y = 0; y < 120; y += 30) {
    const x0 = -rnd() * 30, end = x0 + 160;
    for (let x = x0, first = true; x < end - 1; first = false) {
      let w = 28 + rnd() * 26;
      if (end - x - w < 28) w = end - x;
      const k = Math.floor(rnd() * 3);
      stone(x, y, w, k);
      if (first) stone(x + 160, y, w, k);
      x += w;
    }
  }
  const grad = (k, a, b) => `<linearGradient id="s${k}" x2=".3" y2="1"><stop offset="0" stop-color="${a}"/><stop offset="1" stop-color="${b}"/></linearGradient>`;
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="120" stroke-linejoin="round">${grad(0, "#4b3f36", "#2c241f")}${grad(1, "#574a3e", "#342b24")}${grad(2, "#43392f", "#272019")}${g}</svg>`;
  document.body.style.setProperty("--cobble", `url("data:image/svg+xml,${encodeURIComponent(svg)}")`);
})();
document.addEventListener("visibilitychange", () => document.body.classList.toggle("asleep", document.hidden));

render(null);
if (token) join("");

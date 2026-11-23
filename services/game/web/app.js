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

let token = store.get(), view = null, receivedAt = 0, es = null, refusal = null;
let secretNode = null, secretKey = "", revealTimer = 0, rejoinTimer = 0;
// What the player has open on screen. toJoin() replaces it, so nothing of it outlives the player.
const fresh = () => ({ showSettings: false, showHistory: false, history: null, armedRound: 0, endArmed: 0 });
let ui = fresh();

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
  clearTimeout(rejoinTimer); clearTimeout(ui.endArmed); rejoinTimer = 0;
  if (es) es.close();
  es = null; $("lost").hidden = true;
  store.del(); token = null; view = null; refusal = null;
  ui = fresh();
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

const lum = hex => {
  const c = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(x => (x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
// Ink is whichever of --on-gold and --text contrasts more with the card; mock/check-contrast.mjs runs this rule over every real colour.
const darkInk = c => (lum(c) + 0.05) ** 2 > (lum("#1a0e05") + 0.05) * (lum("#f3e5c8") + 0.05);
const svgs = html => { const t = document.createElement("template"); t.innerHTML = html; return [...t.content.childNodes]; };
const EMBLEM_CLS = { dragon: "claws", rider: "blades", other: "slab" };
const emblem = kind => svgs(`<svg class="emblem ${EMBLEM_CLS[kind] || "slab"}" viewBox="0 0 200 200" fill="none" stroke="currentColor" aria-hidden="true">${EMBLEM[kind] || EMBLEM.other}</svg>`);
const nameEl = name => h("div", { class: "sname" }, name.split(" ").flatMap((w, i) => [i ? " " : null, h("span", { class: "word" }, w)]));
// The kind's emblem behind a dark banner that carries the name.
const cart = (c, ...before) => h("div", { class: "cart" }, ...before, emblem(c.kind), h("div", { class: "banner" }, nameEl(c.name)));
const inkCls = c => (c.color && darkInk(c.color) ? " ink-dark" : "");
const face = c => (c.color ? `--c:${c.color}` : null);

const endable = v => v.phase === "drawing" || v.phase === "playing" || v.phase === "roundEnd";
// The whole game screen is one carved slab: the character's colour while a secret is held, dark stone otherwise.
function hall(v, c, dia, body, foot) {
  const plaque = (p, side) => h("div", { class: `plaque ${side}` }, h("small", {}, p.name),
    h("b", {}, String(p.score), p.connected === false ? h("span", { class: "away" }, " away") : null));
  return h("div", { class: c ? `hall${inkCls(c)}` : "hall stone", style: c ? face(c) : null },
    svgs(KNOTS), plaque(v.you, "l"), plaque(v.other, "r"), h("div", { class: "dia" }, h("small", {}, dia[0]), dia[1]),
    h("div", { class: "crest" }), svgs(BAND), body, svgs(BAND), foot, endable(v) ? endGame() : null);
}
const roundDia = v => v.phase === "playing"
  ? [`Round ${v.round}/${v.options.rounds}`, h("b", { id: "timer", class: "time" }, fmt(remaining()))]
  : ["Round", h("b", {}, `${v.round}/${v.options.rounds}`)];

const ABC = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";
// Each letter becomes a slot that keeps its real letter (unpainted) under a cycling rune background, so the line is the final name from the start.
function startReveal(node, name) {
  const el = node.querySelector(".sname"), slots = [], ink = "#f3e5c8"; // the banner is always dark leather, so the ink is always --text
  const img = [...ABC].map(c => `url("data:image/svg+xml,${encodeURIComponent(runeSvg(c).replace("<svg", `<svg xmlns="http://www.w3.org/2000/svg" color="${ink}"`))}")`);
  el.replaceChildren(...name.split(" ").flatMap((w, i) => [i ? " " : null, h("span", { class: "word" }, [...w].map(ch => {
    const s = h("span", { class: "slot spin" }, ch);
    slots.push([s, ch]);
    return s;
  }))]).filter(Boolean));
  const n = slots.length, total = Math.min(3500, 1600 + n * 90), step = (total - 900) / n, t0 = performance.now();
  node.classList.add("revealing");
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
  if (!secretNode || !secretNode.classList.contains("revealing")) return;
  secretNode.classList.remove("revealing");
  const sn = secretNode.querySelector(".sname");
  sn.replaceWith(nameEl(sn.textContent)); // every slot holds its real letter, so this leaves the plain name
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

// Built once per drawn secret and reused across renders, so the reveal, the open tips and Hide outlive every event.
function secretCard(v) {
  const s = v.secret, key = `${v.round}:${s.name}`;
  if (!secretNode || secretKey !== key) {
    stopReveal();
    secretKey = key;
    let stage = null;
    if (s.clip) {
      stage = h("div", { class: "stage" });
      const fallback = () => stage.remove();
      const video = h("video", { src: s.clip, poster: s.poster, muted: true, playsinline: true, autoplay: true, "aria-hidden": "true" });
      video.muted = true; // the attribute alone is not honoured for autoplay in every browser
      video.addEventListener("ended", () => s.poster ? stage.replaceChildren(h("img", { src: s.poster, alt: "" })) : fallback());
      video.addEventListener("error", fallback);
      stage.append(video);
    }
    // Hide blanks the secret for a glance over the shoulder; the card face stays.
    const hide = h("button", { class: "conceal", "aria-pressed": "false", onclick: () => {
      const on = secretNode.classList.toggle("concealed");
      hide.setAttribute("aria-pressed", String(on));
      hide.textContent = on ? "Show" : "Hide";
    } }, "Hide");
    secretNode = h("div", { class: "secret" }, h("div", { class: "kindrow" }, h("div", { class: "kind" }, s.kind), hide),
      h("div", { class: "well" }, stage, cart(s), s.tips ? hints(s.tips, key) : null));
    // Animate only a draw this page saw happen: a reload mid-round shows the settled name.
    if (!s.clip && ui.armedRound === v.round && !matchMedia("(prefers-reduced-motion: reduce)").matches) startReveal(secretNode, s.name);
  }
  const hb = secretNode.querySelector(".hints");
  if (hb) hb.hidden = v.phase !== "playing";
  return secretNode;
}

const btn = (label, onclick, cls = "", extra = {}) => h("button", { class: cls, onclick, ...extra }, label);
const panel = (...kids) => h("div", { class: "panel" }, ...kids);
// Two taps on purpose: the first arms it for 4 s, only the second ends the game for both phones.
const endGame = () => h("button", { class: "end", onclick: () => {
  if (ui.endArmed) { clearTimeout(ui.endArmed); ui.endArmed = 0; act("end_game"); return; }
  ui.endArmed = setTimeout(() => { ui.endArmed = 0; render(view); }, 4000);
  render(view);
} }, ui.endArmed ? "Really end?" : "End game");

const plinth = (...kids) => h("div", { class: "plinth" }, ...kids);
const reveals = v => h("div", { class: "verdict" },
  [[v.reveal.yourGuess, "You guessed", v.gain.you], [v.reveal.theirGuess, `${v.other.name} guessed`, v.gain.other]].map(([c, label, gain]) =>
    h("figure", {}, h("figcaption", {}, label),
      h("div", { class: `hall mini${inkCls(c)}`, style: face(c) }, svgs(KNOTS), h("div", { class: "kind" }, c.kind), cart(c)),
      h("p", { class: "pts" }, `+${gain}`, h("small", {}, "points")))));

const screens = {
  join() {
    const input = h("input", { maxlength: "20", autocomplete: "nickname", "aria-label": "Your name", placeholder: "Your name", required: true });
    return h("form", { class: "panel", onsubmit: e => { e.preventDefault(); if (input.value.trim()) join(input.value.trim()); } },
      h("h2", {}, "Take your place"), input, h("p"), h("button", { class: "primary big" }, "Join"));
  },
  lobby(v) {
    const o = v.options;
    const hist = ui.showHistory ? panel(h("h2", {}, "The scroll"), ui.history == null ? h("p", { class: "muted" }, "Unrolling...")
      : ui.history.length === 0 ? h("p", { class: "muted" }, "No games yet.")
      : h("ul", { class: "hist" }, ui.history.map(g => h("li", {}, h("span", {}, g.players.map(p => `${p.name} ${p.score}`).join(" - ")),
        h("span", { class: "muted" }, `${g.rounds}r, ${new Date(g.finished).toLocaleDateString()}`))))) : null;
    return [
      panel(h("h2", {}, "Lobby"), h("p", {}, `${v.you.name} (you)`),
        h("p", { class: v.other ? "" : "muted" }, v.other ? v.other.name : "Waiting for a second rider..."),
        h("p", { class: "muted summary" }, `${o.rounds} round${o.rounds > 1 ? "s" : ""}, ${fmt(o.seconds * 1000)}, hints ${o.hints ? "on" : "off"}, ${o.pool}`),
        v.endedBy ? h("p", { class: "notice" }, v.endedBy === v.you.name ? "You ended the game" : `${v.endedBy} ended the game`) : null),
      btn("Settings", () => { ui.showSettings = true; ui.showHistory = false; render(view); }),
      btn("Start", () => act("start"), "primary big", { disabled: !v.canStart }),
      hist,
      h("div", { class: "row" },
        btn(ui.showHistory ? "Roll up the scroll" : "Past games", toggleHistory, "link"),
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
      btn("Back", () => { ui.showSettings = false; render(view); }, "back"));
  },
  drawing(v) {
    if (!v.drawn.you) ui.armedRound = v.round;
    const st = h("p", { class: "st" }, v.drawn.other ? `${v.other.name} has drawn.` : `Waiting for ${v.other.name} to draw...`);
    return v.drawn.you ? hall(v, v.secret, roundDia(v), secretCard(v), plinth(st))
      : hall(v, null, roundDia(v), h("div", { class: "well" }, h("h2", { class: "head" }, "A new round"),
        h("p", { class: "sub" }, "Your secret is waiting. Keep it from the other player."), st),
      plinth(btn("Draw your character", () => act("draw"), "primary")));
  },
  playing(v) {
    const done = v.resolved.you, them = v.resolved.other;
    return hall(v, v.secret, roundDia(v), [secretCard(v),
      h("p", { class: "st" }, them === "got_it" ? `${v.other.name} got it.` : them === "forfeit" ? `${v.other.name} gave up.` : `${v.other.name} is still guessing...`)],
    plinth(done ? h("p", {}, done === "got_it" ? "You got it. Waiting for the round to end..." : "You forfeited. Waiting for the round to end...")
      : [btn("Got it", () => act("got_it"), "primary"), btn("Forfeit", () => act("forfeit"))]));
  },
  roundEnd: v => hall(v, null, roundDia(v), h("div", { class: "well" }, h("h2", { class: "head" }, `Round ${v.round} ends`), reveals(v)),
    plinth(btn(v.round >= v.options.rounds ? "See the verdict" : "Next round", () => act("next"), "primary"))),
  final(v) {
    const mine = v.you.score, theirs = v.other.score, kind = mine > theirs ? "win" : mine < theirs ? "lose" : "tie", t = FINALE[kind];
    return hall(v, null, ["Final", h("b", {}, `${v.round}/${v.options.rounds}`)], h("div", { class: `well finale ${kind}`, "data-result": kind },
      svgs(t.art), h("h2", { class: "head big" }, t.head), h("p", { class: "sub" }, t.lines[(mine + theirs) % t.lines.length]),
      h("p", { class: "pts final-score" }, `${v.you.name} ${mine} - ${theirs} ${v.other.name}`)),
    plinth(btn("Back to lobby", () => act("back_to_lobby"), "primary")));
  },
};

async function toggleHistory() {
  ui.showHistory = !ui.showHistory;
  if (ui.showHistory) { ui.history = null; render(view); const r = await api("/api/history"); ui.history = r.ok ? r.j : []; }
  render(view);
}

// A change of screen sends a dragon's shadow across; the new screen is painted while the shadow covers it (the midpoint).
// Between two game screens the shadow stays inside the slab's middle, so the plaques, timer and buttons hold still.
const FLY_MS = 900;
$("fly").querySelector(".dragon").innerHTML = drake(true);
let shown = null, flying = null;
// Drawing your own character is a screen change on its own phone, and the wait for the other player's draw is not another one.
const screenKey = v => (refusal ? "refused" : !v ? "join" : ui.showSettings && v.phase === "lobby" ? "settings" : v.phase === "playing" || (v.phase === "drawing" && v.drawn.you) ? "secret" : v.phase);
// Where the shadow flies: the whole page, or between two game screens only the slab's middle, between its two rune bands.
function sweepRect(v) {
  const [top, bottom] = [...document.querySelectorAll(".hall > .band")].map(b => b.getBoundingClientRect());
  if (!bottom || !v || v.phase === "lobby" || refusal) return { left: 0, top: 0, width: innerWidth, height: innerHeight };
  return { left: top.left, top: top.bottom, width: top.width, height: bottom.top - top.bottom };
}
// The new screen is painted at the midpoint, once the shadow covers the old one; whichever of timer, finish or cancel comes first does it.
function transition(v) {
  const fly = $("fly"), screen = $("screen"), r = sweepRect(v), f = flying = { v };
  Object.assign(fly.style, { left: r.left + "px", top: r.top + "px", width: r.width + "px", height: r.height + "px" });
  fly.hidden = false; screen.inert = true; // the old screen is already stale: no taps until the new one is painted
  const swap = () => { if (f.swapped) return; f.swapped = true; screen.inert = false; shown = screenKey(f.v); paint(f.v); };
  const anim = fly.firstElementChild.animate([{ transform: `translateX(${-2.4 * r.width}px)` }, { transform: `translateX(${r.width}px)` }], { duration: FLY_MS, easing: "ease-in-out" });
  anim.onfinish = anim.oncancel = () => { swap(); fly.hidden = true; if (flying === f) flying = null; };
  setTimeout(swap, FLY_MS / 2);
}
function render(v) {
  if (flying) { if (!flying.swapped) { flying.v = v; return; } shown = screenKey(v); paint(v); return; } // after the swap the screen is live under the leaving shadow
  const key = screenKey(v);
  if (shown === null || key === shown || matchMedia("(prefers-reduced-motion: reduce)").matches) { shown = key; paint(v); return; }
  transition(v);
}

function paint(v) {
  document.body.classList.toggle("home", !v || v.phase === "lobby");
  document.body.classList.toggle("scene", !v);
  const screen = $("screen");
  if (!v || !(v.phase === "drawing" || v.phase === "playing")) { stopReveal(); secretNode = null; secretKey = ""; }
  if (!v || !endable(v)) { clearTimeout(ui.endArmed); ui.endArmed = 0; }
  if (refusal) {
    screen.replaceChildren(h("div", { class: "panel refused" }, h("h2", {}, "Refused"), h("p", {}, refusal),
      btn("Back", () => { refusal = null; render(view); }, "big")));
    return;
  }
  if (!v) { screen.replaceChildren(screens.join()); return; }
  if (v.phase !== "lobby") ui.showSettings = false;
  const out = screens[ui.showSettings ? "settings" : v.phase](v);
  screen.replaceChildren(...[out].flat().filter(Boolean));
}

setInterval(() => {
  const t = $("timer");
  if (!t || !view || view.phase !== "playing") return;
  const r = remaining();
  t.textContent = fmt(r);
  t.classList.toggle("low", r <= 30000);
}, 250);

document.addEventListener("visibilitychange", () => document.body.classList.toggle("asleep", document.hidden));

render(null);
if (token) { shown = null; join(""); } // the first real screen after a reload is painted at once, not swept in over the blank join form

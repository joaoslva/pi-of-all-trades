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

const lum = hex => {
  const c = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(x => (x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
// Ink is whichever of --on-gold and --text contrasts more with the card; mock/check-contrast.mjs runs this rule over every real colour.
const darkInk = c => (lum(c) + 0.05) ** 2 > (lum("#1a0e05") + 0.05) * (lum("#f3e5c8") + 0.05);
const svgs = html => { const t = document.createElement("template"); t.innerHTML = html; return [...t.content.childNodes]; };
const KNOTS = ["a", "b", "c", "d"].map(k => `<svg class="kn ${k}" aria-hidden="true"><use href="#kn"/></svg>`).join("");
// A <use> takes currentColor from where it is placed, so the band follows the card ink.
const BAND = '<svg class="band" aria-hidden="true"><use href="#rb"/><use href="#rb" x="312"/></svg>';
const BLADE = '<path d="M100 18l8 14v98h-16V32zM100 32v96M78 132h44M100 136v26"/><circle cx="100" cy="168" r="6" fill="currentColor"/>';
// Drawn in the ink at low opacity behind the banner, so no text ever sits on it.
const EMBLEM = {
  dragon: '<g transform="rotate(24 100 100)" fill="currentColor" stroke="none"><use href="#claw"/><use href="#claw" x="38" y="-8"/><use href="#claw" x="76"/></g>',
  rider: `<circle cx="100" cy="100" r="76" stroke-width="2.5"/><circle cx="100" cy="100" r="66" stroke-width="1.5"/><g stroke-width="3" transform="rotate(38 100 100)">${BLADE}</g><g stroke-width="3" transform="rotate(-38 100 100)">${BLADE}</g>`,
  other: '<path d="M44 14h112l18 18v162H26V32z" fill="currentColor" fill-opacity=".12" stroke-width="2"/><path d="M33 22h134v172H33z"/><path d="M124 14l-8 22 6 14-10 18M44 194l10-16-4-12" stroke-width="1.5"/>',
};
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
    if (!s.clip && armedRound === v.round && !matchMedia("(prefers-reduced-motion: reduce)").matches) startReveal(secretNode, s.name);
  }
  const hb = secretNode.querySelector(".hints");
  if (hb) hb.hidden = v.phase !== "playing";
  return secretNode;
}

const btn = (label, onclick, cls = "", extra = {}) => h("button", { class: cls, onclick, ...extra }, label);
const panel = (...kids) => h("div", { class: "panel" }, ...kids);
// Two taps on purpose: the first arms it for 4 s, only the second ends the game for both phones.
const endGame = () => h("button", { class: "end", onclick: () => {
  if (endArmed) { clearTimeout(endArmed); endArmed = 0; act("end_game"); return; }
  endArmed = setTimeout(() => { endArmed = 0; render(view); }, 4000);
  render(view);
} }, endArmed ? "Really end?" : "End game");

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
    plinth(btn("Next round", () => act("next"), "primary"))),
  final(v) {
    const f = v.final;
    return hall(v, null, ["Final", h("b", {}, `${v.round}/${v.options.rounds}`)], h("div", { class: "well" },
      h("h2", { class: "head big" }, f.tie ? "A tie" : `${f.winner} wins`), h("p", { class: "sub" }, `${v.you.name} ${v.you.score} - ${v.other.score} ${v.other.name}`), reveals(v)),
    plinth(btn("Back to lobby", () => act("back_to_lobby"), "primary")));
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
  const screen = $("screen");
  if (!v || !(v.phase === "drawing" || v.phase === "playing")) { stopReveal(); secretNode = null; secretKey = ""; }
  if (!v || !endable(v)) { clearTimeout(endArmed); endArmed = 0; }
  if (refusal) {
    screen.replaceChildren(h("div", { class: "panel refused" }, h("h2", {}, "Refused"), h("p", {}, refusal),
      btn("Back", () => { refusal = null; render(view); }, "big")));
    return;
  }
  if (!v) { screen.replaceChildren(screens.join()); return; }
  if (v.phase !== "lobby") showSettings = false;
  const out = screens[showSettings ? "settings" : v.phase](v);
  screen.replaceChildren(...[out].flat().filter(Boolean));
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

// The hall frame is one 60px tile of nested octagons, 9-sliced by border-image so it fits any size at 1:1 (or 1:2 on the minis).
(function hallArt() {
  const oct = d => { const a = d, b = 60 - d, c = 16 - .586 * d; return [[a + c, a], [b - c, a], [b, a + c], [b, b - c], [b - c, b], [a + c, b], [a, b - c], [a, a + c]]; };
  const P = p => "M" + p.map(v => v.map(n => +n.toFixed(2)).join(" ")).join("L");
  const ln = (d, col, w, part) => { const o = oct(d); return `<path fill="none" stroke="${col}" stroke-width="${w}" d="${part === 1 ? P([o[6], o[7], o[0], o[1]]) : part === 0 ? P([o[2], o[3], o[4], o[5]]) : P(o) + "Z"}"/>`; };
  const ring = (a, b, f) => `<path fill-rule="evenodd" fill="${f}" d="${P(oct(a))}Z${P(oct(b))}Z"/>`;
  const dark = "#0a070b", lit = "#7d6c5f", groove = "#1c1419", lip = "#5f504a";
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="60" height="60">' + ring(0, 13, "#3b3238") + ring(13, 19.5, "#150e16")
    + ln(.75, dark, 1.5) + ln(2.4, lit, 1.2, 1) + ln(2.4, groove, 1.2, 0)
    + ln(7, groove, 1, 1) + ln(8, lip, 1, 1) + ln(7, lip, 1, 0) + ln(8, groove, 1, 0)
    + ln(12.4, lip, 1.2, 0) + ln(15.8, "#c9a35a", 1.6) + ln(19.5, dark, 1)
    + ln(20.6, "rgba(0,0,0,.45)", 1.8, 1) + ln(20.6, "rgba(255,255,255,.22)", 1.8, 0) + "</svg>";
  document.documentElement.style.setProperty("--frame", `url("data:image/svg+xml,${encodeURIComponent(svg)}")`);
  // Corner knot: strands drawn dark-then-gold one after another, so a later strand passes over an earlier one.
  const strand = d => `<path d="${d}" stroke="#0a070b" stroke-width="5"/><path d="${d}" stroke="#d8ad5a" stroke-width="2"/>`;
  const knot = ["M9 40V18L18 9H40", "M16 5L27 16L16 27L5 16Z", "M9 24V18L12 15", "M3 36V12L12 3H36M30 0L36 3L30 6M0 30L3 36L6 30"].map(strand).join("");
  const band = [..."TAIRNSGEYLBODHMPUWXCFJKQVZ"].map((ch, i) => `<path transform="translate(${i * 12 + 1.5} 2) scale(.16)" d="${RUNES[ch]}"/>`).join("");
  $("defs").innerHTML = `<symbol id="kn" viewBox="0 0 40 40"><g fill="none">${knot}<path d="M16 12L20 16L16 20L12 16Z" fill="#d8ad5a"/></g></symbol>`
    + `<g id="rb" fill="none" stroke="currentColor"><path d="M0 .5H312M0 13.5H312"/><g stroke-width="7">${band}</g></g>`
    + '<path id="claw" d="M58 18c14 50 16 110 2 166c26-56 24-116-2-166z"/>';
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

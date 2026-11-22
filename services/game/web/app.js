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

let token = store.get(), view = null, receivedAt = 0, es = null, refusal = null, showHistory = false, history = null;
let secretNode = null, secretKey = "", armedRound = 0, revealTimer = 0;

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
  const r = await api("/api/join", { name, token: token || undefined });
  if (r.status === 409 && r.j.error === "bonded") { refusal = r.j.message; render(view); return; }
  if (!r.ok) { store.del(); token = null; view = null; render(null); return; }
  token = r.j.token; store.set(token);
  setView(r.j.view); connect();
}

function connect() {
  if (es) es.close();
  es = new EventSource(`/api/events?token=${encodeURIComponent(token)}`);
  es.addEventListener("view", e => { $("lost").hidden = true; setView(JSON.parse(e.data)); });
  es.onerror = () => {
    $("lost").hidden = false;
    // A closed stream means the browser gave up (401 after a server restart): rejoin by token ourselves.
    if (es.readyState === EventSource.CLOSED) setTimeout(() => join(view ? view.you.name : ""), 2000);
  };
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
    h("div", { class: "mid" }, `Round ${v.round} / ${v.rounds}`,
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
function runeRow(name) {
  const letters = [];
  const row = h("div", { class: "runes", "aria-hidden": "true" }, name.split(" ").map(w => h("span", { class: "word" }, [...w].map(ch => {
    const s = h("span", { class: "r" });
    s.innerHTML = runeSvg(ch);
    letters.push([s, ch]);
    return s;
  }))));
  return { row, letters };
}

function startReveal(el, letters) {
  const n = letters.length, total = Math.min(3500, 1600 + n * 90), step = (total - 900) / n, t0 = performance.now();
  el.classList.add("revealing");
  letters.forEach(([s]) => s.classList.add("spin"));
  const tick = () => {
    const t = performance.now() - t0;
    letters.forEach(([s, ch], i) => {
      if (!s.classList.contains("spin")) return;
      const settled = t >= 600 + (i + 1) * step;
      s.innerHTML = runeSvg(settled ? ch : ABC[Math.floor(Math.random() * 26)]);
      if (settled) s.classList.remove("spin");
    });
    if (t >= total) stopReveal();
  };
  revealTimer = setInterval(tick, 70);
  tick();
}
function stopReveal() {
  clearInterval(revealTimer); revealTimer = 0;
  if (secretNode) secretNode.querySelector(".card").classList.remove("revealing");
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
    const { row, letters } = runeRow(s.name);
    let face = row;
    if (s.clip) {
      const stage = h("div", { class: "stage" });
      const fallback = () => stage.replaceWith(row);
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
    if (!s.clip && armedRound === v.round && !matchMedia("(prefers-reduced-motion: reduce)").matches) startReveal(front, letters);
  }
  const hb = secretNode.querySelector(".hints");
  if (hb) hb.hidden = v.phase !== "playing";
  return secretNode;
}

const btn = (label, onclick, cls = "", extra = {}) => h("button", { class: cls, onclick, ...extra }, label);
const panel = (...kids) => h("div", { class: "panel" }, ...kids);

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
    const set = n => act("set_rounds", { rounds: n });
    const hist = showHistory ? panel(h("h2", {}, "The scroll"), history == null ? h("p", { class: "muted" }, "Unrolling...")
      : history.length === 0 ? h("p", { class: "muted" }, "No games yet.")
      : h("ul", { class: "hist" }, history.map(g => h("li", {}, h("span", {}, g.players.map(p => `${p.name} ${p.score}`).join(" - ")),
        h("span", { class: "muted" }, `${g.rounds}r, ${new Date(g.finished).toLocaleDateString()}`))))) : null;
    return [
      panel(h("h2", {}, "Lobby"), h("p", {}, `${v.you.name} (you)`),
        h("p", { class: v.other ? "" : "muted" }, v.other ? v.other.name : "Waiting for a second rider...")),
      panel(h("h2", {}, "Rounds"), h("div", { class: "stepper" },
        btn("-", () => set(v.rounds - 1), "", { disabled: v.rounds <= 1, "aria-label": "Fewer rounds" }),
        h("output", {}, String(v.rounds)),
        btn("+", () => set(v.rounds + 1), "", { disabled: v.rounds >= 10, "aria-label": "More rounds" }))),
      btn("Start", () => act("start"), "primary big", { disabled: !v.canStart }),
      hist,
      h("div", { class: "row" },
        btn(showHistory ? "Roll up the scroll" : "Past games", toggleHistory, "link"),
        btn("Leave", () => act("leave").then(() => { store.del(); token = null; if (es) es.close(); view = null; render(null); }), "link")),
    ];
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
  renderBar(v);
  const screen = $("screen");
  if (refusal) {
    screen.replaceChildren(h("div", { class: "panel refused" }, h("h2", {}, "Refused"), h("p", {}, refusal),
      btn("Back", () => { refusal = null; render(view); }, "big")));
    return;
  }
  if (!v) { screen.replaceChildren(screens.join()); return; }
  if (!(v.phase === "drawing" || v.phase === "playing")) { stopReveal(); secretNode = null; secretKey = ""; }
  const out = screens[v.phase](v);
  screen.replaceChildren(...[out].flat().filter(Boolean));
}

setInterval(() => {
  const t = $("timer");
  if (!t || !view || view.phase !== "playing") return;
  const r = remaining();
  t.textContent = fmt(r);
  t.classList.toggle("low", r <= 30000);
}, 250);

render(null);
if (token) join("");

// Stand-in for the Go server, enough of API.md to play two phones. node mock/mock.mjs [port]
// MOCK_SECONDS overrides every round length so a timeout is quick to see.
import { createServer } from "node:http";
import { readFileSync, existsSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { fileURLToPath } from "node:url";
import { extname, join, normalize } from "node:path";

const WEB = fileURLToPath(new URL("..", import.meta.url));
const PORT = +(process.argv[2] || 18091);
const CLIP = join(WEB, "mock", "test-clip.mp4");
const POSTER = join(WEB, "mock", "test-poster.jpg");
const CHARS = [
  { name: "Tairn", kind: "dragon", color: "#1F2421", media: true, tips: ["Tairn tip one.", "Tairn tip two."] },
  { name: "Violet Sorrengail", kind: "rider", color: "#1F2421", tips: ["Violet vague tip.", "Violet closer tip.", "Violet specific tip."] },
  { name: "Andarna", kind: "dragon", color: "#FFD400", tips: ["Andarna tip."] },
  { name: "Xaden Riorson", kind: "rider", color: "#1A5B92" },
  { name: "Sgaeyl", kind: "dragon", color: "#1A5B92" },
  { name: "Colonel Lyron Panchek", kind: "other", tips: ["Panchek tip."] },
  { name: "Cath", kind: "dragon", color: "#C52233" }, { name: "Rhiannon Matthias", kind: "rider", color: "#4C956C" },
];
const TYPES = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2" };

let g;
const DEFAULTS = { rounds: 3, seconds: 180, hints: true, pool: "all" };
const reset = () => (g = { phase: "lobby", players: [], options: { ...DEFAULTS }, round: 0, deadline: 0, used: 0, gain: null, final: null, media: new Map(), history: [] });
reset();
const other = p => g.players.find(q => q !== p);
const roundMs = () => (+process.env.MOCK_SECONDS || g.options.seconds) * 1000;
const bonus = () => Math.min(2, Math.floor((3 * Math.max(0, g.deadline - Date.now())) / roundMs()));

function view(p) {
  const o = other(p), pub = q => ({ name: q.name, score: q.score, connected: q.connected });
  const v = { phase: g.phase, serverNow: Date.now(), you: { name: p.name, score: p.score }, other: o ? pub(o) : null,
    options: g.options, round: g.round, canStart: g.phase === "lobby" && g.players.length === 2 };
  if (g.phase === "drawing" || g.phase === "playing") {
    v.drawn = { you: p.drawn, other: o.drawn }; v.resolved = { you: p.res, other: o.res };
    if (g.phase === "playing") v.deadline = g.deadline;
    if (p.drawn) {
      const c = p.secret, s = { name: c.name, kind: c.kind, color: c.color, tips: g.options.hints ? c.tips : undefined };
      if (c.media && existsSync(CLIP)) { s.clip = `/m/${p.opaque}/clip`; s.poster = `/m/${p.opaque}/poster`; }
      v.secret = s;
    }
  }
  if (g.phase === "roundEnd" || g.phase === "final") {
    const shown = c => ({ name: c.name, kind: c.kind, color: c.color });
    v.reveal = { yourGuess: shown(o.secret), theirGuess: shown(p.secret) };
    v.gain = { you: p.gain, other: o.gain };
    if (g.phase === "final") v.final = g.final;
  }
  return v;
}
const push = () => g.players.forEach(p => p.sse && p.sse.write(`event: view\ndata: ${JSON.stringify(view(p))}\n\n`));

function startRound() {
  g.round++; g.phase = "drawing";
  const deck = CHARS.filter(c => g.options.pool === "all" || `${c.kind}s` === g.options.pool);
  g.players.forEach((p, i) => {
    p.secret = deck[(g.used + i) % deck.length]; p.drawn = false; p.res = null; p.gain = 0;
    p.opaque = randomBytes(8).toString("hex"); g.media.set(p.opaque, p);
  });
  g.used += 2;
}
function maybeEnd() {
  if (g.phase !== "playing" || g.players.some(p => !p.res)) return;
  g.players.forEach(p => { p.gain = p.res === "got_it" ? 1 + bonus() : 0; p.score += p.gain; });
  g.media.clear();
  if (g.round >= g.options.rounds) {
    const [a, b] = g.players, tie = a.score === b.score;
    g.final = { winner: tie ? "" : (a.score > b.score ? a : b).name, tie };
    g.history.unshift({ finished: new Date().toISOString(), rounds: g.options.rounds, players: g.players.map(p => ({ name: p.name, score: p.score })) });
    g.phase = "final";
  } else g.phase = "roundEnd";
}
setInterval(() => {
  if (g.phase !== "playing" || Date.now() < g.deadline) return;
  g.players.forEach(p => { p.res ||= "forfeit"; }); maybeEnd(); push();
}, 300);

const send = (res, code, body, type = "application/json") => { res.writeHead(code, { "content-type": type }); res.end(typeof body === "string" || Buffer.isBuffer(body) ? body : JSON.stringify(body)); };
const err = (res, code, error, message = error) => send(res, code, { error, message });
const body = req => new Promise(r => { let s = ""; req.on("data", d => (s += d)); req.on("end", () => { try { r(JSON.parse(s || "{}")); } catch { r({}); } }); });
const byToken = t => g.players.find(p => p.token === t);

function act(p, a, res) {
  const ph = g.phase, bad = () => err(res, 409, "phase", "Not now.");
  switch (a.type) {
    case "set_options": {
      if (ph !== "lobby") return bad();
      const o = { ...g.options, ...Object.fromEntries(Object.keys(DEFAULTS).filter(k => k in a).map(k => [k, a[k]])) };
      if (!(Number.isInteger(o.rounds) && o.rounds >= 1 && o.rounds <= 10)) return err(res, 400, "rounds");
      if (![120, 180, 300].includes(o.seconds)) return err(res, 400, "seconds");
      if (typeof o.hints !== "boolean") return err(res, 400, "bad_request");
      if (!["all", "riders", "dragons"].includes(o.pool)) return err(res, 400, "pool");
      g.options = o; break;
    }
    case "start": if (ph !== "lobby" || g.players.length < 2) return bad(); startRound(); break;
    case "draw": if (ph !== "drawing") return bad(); p.drawn = true;
      if (g.players.every(q => q.drawn)) { g.phase = "playing"; g.deadline = Date.now() + roundMs(); } break;
    case "got_it": case "forfeit": if (ph !== "playing") return bad(); p.res ||= a.type; maybeEnd(); break;
    case "next": if (ph === "roundEnd") startRound(); else if (ph !== "drawing") return bad(); break;
    case "back_to_lobby": if (ph !== "final") return bad(); g.players.forEach(q => (q.score = 0)); g.phase = "lobby"; g.round = 0; g.used = 0; break;
    case "leave": g.players = g.players.filter(q => q !== p); if (!g.players.length) g.options = { ...DEFAULTS }; if (ph !== "lobby") { g.phase = "lobby"; g.round = 0; g.players.forEach(q => (q.score = 0)); } break;
    default: return err(res, 400, "type");
  }
  send(res, 200, p.token && g.players.includes(p) ? view(p) : {});
  push();
}

createServer(async (req, res) => {
  const u = new URL(req.url, "http://x");
  if (u.pathname === "/api/join" && req.method === "POST") {
    const b = await body(req), name = String(b.name || "").trim().slice(0, 20);
    let p = b.token && byToken(b.token);
    if (!p) {
      if (!name) return err(res, 400, "name", "Name 1..20 characters.");
      if (g.phase !== "lobby" || g.players.length >= 2) return err(res, 409, "bonded", "The dragons have decided not to bond with you. Try again next year...");
      p = { token: randomBytes(16).toString("hex"), name, score: 0, connected: false };
      g.players.push(p);
    }
    send(res, 200, { token: p.token, view: view(p) }); push();
  } else if (u.pathname === "/api/events") {
    const p = byToken(u.searchParams.get("token"));
    if (!p) return err(res, 401, "token", "Unknown token.");
    res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
    p.sse?.end(); p.sse = res; p.connected = true;
    res.write(`event: view\ndata: ${JSON.stringify(view(p))}\n\n`); push();
    const ka = setInterval(() => res.write(": ka\n\n"), 15000);
    req.on("close", () => { clearInterval(ka); if (p.sse === res) { p.sse = null; p.connected = false; push(); } });
  } else if (u.pathname === "/api/action" && req.method === "POST") {
    const p = byToken(u.searchParams.get("token"));
    if (!p) return err(res, 401, "token", "Unknown token.");
    act(p, await body(req), res);
  } else if (u.pathname === "/api/history") send(res, 200, g.history.slice(0, 20));
  else if (u.pathname === "/mock/reset") { reset(); send(res, 200, {}); }
  else if (/^\/m\/[^/]+\/(clip|poster)$/.test(u.pathname)) {
    const [, , id, kind] = u.pathname.split("/"), f = kind === "clip" ? CLIP : POSTER;
    if (!g.media.has(id) || !existsSync(f)) return err(res, 404, "gone");
    send(res, 200, readFileSync(f), kind === "clip" ? "video/mp4" : "image/jpeg");
  } else {
    const f = join(WEB, normalize(u.pathname === "/" ? "/index.html" : u.pathname));
    if (!f.startsWith(WEB) || !existsSync(f)) return err(res, 404, "nf");
    send(res, 200, readFileSync(f), TYPES[extname(f)] || "application/octet-stream");
  }
}).listen(PORT, () => console.log(`mock on :${PORT}`));

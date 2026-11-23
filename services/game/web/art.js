"use strict";
// Everything drawn, and none of the logic: the shared SVG pieces, the dragon, the end screens and the generated backgrounds.
// Loaded after runes.js and before app.js, which uses KNOTS, BAND, EMBLEM, drake and FINALE.
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
// The dragon, drawn once: the sweep flaps its wings, the end screens show it still. The rider is only drawn on the winner's.
// The near wing sweeps back over the tail, the far one is the same wing raised and shrunk behind the body. Wing ribs are even-odd slits.
const DRAKE_BODY = "M394 63C394 63 385 55 378 52C371 49 362 46 356 44C350 42 353 45 344 39C335 33 306 10 306 10C306 10 330 39 334 44C338 49 332 43 326 40C320 37 300 30 300 30C300 30 321 44 324 50C327 56 319 59 316 62C313 65 308 64 308 64C308 64 306 70 304 72C302 74 296 76 296 76C296 76 293 82 290 84C287 86 280 86 280 86C280 86 279 92 276 94C273 96 266 96 266 96C266 96 263 102 256 102C249 102 238 98 226 98C214 98 202 99 190 100C178 101 168 103 162 102C156 101 154 96 154 96C154 96 150 105 146 106C142 107 134 102 134 102C134 102 130 107 124 110C118 113 109 114 100 118C91 122 82 126 72 130C62 134 54 137 46 138C38 139 31 141 26 138C21 135 20 122 20 122L-6 142L20 160C20 160 21 148 26 146C31 144 39 149 48 148C57 147 67 144 78 142C89 140 98 138 108 136C118 134 126 131 134 132C142 133 148 138 150 142C152 146 150 150 146 154C142 158 134 163 128 166C122 169 112 168 112 168C112 168 123 170 122 172C121 174 104 180 104 180C104 180 126 175 128 176C130 177 116 186 116 186C116 186 131 179 140 174C149 169 158 164 164 158C170 152 170 146 176 142C182 138 192 137 200 136C208 135 217 133 222 136C227 139 227 146 230 150C233 154 233 158 236 160C239 162 243 162 248 162C253 162 264 158 264 158C264 158 252 157 252 156C252 155 266 152 266 152C266 152 253 152 250 150C247 148 247 144 248 140C249 136 254 133 258 130C262 127 265 126 270 122C275 118 282 112 288 106C294 100 299 95 306 90C313 85 319 81 326 78C333 75 338 74 346 74C354 74 362 76 370 76C378 76 388 74 388 74L362 68L394 63Z", DRAKE_WING = "M245 103C247 80 242 58 229 44C217 32 203 23 191 17L193 1L180 16Q143 13 110 32Q128 35 68 70Q111 59 52 112Q112 79 72 143Q127 118 168 137L203 117ZM170 20L127 33L127 30ZM163 26L95 62L94 60ZM161 34L83 95L82 92ZM166 39L100 118L98 116Z";
const drake = (flap, rider) => `<g transform="translate(242 102) rotate(54) scale(.72) translate(-242 -102)"><path fill-rule="evenodd" class="${flap ? "wing far" : ""}" d="${DRAKE_WING}"/></g><path d="${DRAKE_BODY}"/><path fill-rule="evenodd" class="${flap ? "wing" : ""}" d="${DRAKE_WING}"/>${rider ? '<path d="M278 94l2-24 12 1-2 23z"/><circle cx="287" cy="58" r="9"/><path d="M281 76C294 68 304 54 310 42L315 46C309 60 299 76 286 86z"/>' : ""}`;
const FINALE = {
  win: { head: "Victory!", lines: ["You were strong enough to survive Guessing Day.", "The dragons approve. Barely.", "Ride into the sunset, you earned it."],
    art: `<svg class="art" viewBox="0 0 400 240" aria-hidden="true"><defs><radialGradient id="sun"><stop offset="0" stop-color="#ffe9a8"/><stop offset=".6" stop-color="#e8b04a"/><stop offset="1" stop-color="#e8b04a00"/></radialGradient></defs>
      <circle cx="250" cy="120" r="105" fill="url(#sun)"/><g stroke="#e8b04a" stroke-width="2" opacity=".5"><path d="M20 70h90M0 100h70M30 170h100M290 205h100"/></g>
      <svg class="rider" x="60" y="40" width="300" height="161" viewBox="-10 -20 410 220">${drake(false, true)}</svg></svg>` },
  lose: { head: "Burned!", lines: ["Scorched, toasted and served with a side of ash.", "The dragons found you... lacking.", "Roasted. Maybe next year."],
    art: `<svg class="art" viewBox="0 0 400 240" aria-hidden="true"><defs><radialGradient id="char"><stop offset="0" stop-color="#000"/><stop offset=".55" stop-color="#150b08"/><stop offset="1" stop-color="#150b0800"/></radialGradient></defs>
      <path d="M200 20l30 34 40-12-6 44 46 6-30 34 38 24-48 8 14 40-44-12-8 36-32-30-30 32-12-38-44 10 10-38-50-14 40-28-36-30 46-6-8-44 40 14z" fill="url(#char)"/>
      <g stroke="#e0300a" stroke-width="2" fill="none" opacity=".75"><path d="M200 120l-20-28-8-30M200 120l30-18 22-34M200 120l-34 20-40 4M200 120l26 30 18 38M200 120l-6 36-20 30"/></g>
      <g fill="#ff8a2a"><circle class="ember" cx="150" cy="64" r="3"/><circle class="ember e2" cx="262" cy="80" r="2.5"/><circle class="ember e3" cx="238" cy="190" r="3"/><circle class="ember e2" cx="132" cy="170" r="2"/></g>
      <g stroke="#7d6c5f" stroke-width="5" fill="none" stroke-linecap="round" opacity=".5"><path class="smoke" d="M200 100c-20-20 20-30 0-50s20-30 0-46"/><path class="smoke s2" d="M236 112c-16-16 14-26 0-42"/></g></svg>` },
  tie: { head: "A tie", lines: ["Both were lucky enough to survive Guessing Day.", "The dragons could not choose. Neither could the fire.", "Nobody burned. Nobody won. Everybody is suspicious."],
    art: `<svg class="art" viewBox="0 0 400 240" aria-hidden="true"><circle cx="200" cy="120" r="90" fill="none" stroke="#e8b04a" stroke-width="2" opacity=".5"/>
      <svg x="20" y="20" width="240" height="129" viewBox="-10 -20 410 220">${drake()}</svg><svg x="150" y="100" width="240" height="129" viewBox="-10 -20 410 220">${drake()}</svg></svg>` },
};

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
  document.getElementById("defs").innerHTML = `<symbol id="kn" viewBox="0 0 40 40"><g fill="none">${knot}<path d="M16 12L20 16L16 20L12 16Z" fill="#d8ad5a"/></g></symbol>`
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

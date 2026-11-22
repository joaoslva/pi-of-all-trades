# Guessing Day: contract

Two phones, one Pi. Both players draw a secret character each round. Each asks the other yes/no questions out loud, then presses a button on their own phone. The server owns all state. Phones render the view they are sent.

Port 8090, plain HTTP. Binary `guessing-day`. Flags: `-listen :8090`, `-data /var/lib/guessing-day`, `-web web` (static files dir), `-content content` (characters.json and media/).

## Constants (one place in code, one const block)

- `MaxPlayers = 2`
- `RoundSeconds = 180`, the default round length; `RoundLengths` 120, 180, 300
- `DefaultRounds = 3`, allowed 1..`maxRounds` (10, or fewer when the pool holds under 20 characters: half the pool, rounded down)
- name length 1..20 after trimming
- speed bonus on a correct guess: 2 in the first third of the round, 1 in the second, 0 in the last (`min(2, floor(3 * remaining / seconds))`, `seconds` being the chosen round length). A correct guess is `1 + bonus` points. A forfeit or timeout is 0.
- `ReapAfter = 10 min` with every player disconnected outside the lobby: reset to an empty lobby.

## Phases

`lobby` -> `drawing` -> `playing` -> `roundEnd` -> (`drawing` again, or `final`) -> `lobby` (back_to_lobby).

- lobby: players join, either may change the options with `set_options`, either may `start` once 2 players are present.
- drawing: server picks one secret per player from a shuffle bag, drawing only from the chosen pool. The two are distinct, and a rider is never paired with their own `partner`. The bag keeps every character drawn since its last refill, across games and (via `history.jsonl`) across restarts, so consecutive games do not repeat characters. When a game starts and the bag has fewer free characters of the pool than the game needs, the pool's characters go back in, so no character repeats within one game. `start` returns `400 rounds` if the deck cannot cover the rounds (fewer than 2 characters per round). Each player taps `draw` to reveal their own secret. Playing starts when both have drawn.
- playing: timer runs for the chosen `seconds`. Each player resolves their own guess with `got_it` or `forfeit`. Timeout counts as forfeit for whoever is unresolved. The phase ends when both are resolved.
- roundEnd: scores updated, both secrets revealed to both. Either player taps `next` (idempotent, the first tap wins). After the last round the phase is `final`.
- final: scoreboard, winner or tie. Either player `back_to_lobby` (scores reset, players stay).

`got_it` and `forfeit` apply to the pressing player's OWN guess of the other player's secret. They are idempotent: a second press in the same round is ignored.

## HTTP

All JSON. Errors are `{"error":"code","message":"text"}` with a 4xx status.

- `POST /api/join` body `{"name":"Violet","token":"<optional>"}` -> `{"token":"<hex32>","view":<View>}`.
  - A known token is a rejoin: allowed in any phase, the name is kept.
  - A new token while the lobby has room: a new player.
  - A new token while a game runs or the lobby is full: `409 {"error":"bonded","message":"The dragons have decided not to bond with you. Try again next year..."}`.
- `GET /api/events?token=<t>` Server-Sent Events. First event is the current view, then one `event: view` per change, plus a keepalive comment every 15 s. 401 for an unknown token. A dropped stream marks the player disconnected, a new stream marks them connected.
- `POST /api/action?token=<t>` body `{"type":"...", ...}`. Types: `set_options`, `start`, `draw`, `got_it`, `forfeit`, `next`, `back_to_lobby`, `leave`. Returns the caller's current view. Illegal in this phase: `409 {"error":"phase"}`.
  - `set_options {"rounds":n?, "seconds":n?, "hints":b?, "pool":"?"}`: any subset, lobby only (else 409 `phase`), idempotent, all or nothing. `400 rounds` outside 1..`maxRounds` of the resulting pool, `400 seconds` outside 120/180/300, `400 pool` unless `all` (every kind, `other` included), `riders` or `dragons`, or when that pool cannot cover one round. Changing the pool without `rounds` lowers `rounds` to the new `maxRounds` if needed. Options survive `back_to_lobby` and return to the defaults (3, 180, true, `all`) when the lobby empties.
  - `leave` in the lobby removes the player. During a game it aborts the game back to the lobby with scores reset, for the player who stays.
- `GET /api/history` -> last 20 finished games, newest first: `[{"finished":"RFC3339","rounds":3,"players":[{"name":"A","score":5},{"name":"B","score":4}]}]`.
- `GET /m/<opaque>/clip` and `GET /m/<opaque>/poster` serve media. `<opaque>` comes only from the holder's own `secret` and dies when the round ends (404 after).
- Anything else serves the static `-web` dir with `Cache-Control: no-cache` (phones revalidate and get a 304, so a deploy shows up on the next load).

## View (per player, never contains the other player's secret before roundEnd)

```json
{
  "phase": "playing",
  "serverNow": 1760000000000,
  "you":   {"name": "A", "score": 3},
  "other": {"name": "B", "score": 2, "connected": true},
  "options": {"rounds": 3, "seconds": 180, "hints": true, "pool": "all"},
  "round": 2,
  "canStart": false,
  "drawn":    {"you": true, "other": false},
  "resolved": {"you": null, "other": "got_it"},
  "deadline": 1760000180000,
  "secret": {"name": "Tairn", "kind": "dragon", "color": "#1b1b1b", "clip": "/m/9f2c.../clip", "poster": "/m/9f2c.../poster", "tips": ["vague", "more specific"]},
  "reveal": {"yourGuess": {"name": "Violet Sorrengail", "kind": "rider", "color": "#1b1b1b"}, "theirGuess": {"name": "Tairn", "kind": "dragon", "color": "#1b1b1b"}},
  "gain": {"you": 2, "other": 0},
  "final": {"winner": "A", "tie": false}
}
```

- `other` is null in the lobby until someone joins. `deadline` is unix ms, only in `playing`. Clients compute remaining time with `deadline - (serverNow + elapsedSinceReceipt)`.
- `secret` is present only for the holder, only after they sent `draw`, only until roundEnd. `clip` and `poster` are omitted when the character has no media. `tips` (hints for the holder, vague to specific) appears only here, never in `reveal`, and is omitted when `options.hints` is false. `color` (the card frame, `#rrggbb`) is in `secret` and in both `reveal` entries, omitted when the character has none. A client with no clip shows the rune reveal.
- `resolved.you` / `resolved.other` is `null`, `"got_it"` or `"forfeit"`.
- `reveal` and `gain` only in `roundEnd` and `final`. `final` only in `final`.
- `round` counts from 1. `options` are the lobby settings, in every phase.

## Content

`content/characters.json` is an array of `{id, name, kind, partner, color?, tips?, books, importance, confidence, source, notes?, clip?, poster?}`. The game reads `id`, `name`, `kind`, `partner` (the bag never pairs a rider with their partner), `color`, `tips`, `clip` and `poster`. `kind` is `rider`, `dragon` or `other`. `clip` and `poster` are file names under `content/media/`, optional. The server maps each `(round, player)` to a random opaque token. File names and character ids never reach the other player.

## History file

On entering `final`, append one JSON line to `<data>/history.jsonl`: the `/api/history` object plus `"chars": ["id", ...]`, every secret id of that game in draw order. Never write on any other event. `/api/history` never shows `chars`.

On startup the server reads the file (missing file, blank or corrupt lines, and old lines without `chars` are skipped), takes the last 20 games, and seeds the bag with their ids, newest first, stopping while at least one default game's worth (6) of characters stays free.

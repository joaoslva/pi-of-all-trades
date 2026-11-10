# Guessing Day

A two-player guessing game for two phones, served by the Pi on port 8090. Both players draw a secret Fourth Wing character or dragon each round, ask each other yes/no questions out loud, and press **Got it** or **Forfeit** on their own phone.

Contract (phases, endpoints, the per-player view): `services/game/API.md`. Read it before changing the server or the page.

## Layout

```
services/game/
  *.go          pure state machine (game.go), per-player views (view.go), HTTP and SSE (server.go), flags (main.go)
  web/          the page: index.html, app.js, style.css (vanilla, no build step); mock/ has the dev mock and the browser checks
  content/      characters.json, and media/ for clips and posters (optional per character)
  rootfs/       the systemd unit; make build also stages web/ and content/ under usr/share/guessing-day/
```

## Rules the server enforces

- Two players at most. A third, or anyone joining mid-game, gets "The dragons have decided not to bond with you. Try again next year...".
- No accounts. A random token in the phone's `localStorage` lets a phone rejoin after a screen lock or a Wi-Fi blip.
- A correct guess scores 1, plus 2 in the first third of the 180 s round, plus 1 in the second third. A forfeit or timeout scores 0. Constants are in `game.go`.
- The guesser's view never contains the holder's secret: not the name, the id, nor the clip path. `TestNoLeakBeforeRoundEnd` fails if it does.
- `history.jsonl` in the data dir gets one line when a game ends, and nothing else writes to the SD card.

## Characters and clips

`content/characters.json` is an array. The game reads `id`, `name` and `kind` (`rider`, `dragon` or `other`). Add `clip` and `poster` (file names under `content/media/`) to give a character an animation. With no clip the page plays a CSS ember reveal of the name.

Clip format: portrait, about 720x1280, H.264 mp4, a few hundred kB, action in the centre because the page crops with `object-fit: cover`. The poster is the final frame as WebP or PNG.

## Run and test

```bash
make -C services/game test                     # go vet + go test (race-clean)
cd services/game && go run . -web web -content content -data /tmp/gd -listen :18090
node services/game/web/mock/live.mjs http://localhost:18090 /tmp/shots   # two phones, real server
platform/deploy.sh game                        # after make -C services/game build
```

The hub lists it as the **Guessing Day** button once `hub/rootfs/etc/pihub/services.d/game.json` is deployed and pihub restarted.

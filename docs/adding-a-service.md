# Adding a service

A service is one JSON file in `hub/rootfs/etc/pihub/services.d/`, one or more systemd units, and (for a web service) one port. Nothing in the hub's Go code changes.

## The JSON file

| field | meaning |
| --- | --- |
| `id` | short unique name, used in the API (`switch`, `stop`) and the state file |
| `name` | label on the landing page and admin page |
| `description` | one line under the name on the landing page |
| `class` | `exclusive` or `background` (below) |
| `units` | systemd units that make up the service. Starting the service starts all of them, stopping stops all of them |
| `port` | TCP port of its web page. `0` means no page |

Unknown fields, duplicate ids and duplicate ports make the hub refuse to start.

## Classes

- `exclusive`: heavy. At most one runs. Switching is stop the old one, then start the new one, behind a two-step confirm in `/admin`. It gets a landing page button (needs a port from 1 to 65535) and may not use template units.
- `background`: light. Several may run. The admin page lists it read-only and the hub never starts or stops it. It never gets a landing button. A template unit such as `camera-offload@.service` means any instance; it counts as running when `systemctl list-units 'camera-offload@*'` shows an active one.

The admin page shows a warning banner when more than one exclusive service runs, when more than 3 background services run, or when less than 120 MB of memory is free. Warnings never block anything.

## Port contract

The service listens on its own port on all interfaces and serves plain HTTP. The landing page links to `http://<host the browser used>:<port>/`. Pick a port no other JSON file uses (the camera is 8081; the hub owns 80).

## Polkit

The hub runs as `joao` and starts and stops units through polkit. Add the new unit names, with `.service`, to the `units` list in `hub/rootfs/etc/polkit-1/rules.d/50-pihub.rules`. That is one line. Without it the switch fails with an authentication error. Only start, stop and restart of the listed units are allowed.

## Worked example: a game

A game server, `game.service`, serves its page on port 8090 and is heavy.

1. `hub/rootfs/etc/pihub/services.d/game.json`:

       {
         "id": "game",
         "name": "Game",
         "description": "Games on the Pi",
         "class": "exclusive",
         "units": ["game"],
         "port": 8090
       }

2. In `50-pihub.rules` change the list to `["camera-drive-web.service", "camera-live.service", "game.service"]`.
3. Ship the unit file `game.service` in the same component's rootfs, without `WantedBy=` if it should only run when switched to.
4. `platform/deploy.sh hub`, then restart pihub so it reads the new JSON. `/` shows a Game button and `/admin` offers "Switch to Game", whose dialog says "Stop Camera and start Game?".

## State

`/var/lib/pihub/state.json` is `{"active":"camera"}`, or `{"active":""}` after an exclusive service was switched off on purpose. A switch writes it first, so a crash half-way is repaired when the hub starts: it re-runs the switch for the saved service. With no state file the hub adopts the one exclusive service that is running, if there is exactly one, and otherwise touches nothing.

## Known limit

Plugging the camera in live mode while Camera is switched off still starts an idle `camera-live` through udev. The hub does not intercept that.

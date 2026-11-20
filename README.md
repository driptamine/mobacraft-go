# mobacraft-go

Go server for the [mobacraft](../mobacraft) frontend
(`src/net/multiplayer.ts`). One process hosts **many lobbies, one room (game) each**.

## Protocol (matches `src/net/multiplayer.ts`)

- **WebTransport** `https://<host>:4433/grimcraft?lobby=<id>` — used by the multiplayer lobby UI.
  Client opens one bidi stream, writes 4-byte LE length-prefixed JSON frames:
  `{"Join":{"preferred_team":…}}`, then `{"Command":{…}}`.
  Server answers on a uni stream with framed
  `{"Session"…}`, `{"Snapshot"…}`, `{"Delta"…}`, `{"CmdAck"…}`.
- **WebSocket** `ws://<host>:8080/join?lobby=<id>&team=Blue&username=…&faction=…` —
  same JSON messages, unframed, one text message each. The Multiplayer tab uses it if WebTransport is unavailable.
- **Lobby browser** `GET /api/lobbies` · `POST /api/lobbies {name, map, capacity}`
  (shape matches `src/net/lobbies.ts`), plus `GET /api/health`.

The server runs entirely in Go: rooms, WebTransport and authoritative simulation.
It does not start subprocesses and does not require Node.js, npm or the frontend
checkout. The Go simulation is the authoritative skirmish implementation: movement, melee/ranged
stances, combat, builders, construction, production queues, reinforcement with cost/time,
grenade/plasma upgrades, resources and income, point capture with listening posts,
research, demolish, transports/flyers (as movables), win detection and schema-2 snapshots.
Unknown commands are still explicitly rejected.

## Run

Requires Go only.

```sh
go run ./cmd/server                 # HTTP :8080 + WebTransport :4433
go run ./cmd/server --no-wt         # HTTP/WS only (local dev)
```

First start generates `certs/cert.pem` + `key.pem` and prints the wiring block.
The server advertises its certificate fingerprint and WebSocket path at
`/api/connection`. The browser reloads this metadata each time it joins a room.
For a remote deployment, set `VITE_MULTIPLAYER_API_URL` when building the
frontend. Set `--wt-public-url https://your-host:4433/grimcraft` when the
advertised endpoint differs from the listen address.

Lobby IDs are random eleven-character URL-safe strings (for example
`uyx9VVMaH9M`), generated with `crypto/rand`. Share the frontend link
`/server/<id>` to open that specific lobby directly.

## Two local browser sessions

1. Run this server with `go run ./cmd/server` or use `--no-wt` for WebSocket-only operation.
2. In `../mobacraft`, run `npm run dev` and open `http://localhost:40000`.
3. Open Multiplayer, wait for **GO SERVER · ONLINE**, then Create Game and double-click a map.
4. Copy the lobby address `http://localhost:40000/server/<room-id>`. Open it in incognito to join from the same machine.
5. The first player is host and presses Start Game. Both browsers load the same room.

The client discovers WebTransport URL and the current certificate SHA-256 from
`GET http://127.0.0.1:8080/api/connection` (uncached), connects to QUIC/UDP `4433`,
and sends framed Join with username, faction and preferred team. Certificate
pinning remains enabled; no manual hash entry or certificate bypass is needed.
`--no-wt` leaves the WebSocket endpoint available for the same room. The lobby
browser accepts maps with Go layouts, including Dota 2. In Dota 2 the lobby seats
one player on each side and `SetLobbyHero` changes the sender's hero before the
match begins. `SetLobbyFaction` serves the RTS maps. `StartGame` is host-only
and may include computer opponents. Dota hero movement, basic combat, bases and
creep waves are simulated in Go at 10 ticks/s and replicated to both players.
Players join Dota rooms before the match begins. To refresh the embedded Dota
layout, navigation grid and unit stats, run
`node --import tsx scripts/sync-dota-multiplayer.mjs` in `../mobacraft`.

Validation:

```sh
go test -race ./...
cd ../mobacraft
npm test
npm run test:ts
```


## Layout

- `cmd/server` — entrypoint, flags, self-signed cert + hash print
- `internal/protocol` — wire types mirroring the JS client
- `internal/sim` — authoritative world (move/attack/reinforce, snapshot/delta)
- `internal/hub` — lobbies (one room each), join/team balance, command dispatch
- `internal/transport` — LE framing, WebSocket + WebTransport handlers
- `internal/server` — lobby HTTP API + tests

# fleet

Leaderless, quorum-free shared state across a fleet of shelleys.

- **Transport**: [tailcat](https://github.com/tailscale/tailcat) (WireGuard + NAT
  traversal + DERP bootstrap, no control plane). Every node runs one tailcat
  server and dials peers as a client. The WireGuard pre-shared key is derived
  from `fleet.secret`; published addresses omit it, so the secret is the gate.
  Node identity = tailcat node key, persisted in `<db>-fleet.db`.
- **Replication**: each node appends to its own op log only. Peers exchange
  version vectors and copy missing ops (pull, then push). State is the
  last-writer-wins fold (hybrid logical clock, node id tiebreak).
- **Discovery**: each node writes `node/<id>` = `{name, addr, seen}`. Joining
  any one member pulls the whole roster; it persists, so a join survives
  restarts.

## Config (shelley.json)

```json
{"fleet": {"name": "alpha", "secret": "…"}}
```

## Bootstrap

```
alpha$ shelley fleet addr            # prints tco2Fw…
beta$  shelley fleet join tco2Fw…    # beta now knows alpha, and anyone alpha knows
```

## Local API (on the shelley port)

```
GET    /api/fleet                 id, addr, peers (+ last sync / error)
POST   /api/fleet/join            {"addr": "tc…"}
GET    /api/fleet/kv?prefix=p
GET    /api/fleet/kv/{key}
PUT    /api/fleet/kv/{key}        JSON body
DELETE /api/fleet/kv/{key}
```

`fleet.Transport` is an interface; tests use a loopback TCP implementation so
multi-node replication runs in-process without DERP.

## CLI

`shelley fleet status|addr|join ADDR|ls [PREFIX]|get KEY|put KEY [JSON]|rm KEY`
(talks to the local server over the Unix socket; `-url` to override).

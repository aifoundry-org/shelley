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
- **Discovery**: each node writes `node/<id>` = `{name, addr, seen}`; one seed
  address is enough to find everyone.

## Config (shelley.json)

```json
{"fleet": {"name": "alpha", "secret": "…", "seeds": ["tco2Fw…"]}}
```

## Local API (on the shelley port)

```
GET    /api/fleet                 id, addr, peers (+ last sync / error)
GET    /api/fleet/kv?prefix=p
GET    /api/fleet/kv/{key}
PUT    /api/fleet/kv/{key}        JSON body
DELETE /api/fleet/kv/{key}
```

`fleet.Transport` is an interface; tests use a loopback TCP implementation so
multi-node replication runs in-process without DERP.

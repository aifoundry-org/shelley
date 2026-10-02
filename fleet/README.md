# fleet

Leaderless, quorum-free shared state across a fleet of shelleys.

- **Membership is state, not config.** `shelley fleet init` creates a fleet;
  `shelley fleet join INVITE` enters one; `shelley fleet leave` forgets it.
  Identity (node key, fleet PSK, name) and state live in `<db>-fleet.db`,
  separate from shelley's main database. Nothing in shelley.json.
- **Transport**: [tailcat](https://github.com/tailscale/tailcat) (WireGuard + NAT
  traversal + DERP bootstrap, no control plane). Every node runs one tailcat
  server and dials peers as a client. All nodes share one WireGuard pre-shared
  key, generated at `init`. An *invite* is this node's tailcat address with the
  PSK embedded (treat it as a secret); the address published to peers omits it.
- **Replication**: each node appends to its own op log only. Peers exchange
  version vectors and copy missing ops (pull, then push). State is the
  last-writer-wins fold (hybrid logical clock, node id tiebreak).
- **Discovery**: each node writes `node/<id>` = `{name, addr, seen}` every
  2 minutes and retracts it on leave. Joining any one member pulls the whole
  roster. A node not seen for 24h is dropped: peers stop dialing it and the
  first heartbeat to notice tombstones its entry. If it returns, its fresher
  heartbeat wins the entry back.

## Bootstrap

```
alpha$ shelley fleet init -name alpha
alpha$ shelley fleet invite              # prints tcpGFw… (secret)
beta$  shelley fleet join -name beta tcpGFw…
beta$  shelley fleet status
```

## Local API (on the shelley port)

```
GET    /api/fleet                 {joined:false} | id, name, addr, peers
POST   /api/fleet/init            {"name": …}
GET    /api/fleet/invite          {"invite": "tc…"}
POST   /api/fleet/join            {"name": …, "invite": "tc…"}
POST   /api/fleet/leave
GET    /api/fleet/kv?prefix=p
GET    /api/fleet/kv/{key}
PUT    /api/fleet/kv/{key}        JSON body
DELETE /api/fleet/kv/{key}
```

`fleet.Transport` is an interface; tests use a loopback TCP implementation so
multi-node replication runs in-process without DERP.

## CLI

`shelley fleet init|invite|join INVITE|leave|status|ls [PREFIX]|get KEY|put KEY [JSON]|rm KEY`
(talks to the local server over the Unix socket; `-url` to override).

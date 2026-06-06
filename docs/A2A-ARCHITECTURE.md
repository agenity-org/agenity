# chepherd A2A architecture — component map

How an **A2A-unaware agent** (claude-code, codex-cli, aider, …) is wrapped so it
can send and receive A2A (agent-to-agent) messages — locally, to a directly
reachable peer, and to a zero-inbound remote party via the central hub.

> Names below are the real code symbols/files. The agent speaks only **stdio +
> MCP**; everything that makes it A2A-capable is chepherd's wrapping.

---

## Component map — agents A + B share Host 1, agent C is remote on Host 2

Containment: **agent ⊂ session ⊂ daemon**. The wrappers (MCP bridge, runner) are
per-session; the Deliverer, knock, HubSignaler and state are shared at the daemon.
A↔B talk **locally** (knock only, no hub). A/B↔C talk **over the internet** via a
WebRTC DataChannel the hub helps negotiate but never reads.

```mermaid
flowchart LR
  classDef agent fill:#1d2b3a,stroke:#4f9dd9,color:#dce8f5
  classDef ours fill:#15241c,stroke:#2fbf8f,color:#d6f5e8
  classDef hub fill:#2a1f10,stroke:#e69f00,color:#f6e6c8
  classDef store fill:#1a1a1a,stroke:#888,color:#ccc

  subgraph H1["HOST 1 — daemon · zero inbound"]
    direction TB
    subgraph SA["session A"]
      A["Agent A · claude-code<br/><i>stdio+MCP</i>"]:::agent
      RA["bridge A + runner A<br/>/a2a/A · card"]:::ours
      A <--> RA
    end
    subgraph SB["session B"]
      B["Agent B · claude-code<br/><i>stdio+MCP</i>"]:::agent
      RB["bridge B + runner B<br/>/a2a/B · card"]:::ours
      B <--> RB
    end
    D1{"Deliverer<br/>local·direct·hub"}:::ours
    K1["knock<br/><code>[chepherd-knock…]</code>→get_task"]:::ours
    HS1["HubSignaler<br/>+ answerer"]:::ours
    ST1[("state<br/>Task·Channel·Cards")]:::store
    RA --> D1
    RB --> D1
    RA -.- ST1
    RB -.- ST1
    D1 -- "local A↔B" --> K1
    K1 -. wake .-> A
    K1 -. wake .-> B
    D1 -- "remote→hub" --> HS1
  end

  subgraph HUB["chepherd-hub · internet · signal.openova.io"]
    direction TB
    REG["registry /v1/registry"]:::hub
    SIG["signaling /v1/signaling<br/><i>body-blind relay</i>"]:::hub
    TURN["STUN / TURN /v1/turn"]:::hub
  end

  subgraph H2["HOST 2 — remote daemon · zero inbound"]
    direction TB
    subgraph SC["session C"]
      C["Agent C · claude-code<br/><i>stdio+MCP</i>"]:::agent
      RC["bridge C + runner C<br/>/a2a/C · card"]:::ours
      C <--> RC
    end
    D2{"Deliverer"}:::ours
    K2["knock→get_task"]:::ours
    HS2["HubSignaler<br/>+ answerer"]:::ours
    ST2[("state")]:::store
    RC --> D2
    RC -.- ST2
    D2 -- "remote→hub" --> HS2
    HS2 --> K2
    K2 -. wake .-> C
  end

  %% control plane only — discovery + signaling + TURN (NEVER payload)
  HS1 -. "register · SDP/ICE · TURN" .-> HUB
  HS2 -. "register · SDP/ICE · TURN" .-> HUB

  %% data plane — encrypted A2A payload, peer-to-peer, hub blind
  HS1 <-->|"WebRTC DataChannel · dc_jsonrpc<br/>DTLS · P2P · hub never sees it"| HS2
  HS1 --> K1
```

---

## Layers, in one line each

| Layer | Component | Role |
|---|---|---|
| **Agent** | claude-code / codex / aider … | A2A-unaware; stdio + MCP only |
| **Speak** | MCP bridge (`chepherd mcp`, `internal/mcpserver`) | agent's stdio MCP → daemon HTTP/WS; carries `send_to_session`, `get_task`, `list_sessions` |
| **Be addressable** | runner (`cmd/runner`) | per-session A2A endpoint `/a2a/<sid>/jsonrpc` + Agent Card; captures the reply as the task artifact |
| **Inbound wake** | knock (`internal/runtime/knock`) | one PTY marker `[chepherd-knock taskID=… from=…]` → agent fetches via `get_task` |
| **Route outbound** | Deliverer (`internal/a2a.Deliverer`) | local→knock · direct→`FederatedDeliverer` · zero-inbound→`HubDeliverer` |
| **Cross-host transport** | `HubSignaler` + WebRTC DataChannel (`dc_jsonrpc`) | encrypted P2P A2A; negotiated via the hub, payload never touches it |
| **Central (remote)** | `chepherd-hub` (`cmd/chepherd-hub`) | registry + body-blind signaling relay + STUN/TURN — control plane only |

---

## Message round-trips

### Local — A → B (same host, no hub)

```mermaid
sequenceDiagram
  autonumber
  participant A as Agent A
  participant D1 as Deliverer (Host 1)
  participant K1 as knock (Host 1)
  participant B as Agent B
  A->>D1: chepherd.send_to_session("B", body)  (MCP tool)
  D1->>K1: B is local → knock path (no hub, no network)
  K1->>B: [chepherd-knock taskID=… from=A]
  B->>K1: chepherd.get_task(taskID) → reads, replies
  Note over K1: silence-finalize → reply = task artifact
  K1-->>A: task completed (artifact = B's reply)
```

### Remote — A → C (over the internet, via the hub)

```mermaid
sequenceDiagram
  autonumber
  participant A as Agent A (Host 1)
  participant HS1 as HubSignaler (Host 1)
  participant H as chepherd-hub
  participant HS2 as HubSignaler (Host 2)
  participant K2 as knock (Host 2)
  participant C as Agent C (Host 2)

  A->>HS1: send_to_session("C", body) → Deliverer: C = hub-only peer
  Note over HS1,H: first contact only — negotiate transport
  HS1->>H: POST /v1/signaling/offer (SDP) + /ice + TURN creds
  HS2->>H: long-poll /v1/signaling/pending → gets offer
  HS2-->>H: POST /v1/signaling/answer
  Note over HS1,HS2: WebRTC DataChannel up (DTLS, P2P)<br/>TURN-relayed only if NAT requires
  HS1->>HS2: A2A JSON-RPC over dc_jsonrpc (hub blind to payload)
  HS2->>K2: deliver → knock marker
  K2->>C: [chepherd-knock taskID=… from=A]
  C->>K2: chepherd.get_task(taskID) → reads, replies
  K2-->>HS1: response over the same DataChannel
  HS1-->>A: task completed (artifact = C's reply)
```

---

## Invariants worth remembering

- **The agent never knows it's in a mesh.** It calls MCP tools and reads its terminal; the runner + bridge + knock do everything else.
- **Zero inbound on either host.** Both daemons reach *out* to the hub; the hub relays signaling and (if needed) TURN. No daemon opens an inbound port to the internet.
- **The hub is control-plane only.** It brokers discovery + the WebRTC handshake + TURN credentials. The A2A payload rides the DTLS-encrypted DataChannel peer-to-peer — the hub cannot read it.
- **One mental model:** *A2A-unaware agent ↔ MCP bridge (speak) + runner (addressable) → Deliverer picks the path → knock (local) · FederatedDeliverer (direct remote) · HubDeliverer over WebRTC (zero-inbound remote, negotiated via chepherd-hub).*

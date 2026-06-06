# chepherd A2A architecture — component map

How an **A2A-unaware agent** (claude-code, codex-cli, aider, …) is wrapped so it
can send and receive A2A (agent-to-agent) messages — locally, to a directly
reachable peer, and to a zero-inbound remote party via the central hub.

> Names below are the real code symbols/files. The agent speaks only **stdio +
> MCP**; everything that makes it A2A-capable is chepherd's wrapping.

---

## Component map

```mermaid
flowchart TB
  classDef agent fill:#1d2b3a,stroke:#4f9dd9,color:#dce8f5
  classDef ours fill:#15241c,stroke:#2fbf8f,color:#d6f5e8
  classDef hub fill:#2a1f10,stroke:#e69f00,color:#f6e6c8
  classDef wire fill:#241626,stroke:#cc79a7,color:#f0dcec
  classDef store fill:#1a1a1a,stroke:#888,color:#ccc

  subgraph HostA["HOST A — daemon · zero inbound to internet"]
    direction TB
    AgentA["<b>A2A-UNAWARE AGENT</b><br/>claude-code / codex-cli / aider / qwen …<br/><i>stdio + MCP only — knows nothing about A2A</i>"]:::agent
    MCPBridge["<b>MCP bridge</b> — it can <i>speak</i><br/>chepherd mcp · stdio → HTTP/WS<br/>internal/mcpserver"]:::ours
    Knock["<b>knock</b> — inbound wake<br/>internal/runtime/knock<br/>writes <code>[chepherd-knock taskID=… from=…]</code>"]:::ours
    Runner["<b>RUNNER</b> — it's <i>addressable</i> (agent's A2A face)<br/>cmd/runner · /a2a/&lt;sid&gt;/jsonrpc<br/>/.well-known/agent-card.json · silence-finalize→artifact"]:::ours
    Deliverer{"<b>Deliverer</b><br/>internal/a2a.Deliverer<br/>picks the path"}:::ours
    State[("daemon state<br/>TaskStore · ChannelStore<br/>AgentCards · event bus")]:::store

    AgentA -- "①  MCP tool calls<br/>send_to_session · get_task · list_sessions" --> MCPBridge
    MCPBridge --> Runner
    Knock -- "②  marker line → agent calls get_task" --> AgentA
    Runner --> Deliverer
    Runner -.- State
    Deliverer -- "local peer" --> Knock
  end

  subgraph Hub["CENTRAL HUB — chepherd-hub · signal.openova.io · cmd/chepherd-hub"]
    direction TB
    Reg["<b>Registry</b><br/>/v1/registry/announce · /peers<br/><i>parties discover each other</i>"]:::hub
    Sig["<b>Signaling relay</b> (body-blind)<br/>/v1/signaling/offer · answer · ice · pending"]:::hub
    Turn["<b>NAT traversal</b><br/>/v1/turn/credentials + STUN/TURN"]:::hub
  end

  DC(["<b>WebRTC DataChannel</b> · dc_jsonrpc<br/>DTLS-encrypted · A2A payload P2P<br/><i>hub NEVER sees the payload</i>"]):::wire

  subgraph HostB["HOST B — independent party · mirror · zero inbound"]
    direction TB
    HubSig["<b>HubSignaler</b> + answerer loop<br/>internal/webrtcrtc"]:::ours
    RunnerB["runner → MCP bridge → agent<br/>→ knock"]:::ours
    HubSig --> RunnerB
  end

  %% routing paths out of the Deliverer
  Deliverer == "direct remote · HTTP POST /jsonrpc<br/>(FederatedDeliverer)" ==> RunnerB
  Deliverer == "zero-inbound remote<br/>(HubDeliverer)" ==> DC
  DC ==> RunnerB

  %% control plane only — signaling + discovery + TURN (never payload)
  Deliverer -. "SDP / ICE / TURN creds<br/>(control plane only)" .-> Hub
  HubSig -. "long-poll /signaling/pending" .-> Hub
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

## Message round-trip (alice → bob, zero-inbound remote)

```mermaid
sequenceDiagram
  autonumber
  participant AA as Agent A (claude-code)
  participant MB as MCP bridge (A)
  participant DA as Deliverer / HubDeliverer (A)
  participant H as chepherd-hub
  participant HB as HubSignaler (B)
  participant RB as runner + knock (B)
  participant AB as Agent B (claude-code)

  AA->>MB: chepherd.send_to_session("bob", body)  (MCP tool call)
  MB->>DA: A2A message → pick path (bob = hub-only peer)
  Note over DA,H: first contact only — negotiate transport
  DA->>H: POST /v1/signaling/offer (SDP) + /ice
  HB->>H: long-poll /v1/signaling/pending → gets offer
  HB-->>H: POST /v1/signaling/answer (SDP/ICE)
  Note over DA,HB: WebRTC DataChannel established (DTLS, P2P)<br/>TURN-relayed only if NAT requires
  DA->>HB: A2A JSON-RPC over dc_jsonrpc (payload — hub blind)
  HB->>RB: deliver task → write knock marker
  RB->>AB: [chepherd-knock taskID=… from=alice]
  AB->>RB: chepherd.get_task(taskID) → reads body, replies
  Note over RB: silence-finalize captures reply as task artifact
  RB-->>DA: A2A response over the same DataChannel
  DA-->>AA: task completed (artifact = bob's reply)
```

---

## Invariants worth remembering

- **The agent never knows it's in a mesh.** It calls MCP tools and reads its terminal; the runner + bridge + knock do everything else.
- **Zero inbound on either host.** Both daemons reach *out* to the hub; the hub relays signaling and (if needed) TURN. No daemon opens an inbound port to the internet.
- **The hub is control-plane only.** It brokers discovery + the WebRTC handshake + TURN credentials. The A2A payload rides the DTLS-encrypted DataChannel peer-to-peer — the hub cannot read it.
- **One mental model:** *A2A-unaware agent ↔ MCP bridge (speak) + runner (addressable) → Deliverer picks the path → knock (local) · FederatedDeliverer (direct remote) · HubDeliverer over WebRTC (zero-inbound remote, negotiated via chepherd-hub).*

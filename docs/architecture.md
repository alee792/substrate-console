# Architecture

The diagrams follow the [C4 model](https://c4model.com). Dark blue is a
person, blue is ours, grey is someone else's, and a dashed box is planned.

## Context

The console is one Go binary. It is an MCP server whose tools wrap
Substrate's control API, Kubernetes and Prometheus, and it serves the view
those tools render. It runs inside an assistant (Claude, ChatGPT, Codex,
VS Code) or on its own local page. It knows nothing about agents; it is a
console for any Substrate cluster.

```mermaid
flowchart LR
  op["<b>Cluster operator</b><br/>[Person]"]
  host["<b>AI host app</b><br/>[External]<br/>Claude, ChatGPT, Codex, VS Code"]
  console["<b>substrate-console</b><br/>[Software system]<br/>Shows what needs attention<br/>and fixes it"]
  substrate["<b>Agent Substrate</b><br/>[External]<br/>ateapi, WorkerPools"]
  prom["<b>Prometheus</b><br/>[External]"]
  op -->|"asks and acts in chat"| host
  host -->|"MCP tools and views"| console
  op -.->|"or the local page"| console
  console -->|"ateapi and Kubernetes API"| substrate
  console -->|"PromQL"| prom
  classDef person fill:#08427b,stroke:#052e56,color:#ffffff
  classDef system fill:#1168bd,stroke:#0b4884,color:#ffffff
  classDef external fill:#8a8f98,stroke:#6b7079,color:#ffffff
  class op person
  class console system
  class host,substrate,prom external
```

## Components

```mermaid
flowchart TB
  host["<b>AI host app</b><br/>Claude, ChatGPT, Codex, VS Code"]
  browser["<b>Browser</b>"]
  subgraph SC["substrate-console binary"]
    direction TB
    cmd["<b>CLI</b><br/>[cmd/substrate-console]<br/>mcp on stdio, or serve on HTTP"]
    shell["<b>HTTP shell</b><br/>[shell]<br/>/mcp with cross-origin protection<br/>and a token; / is a small MCP Apps host"]
    auth["<b>Per-user sign-in</b><br/>[planned]<br/>OAuth on /mcp, forwards<br/>the caller's ateapi token"]
    srv["<b>MCP server</b><br/>[mcpapp]<br/>read and write tools, ui:// views,<br/>MCP Apps and OpenAI keys"]
    rules["<b>Rules</b><br/>[rules]<br/>cluster snapshot to<br/>what needs you"]
    be["<b>Backend</b><br/>[backend, interface]"]
    ate["<b>Substrate backend</b><br/>[backend/ate]<br/>port-forward, ClusterTrustBundle,<br/>minted ate-client token"]
    met["<b>Metrics</b><br/>[metrics]<br/>PromQL"]
  end
  view["<b>View bundle</b><br/>[web, embedded]<br/>Preact, calls tools through the host"]
  api["<b>ateapi</b>"]
  k8s["<b>Kubernetes API</b>"]
  prom["<b>Prometheus</b>"]
  fake["<b>substrate-fake</b><br/>serve --demo and tests"]
  host -->|"stdio"| cmd
  host -->|"streamable HTTP"| shell
  browser --> shell
  cmd --> srv
  shell --> srv
  auth -.-> shell
  srv --> rules
  srv --> be
  srv --> met
  be --> ate
  ate --> api & k8s
  met --> prom
  srv -.->|"serves"| view
  fake -.->|"stands in for"| api & k8s & prom
  class host,browser person
  class cmd,shell,srv,rules,be,ate,met component
  class auth planned
  class view container
  class api,k8s,prom,fake external
  classDef person fill:#08427b,stroke:#052e56,color:#ffffff
  classDef system fill:#1168bd,stroke:#0b4884,color:#ffffff
  classDef container fill:#438dd5,stroke:#2e6295,color:#ffffff
  classDef component fill:#85bbf0,stroke:#5d82a8,color:#0b1a2b
  classDef external fill:#8a8f98,stroke:#6b7079,color:#ffffff
  classDef planned fill:#ffffff,stroke:#1168bd,stroke-dasharray:5 4,color:#0b4884
```

- **CLI** (`cmd/substrate-console`): `mcp` serves on stdio, `serve` on HTTP.
- **HTTP shell** (`internal/shell`): `/mcp` with Go's cross-origin
  protection and a bearer token off loopback, and `/`, a minimal MCP Apps
  host that loads the view into a sandboxed iframe.
- **MCP server** (`internal/mcpapp`): tools, `ui://` view resources, and the
  MCP Apps and OpenAI keys. Every write tool takes `dry_run`.
- **Rules** (`internal/rules`): turns a cluster snapshot into the issues
  list.
- **Backend** (`internal/backend`): the interface the tools use. The `ate`
  backend connects the way `kubectl ate` does: port-forward to
  `ate-system/api`, trust the ClusterTrustBundle, mint an `ate-client`
  token.
- **Metrics** (`internal/metrics`): Prometheus queries.
- **Per-user sign-in** (planned): OAuth on `/mcp`, forwarding the caller's
  own token so Substrate's access policies decide what they can see and do.
  Today every caller acts with the console's kubeconfig, and atespace scope
  only filters the view.

## Tests and demo

`serve --demo` and the tests run against
[substrate-fake](https://github.com/alee792/substrate-fake), which serves
ateapi over TLS, the Kubernetes API parts the console reads, and Prometheus
with the real PromQL engine, all from one in-memory world. Canopy's tests
use the same fake.

```mermaid
flowchart LR
  ct["<b>Canopy tests</b><br/>sandbox provider test"]
  cd["<b>Console tests</b><br/>and serve --demo"]
  subgraph F["substrate-fake"]
    direction TB
    g["<b>ateapi</b><br/>gRPC over TLS or in process"]
    k["<b>Kubernetes API</b><br/>WorkerPools, trust bundle,<br/>tokens, pod logs"]
    p["<b>Prometheus</b><br/>real PromQL engine"]
    r["<b>Router</b><br/>Dial to exposed ports"]
    w[("<b>World</b><br/>actors, templates, workers,<br/>crashes, faults, clock")]
  end
  ct -->|"in-process client, Router.Dial"| g
  ct --> r
  cd -->|"kubeconfig, endpoint, URL"| g
  cd --> k
  cd --> p
  g & k & p & r --> w
  class ct,cd person
  class g,k,p,r component
  class w container
  classDef person fill:#08427b,stroke:#052e56,color:#ffffff
  classDef system fill:#1168bd,stroke:#0b4884,color:#ffffff
  classDef container fill:#438dd5,stroke:#2e6295,color:#ffffff
  classDef component fill:#85bbf0,stroke:#5d82a8,color:#0b1a2b
  classDef external fill:#8a8f98,stroke:#6b7079,color:#ffffff
  classDef planned fill:#ffffff,stroke:#1168bd,stroke-dasharray:5 4,color:#0b4884
```

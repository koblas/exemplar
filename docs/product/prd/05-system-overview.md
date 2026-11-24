<a id="5-system-overview"></a>

## 5. System Overview

```mermaid
flowchart LR
  subgraph L["LEARNING PLANE · offline"]
    A[Review history<br/>750-day lookback] --> B[Extract + embed]
    B --> C[Cluster + merge]
    C --> D[Ranked candidates<br/>with citations]
    D --> DC[Try compile to<br/>deterministic rule]
  end
  DC --> G{{"HUMAN GATE<br/>edit · test · merge config PR"}}
  subgraph E["ENFORCEMENT PLANE · online"]
    H[Effective config] --> S[Deterministic scope]
    S --> L1[Lane 1: compiled rules<br/>Semgrep / ast-grep]
    S --> L2[Lane 2: exemplar agents<br/>one per rule]
    S --> L3[Lane 3: bug lane<br/>narrow, high bar]
    L2 --> V[Verification<br/>refute · importance]
    L3 --> V
    L1 --> IMP[Importance check<br/>lightweight]
    IMP --> VAL[Validate in code<br/>provenance · anchors · coverage]
    V --> VAL
    VAL --> R[Rank + comment budget<br/>+ fixes]
    R --> J[Post · gates · auto-approve]
    J --> K[Structured feedback]
  end
  G --> H
  G --> X[Export to agents & IDEs<br/>AGENTS.md · CLAUDE.md · MCP]
  K -. reconciled feedback .-> A
```

**Invariants**

- On the same PR, learning never mutates the review in progress.
- Long-term, reconciled feedback becomes evidence for the next extraction — still behind the human gate.
- Every model call is constrained; every model output is checked by deterministic code before it reaches a human.
- Cheap and deterministic first; expensive only if needed. A rule that can be expressed deterministically is run deterministically.
- Nothing is posted that has not survived verification (model lanes), an importance check (all lanes) and validation (all lanes).
- A review that did not run to completion says so. Partial coverage, a provider outage or a budget stop is reported as "not reviewed", never as "no findings" (§8.10).

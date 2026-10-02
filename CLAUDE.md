# DIF — Data Integration Framework

Build a minimal viable prototype of **DIF (Data Integration Framework)** in Go.

DIF is a lightweight, dependency-free integration framework inspired by Apache Camel and Spring Integration. It combines:

- Flow-Based Programming (FBP)
- Enterprise Integration Patterns (EIP)
- A Data Integration Language (DIL)

The goal of this MVP is **not** to build a production-ready integration platform. The goal is to establish the smallest possible architecture that proves the core concept.

## Core principles

1. **KISS** — keep the implementation extremely simple.
2. **Zero external dependencies** — use only the Go standard library.
3. **Low resource usage** — avoid unnecessary allocations, goroutines, reflection, frameworks, etc.
4. **Message-oriented** — everything flowing through a flow is a Message.
5. **Data-oriented** — messages can contain structured data.
6. **Flow-based** — a flow consists of connected steps.
7. **EIP-oriented** — steps should eventually represent Enterprise Integration Patterns.
8. **Extensible** — steps must be pluggable without modifying the engine.
9. **DSL-independent engine** — the engine should execute an internal flow model, not depend directly on DIL.
10. **Future state support** — design the message/flow model so state can eventually be persisted, but DO NOT implement persistence in the MVP.

---
name: service-lifecycle-protocol
description: >-
  Execute the 360-degree Service Lifecycle Protocol for designing, implementing, and verifying
  large or complex backend services. Dispatches specialized subagents (BA/UX, Solutions Architect, SecOps/QA)
  to eliminate ambiguity, generate an RFC with trade-off matrices, enforce Inversion of Control (IoC),
  and require multi-tiered evidence before completion.
---

# 360-Degree Service Lifecycle Protocol

This skill codifies the enterprise engineering framework for breaking down, architecting, implementing, and verifying large, ambiguous backend services without blind spots.

---

## 1. Core Invariants & Engineering Guardrails

1. **Strict Pre-Implementation Protocol (NO IMMEDIATE CODE)**:
   The agent is strictly forbidden from writing production implementation code upon receiving a feature request. All services must pass **Quality Gate 1 (User RFC Sign-Off)**.
2. **Inversion of Control (IoC) & Clean Architecture**:
   Application use cases and domain models must depend strictly on domain abstractions (interfaces), never on concrete infrastructure structs (e.g. database connection pools, cloud SDKs, message queue clients).
3. **Evidence-Based Verification**:
   No task is marked complete without executable proof: compile check, 100% mocked unit tests, live integration runs with real data, and failure/chaos recovery evidence.

---

## 2. The 4-Phase Service Lifecycle

```
[User / Feature Request]
           │
           ▼
┌─────────────────────────────────────────────────────────────┐
│ PHASE 1: 360° DISCOVERY & MULTI-PERSPECTIVE PROBING         │
│ Dispatch 3 specialized subagents to probe blind spots:      │
│  1. BA & UX: State machine, edge cases, failure journeys.   │
│  2. Solutions Architect: Protocols, capacity, FinOps, IoC.  │
│  3. SecOps & QA: FMEA risk matrix, poison input, chaos plan.│
└──────────────────────────────┬──────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────┐
│ PHASE 2: TECHNICAL RFC & TRADE-OFF ALIGNMENT GATE           │
│ Synthesize discoveries into `docs/rfcs/XXX-<service>.md`:   │
│  • Gap Analysis & Invariants                                │
│  • Trade-Off Matrix (Option A vs Option B with rationale)   │
│  • FMEA Failure Matrix                                      │
│  • Data Contracts & Database Schema DDL                     │
│  >> QUALITY GATE 1: STOP & REQUEST USER SIGN-OFF <<         │
└──────────────────────────────┬──────────────────────────────┘
                               │ (User Approves RFC)
                               ▼
┌─────────────────────────────────────────────────────────────┐
│ PHASE 3: CONTRACT-FIRST & IoC IMPLEMENTATION                │
│  0. Worktree Provisioning: `make worktree-create NAME=...`  │
│  1. Domain Entities & Value Objects (0 external imports)    │
│  2. Domain Interfaces (Repositories, Storage, Queues)       │
│  3. Application Use Cases (Injects interfaces only)         │
│  4. Infrastructure Adapters (PostgreSQL, Cloud, MQ)         │
│  5. Composition Root (`main.go` wires DI)                   │
└──────────────────────────────┬──────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────┐
│ PHASE 4: EVIDENCE-BASED VERIFICATION & CHAOS PROOF          │
│  • Evidence 1: Compiler & Unit Tests with Mocks (100% pass) │
│  • Evidence 2: Live Happy Path run with real storage/DB     │
│  • Evidence 3: Chaos/Failure Mode run (resume/reconnect)    │
│  • Worktree Teardown & Merge: `make worktree-merge`         │
│  >> Final Delivery Sign-Off <<                              │
└─────────────────────────────────────────────────────────────┘
```

---

## 3. Subagent Orchestration Playbook

When invoked, the **Tech Lead Orchestrator** defines and invokes 3 specialized subagents:

### Subagent 1: `ba-analyst` (BA & Product Experience)
- **Role**: Uncover user journeys, state transitions, and edge cases under network failure.
- **Key Questions**:
  - What happens if the network drops at 1%, 50%, or 99%?
  - How does the client observe progress (byte accuracy, EMA smoothing)?
  - How does pause/resume and cross-session browser refresh work?
- **Output**: `docs/rfcs/discovery/01-ba-ux-analysis.md`.

### Subagent 2: `solutions-architect` (Solutions & Cloud Architecture)
- **Role**: Design protocols, data contracts, infrastructure sizing, and Clean Architecture abstractions.
- **Key Questions**:
  - What is the mathematical trade-off for chunk/batch sizing?
  - How does this impact cloud provider API costs (Class A operations)?
  - What is the database schema DDL and indexes?
  - What are the pure Go domain interfaces?
- **Output**: `docs/rfcs/discovery/02-solutions-architecture.md`.

### Subagent 3: `secops-qa-engineer` (SecOps & Chaos Specialist)
- **Role**: Adversarial FMEA risk analysis, poison input defense, storage leak cleanup, chaos testing.
- **Key Questions**:
  - What is the highest RPN risk (e.g. orphaned partial uploads)?
  - How do we validate file integrity and magic bytes before workers consume data?
  - How does the system recover if a worker process crashes mid-stream?
- **Output**: `docs/rfcs/discovery/03-secops-chaos-analysis.md`.

---

## 4. Phase-by-Phase Instructions for the Agent

1. **Step 1 (Discovery)**:
   - Create directory `docs/rfcs/discovery/`.
   - Define subagents `ba-analyst`, `solutions-architect`, `secops-qa-engineer`.
   - Dispatch all 3 subagents in parallel to author their respective discovery reports.
2. **Step 2 (RFC Generation & Gate 1)**:
   - Read the 3 discovery reports and synthesize them into `docs/rfcs/XXX-<feature-name>.md` using the template at [RFC_TEMPLATE.md](./templates/RFC_TEMPLATE.md).
   - Present the executive summary and trade-off matrix to the user.
   - **HALT EXECUTION**: Wait for user review and explicit sign-off before writing any implementation code.
3. **Step 3 (Implementation in Isolated Worktree)**:
   - **MANDATORY**: Provision a dedicated worktree:
     ```bash
     make worktree-create NAME=<feature-name>
     ```
   - Switch active workspace to `.worktrees/<feature-name>`.
   - Apply database migrations (DDL).
   - Write domain entities and domain interfaces in `internal/domain/<aggregate>/`.
   - Write infrastructure adapters in `internal/infrastructure/<adapter>/`.
   - Write application services in `internal/application/<aggregate>/`.
   - Wire dependencies at the composition root (`cmd/**/main.go`).
4. **Step 4 (Verification & Delivery)**:
   - Run unit tests with mocks: `go test -v ./internal/application/...`.
   - Run live end-to-end integration tests proving real connectivity.
   - Once all evidence is validated, merge worktree to `main` and clean up:
     ```bash
     make worktree-merge NAME=<feature-name>
     make worktree-clean NAME=<feature-name>
     ```
   - Check in code cleanly with git commits.

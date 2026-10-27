# Bitmill Engineering & Agent Rules

## 1. Language & Communication Protocols (MANDATORY)
- **User Communication**: Always communicate, converse, consult, and explain concepts to the user in **Vietnamese (Tiếng Việt)**.
- **Code & Technical Documentation**: Always write 100% in **English (Tiếng Anh)**. This applies strictly to:
  - All source code, struct names, function names, variables, constants, and types.
  - All inline code comments, docstrings, and commit messages.
  - All technical documentation, specs, guides, architecture documents (`docs/**/*.md`), schemas, and DDL scripts.

## 2. Core Architectural & Engineering Guardrails
- **Inversion of Control (IoC) & Interface-First Design (MANDATORY)**:
  - ALWAYS program to interfaces, NEVER to concrete implementations.
  - High-level business logic (Application Services, Use Cases) MUST depend strictly on Domain Interfaces (`video.Repository`, `video.StorageProvider`, `video.JobQueue`, etc.).
  - NEVER accept, pass, or store concrete structs (e.g., `*PostgresRepo`, `*R2Adapter`, `*RabbitMQAdapter`) inside Application services.
  - Interfaces must be declared in the `domain` layer (or by the consumer), completely decoupled from infrastructure.
  - Concrete adapters are instantiated only at the composition root (`cmd/**/main.go`) and injected via constructors (Dependency Injection).
- **DDD & Clean Architecture**: Maintain strict separation of concerns under `go-streaming/internal/` (`domain`, `application`, `infrastructure`, `interfaces`). `domain` must remain 100% dependency-free.
- **Media Engine Isolation**: NEVER install FFmpeg on the host machine. All video processing operations must execute through Docker (`mwader/static-ffmpeg:latest`) or root wrapper scripts (`./ffmpeg.sh`, `./ffprobe.sh`).
- **Configuration**: Always manage application parameters through Viper using `go-streaming/config.yaml` and environment variables.
- **Pre-flight Build Verification**: Always ensure the Go codebase compiles cleanly before reporting completion:
  ```bash
  cd go-streaming && go build ./...
  ```

## 3. Strict Pre-Implementation Protocol (The 5-Element Expert Framework)
- **NO IMMEDIATE IMPLEMENTATION**: The agent is strictly prohibited from jumping straight into writing implementation code or creating multiple code files upon receiving a request.
- **Mandatory 3-Phase Execution**:
  1. **Phase 1 (Decomposition & Analysis)**: Adopt the appropriate expert persona (BA, Solution Architect, Database Architect, Core Engineer, QA/SecOps) using the 5-Element Prompting Framework:
     - `[Role & Persona]`: Domain expert mindset and industry standards.
     - `[Business Context & Domain Rules]`: Invariants, state machine, and SLA.
     - `[Input Schema & Constraints]`: DDL, data contracts, and boundary conditions.
     - `[Technical Guardrails & Security]`: Clean architecture, concurrency safety, IDOR/RBAC, error handling.
     - `[Output Format & Evidence Requirement]`: Gap analysis, trade-off matrix, and verification plan.
  2. **Phase 2 (Clarification & Alignment)**: Identify all fuzzy assumptions, edge cases, and missing requirements. Present clarification questions and design options (Option A vs Option B) to the user.
  3. **Phase 3 (Execution with Evidence)**: Only write code AFTER the user has reviewed and approved the analysis and design. Every implementation must include executable tests or verification evidence.

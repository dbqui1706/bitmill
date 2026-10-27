# 📚 Bitmill Engineering Documentation Hub

Welcome to the technical documentation repository for the **Bitmill** media streaming and VOD platform. The `docs/` folder is structured according to enterprise standards for distributed media architectures.

---

## 🗂️ Documentation Hierarchy (`docs/`)

```
docs/
├── README.md                          # [Root] Central navigation index and technical overview
├── requirements/                      # Functional (FR) & Non-Functional Requirements (NFR)
│   └── upload-service-spec.md         # INVEST User Stories, Acceptance Criteria (BDD/Gherkin), Invariants
├── analysis-design/                   # Business domain analysis, data models, and schemas
│   ├── database-schema.md             # Relational Database Schema (RMDS / PostgreSQL), ERD & DDL
│   └── upload-service-decisions.md    # Architecture Decision Records (ADRs: R2, Event Ingest, ABR, RabbitMQ)
├── architecture/                      # System topology, sequence diagrams, and service specs
│   └── upload-service-architecture.md # Video Upload & Processing Pipeline architectural specification
├── development/                       # Engineering guidelines, tech stack, and task work breakdown
│   └── upload-service-task.md         # Source tree layout, tech stack matrix, and task file mapping
├── security/                          # AuthN/AuthZ, Presigned URL policies, and storage security
├── operations/                        # Docker, Kubernetes, CI/CD, and Observability
└── project-tracking/                  # Roadmap, sprint deliverables, and backlog
```

---

## 📑 Core Documentation Index

| Document | Classification | Key Coverage |
| :--- | :--- | :--- |
| [upload-service-spec.md](file:///Users/quidb/Working/projects/bitmill/docs/requirements/upload-service-spec.md) | **Requirements (BA)** | INVEST User Stories, Gherkin BDD specs, domain invariants, and actor permissions. |
| [upload-service-decisions.md](file:///Users/quidb/Working/projects/bitmill/docs/analysis-design/upload-service-decisions.md) | **Decisions (ADR)** | ADRs: Cloudflare R2 (Zero Egress), Event-driven Ingestion, Multi-bitrate ABR HLS, RabbitMQ & DLQ. |
| [upload-service-architecture.md](file:///Users/quidb/Working/projects/bitmill/docs/architecture/upload-service-architecture.md) | **Architecture** | Architectural spec derived from `upload-service.drawio`: Direct R2 Upload, Presigned URLs, Queue, DLQ, Worker Pool, and CDN. |
| [database-schema.md](file:///Users/quidb/Working/projects/bitmill/docs/analysis-design/database-schema.md) | **Analysis & Design** | RMDS (PostgreSQL) schema: ER diagrams, state machines, and DDL for `videos`, `video_renditions`, and `processing_jobs`. |
| [upload-service-task.md](file:///Users/quidb/Working/projects/bitmill/docs/development/upload-service-task.md) | **Development** | Complete Go codebase map, active tech stack, Clean Architecture rules, and task-specific file inventory. |
| [AI_WORKFLOW.md](file:///Users/quidb/Working/projects/bitmill/AI_WORKFLOW.md) | **Repository Root** | Engineering workflow principles: Deep understanding first, DDD Clean Architecture, and Docker FFmpeg standards. |
| [architecture.md](file:///Users/quidb/Working/projects/bitmill/architecture.md) | **Repository Root** | High-level C4 model architecture of the complete Bitmill system (RTMP, HLS, Chat, VOD). |
| [requirements.md](file:///Users/quidb/Working/projects/bitmill/requirements.md) | **Repository Root** | Functional and non-functional requirements specification. |

---

## 🎯 Architectural Goals for the Video Upload Subsystem
1. **Zero-Server Ingest Bottleneck**: Multi-gigabyte raw video payloads bypass the API server entirely via secure, time-limited Presigned PUT URLs directly to Object Storage (S3 / MinIO).
2. **Decoupled Asynchronous Processing**: CPU-intensive video decoding, scaling, and Adaptive Bitrate (ABR) HLS packaging are offloaded to dedicated worker pools via Message Queues.
3. **Resilience & Fault Tolerance**: Integration with Dead Letter Queues (DLQ) ensures malformed media and runtime crashes are captured with contextual telemetry without starving other tasks.
4. **Clean Architecture & DDD**: Strict layer separation within Golang (`domain`, `application`, `infrastructure`, `interfaces`).

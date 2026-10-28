# 📚 Bitmill Engineering Documentation Hub

Welcome to the technical documentation repository for the **Bitmill** media streaming and VOD platform. The documentation is organized according to the **Lean 3-Pillar Architecture**.

---

## 🗂️ The 3-Pillar Documentation Hierarchy

```
docs/
├── README.md              # [Root] Central navigation index and system overview
├── milestones.md          # [Roadmap] End-to-end milestones, progress, and verification tracker
│
├── rfcs/                  # [PILLAR 1: HISTORICAL DECISIONS & DISCOVERY (Immutable)]
│   ├── discovery/         # Multi-perspective discovery reports (BA/UX, Architect, SecOps)
│   │   ├── rfc-002/       # RFC-002 Discovery reports (BA, Architect, SecOps)
│   │   ├── 01-ba-ux-analysis.md
│   │   ├── 02-solutions-architecture.md
│   │   └── 03-secops-chaos-analysis.md
│   ├── 001-resumable-video-upload.md  # RFC-001: Resumable & Fault-Tolerant Ingestion Engine
│   └── 002-transcoding-worker-orchestration.md # RFC-002: Asynchronous Transcoding Worker & RabbitMQ
│
├── architecture/          # [PILLAR 2: CURRENT STATE & SINGLE SOURCE OF TRUTH (Living Docs)]
│   ├── system-overview.md # System topology, sequence diagrams, and pipeline design
│   ├── database-schema.md # Complete PostgreSQL ERD, DDL, indexes, and triggers
│   └── api-contracts.md   # Active REST API specifications, request/response envelopes
│
└── operations/            # [PILLAR 3: OPERATIONS & RUNBOOKS]
    ├── local-setup.md     # Local development setup, Docker, environment variables, Makefile
    └── runbook.md         # Troubleshooting playbooks, DLQ remediation, R2 storage cleanup
```

---

## 📑 Core Documentation Index

| Pillar | Document | Focus Area |
| :--- | :--- | :--- |
| **Roadmap** | [milestones.md](file:///Users/quidb/Working/projects/bitmill/docs/milestones.md) | High-level roadmap, deliverables, and verification evidence tracker. |
| **Pillar 1: RFCs** | [RFC-001](file:///Users/quidb/Working/projects/bitmill/docs/rfcs/001-resumable-video-upload.md) | Architectural decision record for Resumable Direct-to-R2 S3 Multipart Upload. |
| **Pillar 1: RFCs** | [RFC-002](file:///Users/quidb/Working/projects/bitmill/docs/rfcs/002-transcoding-worker-orchestration.md) | Asynchronous Video Transcoding Worker & RabbitMQ Orchestration Engine. |
| **Pillar 2: Architecture** | [system-overview.md](file:///Users/quidb/Working/projects/bitmill/docs/architecture/system-overview.md) | High-level system architecture, ABR HLS ladder, and RabbitMQ event pipeline. |
| **Pillar 2: Architecture** | [database-schema.md](file:///Users/quidb/Working/projects/bitmill/docs/architecture/database-schema.md) | Relational database schema (PostgreSQL 15+), ERD, table DDL, and triggers. |
| **Pillar 2: Architecture** | [api-contracts.md](file:///Users/quidb/Working/projects/bitmill/docs/architecture/api-contracts.md) | REST API v1 contracts under `/api/v1/videos` with JSON request/response schemas. |
| **Pillar 3: Operations** | [local-setup.md](file:///Users/quidb/Working/projects/bitmill/docs/operations/local-setup.md) | Local development setup, environment variables, and `make` automation targets. |
| **Pillar 3: Operations** | [runbook.md](file:///Users/quidb/Working/projects/bitmill/docs/operations/runbook.md) | Incident diagnosis, DLQ inspection, Cloudflare R2 orphan cleanup, and database queries. |

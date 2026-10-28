# 🎯 Bitmill Engineering Roadmap & Milestones

This document tracks the end-to-end implementation progress, deliverables, and verification evidence for the **Bitmill** video streaming platform.

---

## 📊 High-Level Progress Overview

| Milestone | Focus Area | Status | Progress |
| :--- | :--- | :---: | :--- |
| **Milestone 1** | Core Infrastructure & Relational Storage | **`COMPLETED`** | 100% |
| **Milestone 2** | Cloudflare R2 Storage Adapter & Cloud Connectivity | **`COMPLETED`** | 100% |
| **Milestone 3** | Resumable Multipart Ingestion & Signed URLs | **`COMPLETED`** | 100% |
| **Milestone 4** | Background Transcoding Worker & RabbitMQ Orchestration | **`COMPLETED`** | 100% |
| **Milestone 5** | Production Hardening, CDN Distribution & Observability | **`IN PROGRESS`** | Next Up |

---

## 🏁 Milestone Breakdown

### Milestone 1: Core Infrastructure & Tooling Setup
- **Objective**: Establish the relational database, message broker, automation tooling, and logging infrastructure.
- **Status**: **`COMPLETED`** ✅
- **Deliverables**:
  - [x] Docker Compose orchestration for PostgreSQL 15 (`bitmill-postgres`) and RabbitMQ 3 Management (`bitmill-rabbitmq`).
  - [x] Database migrations managed via **Goose** (`migrations/00001_init_schema.sql`) using native PostgreSQL 15 `gen_random_uuid()`.
  - [x] High-performance structured logging with **Uber `zap`** and **`lumberjack`** (100MB rotation, 5 backups, gzip compression).
  - [x] Root [Makefile](file:///Users/quidb/Working/projects/bitmill/Makefile) automating development, testing, migrations, and docker management.
- **Verification Evidence**:
  - `make docker-up` runs healthy containers on ports `5432` and `5672`/`15672`.
  - `make migrate-status` confirms all migrations applied.

---

### Milestone 2: Cloudflare R2 Storage Adapter
- **Objective**: Integrate S3-compatible Cloudflare R2 storage without vendor lock-in.
- **Status**: **`COMPLETED`** ✅
- **Deliverables**:
  - [x] Pure Go Domain Interface: `video.StorageProvider` decoupled from cloud SDKs (IoC).
  - [x] Infrastructure Adapter: `R2Adapter` implementing `StorageProvider` using AWS SDK for Go v2.
  - [x] Real credential integration via `.env`: `bitmill-raw-videos` and `bitmill-processed-videos`.
- **Verification Evidence**:
  - Verified live connectivity, object PUT, and HeadObject existence check against real Cloudflare R2 bucket.

---

### Milestone 3: Resumable Multipart Ingestion & Signed URLs
- **Objective**: Zero-server bandwidth video ingestion supporting large files (50MB to 5GB+), adaptive chunking, network failure recovery, and frontend dashboard.
- **Status**: **`COMPLETED`** ✅
- **Deliverables**:
  - [x] **RFC-001 Approved**: Multi-perspective 360° discovery (BA/UX, Solutions Architect, SecOps/QA).
  - [x] **Adaptive Chunking Engine**: 10MB chunks ($\le 250\text{MB}$) and 20MB chunks ($> 250\text{MB}$), cutting R2 Class A API costs by 75%.
  - [x] **Presigned Multipart URLs**: Time-limited (2h) SigV4 presigned PUT URLs for individual chunks.
  - [x] **Resumable Telemetry & Idempotency**: `GET /upload-status` queries missing parts; idempotent completion handles late-stage 99% network drops.
  - [x] **Type-Safe Persistence**: **`sqlc`** integrated with Goose schema, generating zero-boilerplate queries.
  - [x] **Interactive Web UI**: Modern dark-themed dashboard at `http://localhost:8080` featuring visual chunk matrix, pause/resume, and real-time EMA upload speed.
- **Verification Evidence**:
  - `make test`: All unit tests pass with race detector (`-race`).
  - `make test-e2e`: Live 12MB multipart upload (10MB + 2MB) directly to Cloudflare R2, verified in PostgreSQL and R2 storage with 100% success.

---

### Milestone 4: Background Transcoding Worker & RabbitMQ Orchestration
- **Objective**: Asynchronous, distributed video transcoding into Multi-Bitrate Adaptive Bitrate (ABR) HLS streams using isolated Docker FFmpeg.
- **Status**: **`COMPLETED`** ✅
- **Deliverables**:
  - [x] **RFC-002 Approved & Implemented**: 360° discovery (BA/UX, Solutions Architect, SecOps/QA) and technical specification.
  - [x] **RabbitMQ Event Publishing**: API emits persistent `VideoUploadedEvent` to topic exchange `bitmill.video.events` on `/complete-upload` with publisher confirms.
  - [x] **Worker Consumer Daemon**: Standalone background worker (`cmd/worker`) consuming from `video.transcoding.jobs` with strict `prefetch=1` and graceful shutdown (`SIGINT`/`SIGTERM`).
  - [x] **Docker FFmpeg Media Engine**: Isolated container execution (`mwader/static-ffmpeg:latest`) with `--network none` sandboxing, dynamic ABR resolution ladder (4K down to 360p with "Never Upscale" capping), closed GOPs (2s segments), and poster thumbnail extraction.
  - [x] **Parallel HLS Distribution Upload**: Worker streams `.m3u8` master and variant manifests, `.ts` segments, and `thumbnail.jpg` to Cloudflare R2 `bitmill-processed-videos` with optimal CDN caching headers.
  - [x] **Dead Letter Queue (DLQ) & Retry Topology**: Fail-Fast routing for poison pills and 30s TTL delay exchange retry loop (`bitmill.video.retry`) for transient failures (capped at 3 attempts before DLQ quarantine).
  - [x] **Database State Machine & Type-Safe Queries**: Atomic update `UPLOADED` $\rightarrow$ `PROCESSING` $\rightarrow$ `READY` (with rendition metadata in `video_renditions` and job tracking in `processing_jobs`).
- **Verification Evidence**:
  - `make test`: All unit tests pass with race detector (`-race`), covering dynamic ladder capping, idempotency, poison pill defense, and queue publishing.
  - `make verify-worker`: Live end-to-end integration test passes all 8 verification steps with real Cloudflare R2, RabbitMQ broker, and isolated Docker FFmpeg (`mwader/static-ffmpeg:latest`), generating multi-rendition HLS (720p, 480p, 360p) + thumbnail in 11.8s, verified in PostgreSQL and publicly streamable over R2 CDN (`HTTP 200 OK`).

---

### Milestone 5: Production Hardening, CDN & Observability
- **Objective**: Enterprise production readiness, CDN playback optimization, and operational monitoring.
- **Status**: **`PLANNED`** ⏳
- **Planned Deliverables**:
  - [ ] Cloudflare CDN public distribution with caching headers for `.m3u8` (no-cache) and `.ts` (immutable).
  - [ ] Prometheus metrics (`/metrics`) tracking ingestion rate, transcoding duration, and queue lag.
  - [ ] Automated Orphan Sweeper cron purging incomplete multipart sessions older than 3 days.
  - [ ] CI/CD automated pipeline (GitHub Actions).

---

## 📝 Milestone Maintenance Protocol
- Whenever a milestone starts, change status to **`IN PROGRESS`**.
- Whenever a milestone completes, verify executable evidence (tests, logs), check off all deliverables, and change status to **`COMPLETED`**.
- Commit changes with message `docs: update milestones tracker`.

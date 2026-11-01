# RFC 005: Google Cloud Platform (GCP) Storage & Serverless Transcoder Migration

**Document ID**: `RFC-005`  
**Feature Name**: `gcp-storage-and-transcoder-migration`  
**Status**: PROPOSED (Awaiting User Sign-Off)  
**Author**: Principal Solutions & Cloud Architect (Antigravity Pairing Lead)  
**Target Date**: 2026-11-01  
**Subsystem**: Object Storage (`GCS`), Serverless Transcoder (`Transcoder API`), Event Ingestion (`Pub/Sub`), Core Streaming Backend (`go-streaming`)  

---

## 1. Executive Summary & Motivation

### 1.1 The Context
Bitmill's video ingestion and streaming pipeline currently relies on:
1. **Cloudflare R2** for object storage (via S3-compatible API).
2. **Docker FFmpeg Workers** hosted directly on the VPS (`103.90.224.69`), coordinated through RabbitMQ.

While functional, self-hosting video transcoding on a single VPS creates severe CPU starvation (100% utilization during 1080p ABR encoding), degrades API responsiveness, saturates server bandwidth, and limits concurrent upload scalability.

### 1.2 The Goal
Migrate the entire storage and media transformation workload to **Google Cloud Platform (GCP)**, taking full advantage of the user's **₫7,791,151.00 VND (~$300 USD) Free Trial credit**:
1. **Google Cloud Storage (GCS)**: Replace Cloudflare R2 for raw ingest and processed HLS delivery with Resumable V4 Signed URLs and Uniform Bucket-Level Access.
2. **Google Cloud Transcoder API (Serverless)**: Replace the VPS Docker FFmpeg worker with high-throughput cloud encoding across a 4-rung ABR ladder (`1080p`, `720p`, `480p`, `360p`) and poster thumbnail extraction in ~20–40 seconds.
3. **Cloud Pub/Sub Webhook**: Real-time event-driven job completion ingestion, transitioning video state to `READY` with zero CPU overhead on the VPS.
4. **Clean Architecture & Inversion of Control (IoC)**: Retain 100% decoupling by implementing new adapters (`GCSStorageAdapter`, `GCPTranscoderAdapter`) against existing domain contracts (`video.StorageProvider`, `video.Transcoder`).

---

## 2. Architecture Specification

### 2.1 End-to-End Media Flow Diagram

```
[Content Creator Client]
       │
       │ 1. Request Signed Upload URL (POST /api/v1/videos/upload/resumable)
       ▼
┌───────────────────────────────┐
│  Bitmill API Server (Gin)     │
│  (Issues GCS V4 Signed URL)   │
└──────────────┬────────────────┘
               │ 2. Returns GCS Signed PUT / Resumable URL
               ▼
[Content Creator Client] ─────────── 3. Direct Streaming Upload (HTTP PUT) ───────────► ┌────────────────────────┐
                                                                                        │  bitmill-raw-videos    │
                                                                                        │  (GCS Raw Bucket)      │
                                                                                        └───────────┬────────────┘
                                                                                                    │
                                                                                                    │ 5. Reads Source
                                                                                                    ▼
┌───────────────────────────────┐                             4. Submit Transcode Job   ┌────────────────────────┐
│  Bitmill API Server           ├──────────────────────────────────────────────────────►│   GCP Transcoder API   │
│  (or Async Event Trigger)     │                                                       │   (Serverless Engine)  │
└──────────────▲────────────────┘                                                       └───────────┬────────────┘
               │                                                                                    │
               │                                                      6. Writes HLS Renditions      │
               │                                                                                    ▼
               │ 8. Webhook / Pub/Sub Push                                              ┌────────────────────────┐
               │    ("JOB_SUCCEEDED")                                                   │bitmill-processed-videos│
               │                                                                        │(Public HLS Stream)     │
               │                                                                        └───────────┬────────────┘
               │                                                                                    │
┌──────────────┴────────────────┐                                                                   │ 9. HLS Stream
│   Cloud Pub/Sub Topic         │◄──────────────── 7. Publish Completion Event ─────────────────────┘
│ (bitmill-transcoder-events)   │
└───────────────────────────────┘
```

---

## 3. Inversion of Control & Software Design

### 3.1 Domain Layer Integrity
The Go domain layer (`go-streaming/internal/domain/video/`) remains completely untouched by external GCP dependencies.

```go
// Existing Domain Contract in internal/domain/video/storage.go
type StorageProvider interface {
    GeneratePresignedPutURL(ctx context.Context, bucket, objectKey string, expiry time.Duration, contentType string) (string, error)
    CreateMultipartUpload(ctx context.Context, bucket, objectKey, contentType string) (uploadID string, err error)
    GeneratePresignedPartURL(ctx context.Context, bucket, objectKey, uploadID string, partNumber int, expiry time.Duration) (string, error)
    CompleteMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string, parts []CompletedPart) (string, error)
    AbortMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string) error
    ListUploadedParts(ctx context.Context, bucket, objectKey, uploadID string) ([]CompletedPart, error)
    CheckObjectExists(ctx context.Context, bucket, objectKey string) (bool, error)
    GetObjectSize(ctx context.Context, bucket, objectKey string) (int64, error)
    GetDownloadURL(ctx context.Context, bucket, objectKey string, expiry time.Duration) (string, error)
    DownloadToFile(ctx context.Context, bucket, objectKey, localPath string) error
    UploadFile(ctx context.Context, localPath, bucket, objectKey, contentType, cacheControl string) error
    UploadDirectory(ctx context.Context, localDir, bucket, prefix string) error
}
```

### 3.2 Infrastructure Adapters

#### 1. `GCSStorageAdapter` (`internal/infrastructure/storage/gcs_adapter.go`)
- Wraps `cloud.google.com/go/storage`.
- Satisfies `video.StorageProvider` 100%.
- Generates V4 signed URLs using Service Account credentials.
- Handles XML API S3-multipart compatibility or native GCS object operations.

#### 2. `GCPCloudTranscoderAdapter` (`internal/infrastructure/transcoder/gcp_transcoder.go`)
- Wraps `google.golang.org/api/transcoder/v1`.
- Compiles the 4-rung ABR ladder specification (`1080p`, `720p`, `480p`, `360p` + AAC audio + poster thumbnail).
- Submits asynchronous transcoding jobs with direct Pub/Sub destination targeting.

---

## 4. Google Cloud Infrastructure Design

### 4.1 Cloud Storage Buckets
Configured according to `@skill:google-cloud-storage-bucket-architect`:

1. **`bitmill-raw-videos`**:
   - Location: Selected region (e.g., `asia-southeast1` or `us-central1`).
   - Access: **Uniform Bucket-Level Access (UBLA)** enabled.
   - Public Access Prevention: **Enforced** (`publicAccessPrevention = "enforced"`).
   - Lifecycle: Delete objects older than 7 days (`age: 7`).
   - CORS: Allow `PUT`, `POST`, `OPTIONS` from authorized web origins.

2. **`bitmill-processed-videos`**:
   - Location: Same region as raw bucket (eliminates data egress costs).
   - Access: **Uniform Bucket-Level Access (UBLA)** enabled.
   - Public Access: Enabled for playback (`allUsers` granted `roles/storage.objectViewer`).
   - CORS: Allow `GET`, `HEAD`, `OPTIONS` from origin `*` with exposed headers `Content-Range`, `Content-Length`, `ETag`.

### 4.2 Cloud Transcoder Template Configuration
- **Input**: `gs://bitmill-raw-videos/{video_id}/raw.mp4`
- **Output**: `gs://bitmill-processed-videos/videos/{video_id}/hls/`
- **Elementary Video Streams**:
  - `1080p`: 1920x1080 @ 4,500 kbps, H.264 High profile, GOP 2s (60 frames).
  - `720p`: 1280x720 @ 2,500 kbps, H.264 Main profile, GOP 2s.
  - `480p`: 854x480 @ 1,000 kbps, H.264 Main profile, GOP 2s.
  - `360p`: 640x360 @ 600 kbps, H.264 Baseline profile, GOP 2s.
- **Audio Stream**: AAC-LC @ 128 kbps, 48kHz, Stereo.
- **Manifest**: Master playlist `master.m3u8` grouping all 4 rungs.
- **Thumbnail**: Poster snapshot extracted at second 00:00:02 (`thumbnail.jpg`).
- **Pub/Sub Notification**: `projects/{PROJECT_ID}/topics/bitmill-transcoder-events`.

---

## 5. Security & IAM Governance

### 5.1 Service Account Least Privilege
- **Service Account**: `bitmill-backend-sa@{PROJECT_ID}.iam.gserviceaccount.com`
- **Assigned Roles**:
  - `roles/storage.objectAdmin` on `bitmill-raw-videos` and `bitmill-processed-videos`.
  - `roles/transcoder.admin` on project level.
  - `roles/pubsub.editor` (or `roles/pubsub.subscriber` + `roles/pubsub.publisher`).

### 5.2 Transcoder Service Agent Roles
The GCP managed agent `service-{PROJECT_NUMBER}@gcp-sa-transcoder.iam.gserviceaccount.com` is granted:
- `roles/storage.objectViewer` on `bitmill-raw-videos`.
- `roles/storage.objectAdmin` on `bitmill-processed-videos`.
- `roles/pubsub.publisher` on `bitmill-transcoder-events`.

---

## 6. Architectural Trade-Off Matrix

| Evaluation Dimension | Option A: Multi-Provider Inversion of Control (Recommended) | Option B: Immediate Hard Deletion of R2/Worker |
| :--- | :--- | :--- |
| **Config Flexibility** | Switchable via `STORAGE_PROVIDER=gcs\|r2` and `TRANSCODER_TYPE=gcp\|docker` | Fixed to GCP only |
| **Testing & CI Safety** | Existing unit tests and R2 integration tests continue passing without regression | Breaks existing test suites until complete rewrite |
| **Deployment Risk** | **Zero-risk rollback**: If GCP quota or billing issues occur, flip back to R2 in 1 line | High risk: Rollback requires Git revert |
| **Architectural Purity**| Perfect demonstration of Clean Architecture and polymorphic IoC | Simplifies codebase slightly by removing S3 dependencies |

**Architectural Decision**: **Option A** is selected. The system will support pluggable storage and transcoding backends via Go interfaces, allowing seamless runtime selection via `config.yaml` or `.env`.

---

## 7. Phased Implementation Plan

```
Phase 1: Setup & Environment Validation (Current Phase)
  ├── Authenticate GCP via Service Account & gcloud
  ├── Verify Project ID, Region, and APIs (Storage, Transcoder, Pub/Sub)
  └── Sign-off RFC 005 (Quality Gate 1)

Phase 2: Git Worktree Creation & Infrastructure Provisioning
  ├── make worktree-create NAME=gcp-migration
  ├── Provision GCS Buckets (raw & processed) with CORS & UBLA
  ├── Create Cloud Pub/Sub Topic & Subscription
  └── Bind Transcoder Service Agent IAM roles

Phase 3: Code Implementation (Contract-First & IoC)
  ├── 1. Extend internal/config with GCP parameters
  ├── 2. Implement GCSStorageAdapter (video.StorageProvider)
  ├── 3. Implement GCPCloudTranscoderAdapter
  ├── 4. Implement Pub/Sub Webhook / Consumer Handler
  └── 5. Wire dependencies at Composition Root (cmd/server/main.go)

Phase 4: Evidence-Based Verification & Worktree Merge
  ├── Evidence 1: Unit tests with mocks (100% pass)
  ├── Evidence 2: Live end-to-end upload & serverless transcoding test
  ├── Evidence 3: Playback test in HLS Player
  └── Merge feat/gcp-migration into main
```

---

## 8. Quality Gate 1: Sign-Off Checklist

Before any implementation code is written on Git Worktree, the user must confirm:
- [ ] Approval of RFC 005 Architecture and Trade-Off Matrix.
- [ ] Google Cloud Project ID and Region confirmation.
- [ ] Service Account Key (`credentials.json`) created and placed securely.
- [ ] Permission to create isolated Git Worktree: `make worktree-create NAME=gcp-migration`.

# 📋 Requirements Specification: Video Upload & Transcoding Subsystem

This document provides the formal business analysis, domain invariants, INVEST User Stories, and Acceptance Criteria (BDD/Gherkin) for the **Bitmill Video Upload & Transcoding Subsystem**.

---

## 1. Executive Summary & Domain Invariants

### 1.1. Business Context & Objective
* **Platform**: Bitmill Media Streaming & VOD Platform.
* **Objective**: Provide automated, zero-server-ingest video upload with seamless event-driven transcoding into multi-bitrate Adaptive Bitrate (ABR) HLS streams distributed globally.
* **Target Audience**: Content Creators (Uploaders), Platform Viewers (HLS Video Players), System Operations.

### 1.2. Core Domain Invariants (Unbreakable Business Rules)
1. **Zero-Byte API Transit**: Upload Service instances MUST NEVER receive, buffer, or pipe raw video payload streams. Raw binaries must flow directly from Client to Cloudflare R2 via Presigned URLs.
2. **Session Expiry**: An upload session (`PENDING_UPLOAD`) is strictly valid for **15 minutes**. Unconfirmed uploads beyond this window are marked expired/abandoned.
3. **Event-Driven Ingestion**: The transcoding workflow is triggered exclusively by Cloudflare R2 Storage Event Notifications (or MinIO bucket notifications in local development), eliminating reliance on client-side completion calls.
4. **Multi-Bitrate Completeness**: A video transitions to `READY` state if and only if the `master.m3u8` playlist, poster thumbnail, and at least two target rendition variants (e.g., 720p and 1080p) are successfully generated and uploaded.
5. **DLQ Isolation**: Any video transcoding task failing after 3 consecutive attempts must be pushed to the Dead Letter Queue (DLQ) with contextual failure telemetry, without blocking remaining queue consumers.

---

## 2. Actor Profiles & Security Boundaries

| Actor | Role Definition | Authentication & Authorization |
| :--- | :--- | :--- |
| **Creator / Uploader** | End user uploading video files via web or mobile apps. | Bearer JWT; authorized to upload and query own video status. |
| **Storage Engine (R2)** | Cloudflare R2 bucket emitting lifecycle events on object creation. | Webhook HMAC signature or internal event pipeline. |
| **Transcoding Worker** | Background daemon executing Docker FFmpeg transcoding jobs. | Internal service account with read/write access to R2 & RabbitMQ. |
| **Viewer / Consumer** | End user playing transcoded HLS streams. | Public / CDN tokenized stream access. |

---

## 3. Epics & User Stories (INVEST Compliant)

```
[Epic 1: Direct Cloud Ingestion (EPIC-INGEST)]
   ├── US-01: Request Presigned Upload Ticket
   └── US-02: Direct HTTP PUT to Cloudflare R2
[Epic 2: Event-Driven Pipeline Trigger (EPIC-TRIGGER)]
   ├── US-03: Storage ObjectCreated Event Notification
   └── US-04: Enqueue Transcoding Job into RabbitMQ
[Epic 3: Multi-Bitrate Transcoding Engine (EPIC-TRANSCODE)]
   ├── US-05: Worker Job Consumption & Raw Download
   ├── US-06: FFmpeg Adaptive Bitrate Packaging (1080p, 720p, 480p, 360p)
   ├── US-07: Master Playlist & Segment Upload to Processed R2
   └── US-08: Dead Letter Queue (DLQ) Handling on Poison Pills
[Epic 4: Playback & Lifecycle Status (EPIC-DELIVERY)]
   └── US-09: Video Ready State Transition & CDN Playback Manifest
```

### Story Details & Acceptance Criteria

#### 📌 US-01: Request Presigned Upload Ticket
* **As a** Content Creator,
* **I want to** submit video metadata (title, expected file size, MIME type) to receive a secure, time-limited upload URL,
* **So that** I can upload large video files directly to Cloudflare R2 without burdening the API server.

**Acceptance Criteria (Gherkin):**
```gherkin
Scenario: Successful upload ticket generation
  Given an authenticated creator with valid credentials
  When the creator submits POST /api/v1/videos/upload-ticket with:
    | field           | value       |
    | title           | "My Video"  |
    | file_size_bytes | 150000000   |
    | mime_type       | "video/mp4" |
  Then the system responds with HTTP 201 Created
  And the response body contains:
    | field       | type    | description                        |
    | video_id    | UUIDv4  | Unique video record ID             |
    | upload_url  | String  | Cloudflare R2 Presigned PUT URL     |
    | expires_in  | Integer | 900 seconds (15 minutes)           |
  And a record in "videos" table is created with status "PENDING_UPLOAD"
```

---

#### 📌 US-03: Storage Event Notification Ingestion
* **As an** Upload Service / Event Dispatcher,
* **I want to** listen for `s3:ObjectCreated:Put` events emitted by Cloudflare R2 / MinIO,
* **So that** the system automatically recognizes completed uploads without trusting client-side reports.

**Acceptance Criteria (Gherkin):**
```gherkin
Scenario: Ingest R2 ObjectCreated event
  Given a video record in "PENDING_UPLOAD" with object key "raw/{video_id}.mp4"
  When Cloudflare R2 emits an ObjectCreated event matching "raw/{video_id}.mp4"
  Then the service validates the event payload signature
  And transitions the video status to "UPLOADED" in RMDS (PostgreSQL)
  And publishes a message to RabbitMQ exchange "video.transcoding.exchange" with routing key "video.transcode.job"
```

---

#### 📌 US-06: FFmpeg Multi-Bitrate ABR Transcoding
* **As a** Processing Worker,
* **I want to** process raw MP4 videos into multi-rendition HLS (1080p, 720p, 480p, 360p) with closed GOPs,
* **So that** viewers on different bandwidths experience smooth streaming with adaptive resolution switching.

**Acceptance Criteria (Gherkin):**
```gherkin
Scenario: Complete multi-bitrate ABR HLS transcoding
  Given a consumed job message for video_id "550e8400-e29b-41d4-a716-446655440000"
  When the worker executes Docker FFmpeg with ABR parameters:
    | rendition | resolution | video_bitrate | audio_bitrate | segment_duration |
    | 1080p     | 1920x1080  | 4500k         | 192k          | 4s               |
    | 720p      | 1280x720   | 2500k         | 128k          | 4s               |
    | 480p      | 854x480    | 1200k         | 96k           | 4s               |
    | 360p      | 640x360    | 600k          | 64k           | 4s               |
  Then variant playlists and segment chunks (*.ts) are produced
  And a master.m3u8 playlist referencing all 4 variants is generated
  And all artifacts are uploaded to the Processed R2 bucket
  And the video status transitions to "READY"
  And the message is acknowledged (ACK) to RabbitMQ
```

---

#### 📌 US-08: Poison Pill Isolation to Dead Letter Queue
* **As a** Reliability Engineer,
* **I want** jobs that fail repeatedly (e.g. corrupted video header) to be routed to a Dead Letter Queue,
* **So that** the worker is not blocked in infinite retry loops and the incident is logged for inspection.

**Acceptance Criteria (Gherkin):**
```gherkin
Scenario: Route corrupted video to DLQ after 3 failed attempts
  Given a video job that triggers FFmpeg fatal exit code
  When the worker fails attempt #3
  Then the message is rejected with requeue=false (NACK)
  And RabbitMQ dead-letter exchange routes the message to "video.transcode.dlq"
  And the video status in PostgreSQL is set to "FAILED"
  And an alert log is emitted with error trace
```

---

## 4. INVEST Quality Checklist

| Criteria | Assessment | Evidence |
| :--- | :---: | :--- |
| **Independent** | ✅ Pass | US-01 (Ticket), US-03 (Event Ingestion), and US-06 (Transcoding) are decoupled via async message boundaries. |
| **Negotiable** | ✅ Pass | Rendition ladders (1080p/720p/480p) and timeout thresholds are config-driven via `config.yaml`. |
| **Valuable** | ✅ Pass | Enables multi-gigabyte uploads without API latency spikes, with global ABR video streaming. |
| **Estimable** | ✅ Pass | Clear technical boundaries: S3 Presign Client, AMQP driver, and Docker FFmpeg execution. |
| **Small** | ✅ Pass | Each story is scoped to 1-3 dev days within modular packages. |
| **Testable** | ✅ Pass | Every story has executable BDD/Gherkin acceptance criteria verifiable by curl and automated unit tests. |

# 📑 Product Discovery & UX Specification: GCP Cloud Storage & Serverless Transcoder Migration

**Document ID**: `RFC-DISCOVERY-005`  
**Subsystem**: Cloud Storage (GCS), Serverless Transcoding (GCP Transcoder API), Asynchronous Event Ingestion (Cloud Pub/Sub), Adaptive Bitrate HLS Delivery  
**Status**: DRAFT (Review Ready)  
**Author**: Principal Business Analyst & Video Streaming UX Specialist  
**Target Environment**: Google Cloud Platform (GCP) Free Trial Tier (~$300 USD / ₫7,791,151 VND), Bitmill Streaming Backend  
**Target Platform**: Bitmill Media Platform (`go-streaming`, Clean Architecture, DDD, Inversion of Control)  

---

## 1. Executive Summary & Problem Space

### 1.1 The VPS Compute Bottleneck in Media Processing
The current Bitmill architecture relies on self-hosted Docker FFmpeg workers executing on a single VPS host (`103.90.224.69`). While this architecture demonstrated feasibility during initial milestones, it exhibits severe operational bottlenecks under production conditions:
1. **CPU & RAM Exhaustion**: Encoding a single 1080p 60fps video into a 4-rung Adaptive Bitrate (ABR) ladder consumes 100% of all available CPU cores on the VPS for minutes. In-flight API requests experience latency degradation, and RTMP ingestion buffers stall.
2. **Bandwidth Inefficiency (Double Ingress/Egress)**: In the self-hosted worker model, the VPS must first download the raw video from object storage (Cloudflare R2), transcode it locally on scratch disk, and then re-upload hundreds of HLS segments back to storage. This doubles network transfer time and saturates VPS network interfaces.
3. **Storage Latency & Scaling Cap**: Concurrent user uploads are serialized or throttled by VPS disk I/O and CPU limits. Scaling to support dozens of concurrent creators requires provisioning expensive high-CPU compute instances that sit idle between uploads.

```
┌────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                  CURRENT VPS WORKER MODEL VS. TARGET SERVERLESS GCP ARCHITECTURE                   │
├───────────────────────────────────────────────────┬────────────────────────────────────────────────┤
│ ❌ CURRENT STATE: SELF-HOSTED WORKER & R2          │ ✅ TARGET: SERVERLESS GCP PIPELINE             │
├───────────────────────────────────────────────────┼────────────────────────────────────────────────┤
│ • Client uploads to R2 or VPS disk                │ • Client uploads directly to GCS via Signed URL│
│ • Worker downloads raw video (VPS bandwidth cost) │ • GCP Transcoder reads directly from GCS bucket│
│ • Docker FFmpeg burns 100% VPS CPU for 3-5 mins   │ • Distributed serverless encoding in ~20-40s   │
│ • Worker uploads HLS segments back to R2          │ • HLS chunks written directly to target bucket │
│ • RabbitMQ & worker maintenance overhead          │ • Cloud Pub/Sub pushes completion event        │
│ • VPS CPU spike harms API & RTMP ingestion        │ • 0% CPU consumption on Bitmill VPS backend   │
└───────────────────────────────────────────────────┴────────────────────────────────────────────────┘
```

### 1.2 User Personas & Experience Requirements

#### Persona A: The Content Creator ("Minh Anh" - 4K/60fps Vlogger)
- **Goal**: Upload multi-gigabyte video files reliably over unstable cellular/home connections without timeouts.
- **Pain Points**: Connection resets at 85% causing complete restart; slow processing times where video remains stuck in "Processing" for 15+ minutes.
- **UX Requirements**:
  - Resumable upload directly to cloud storage (Google Cloud Storage V4 Signed URLs).
  - Byte-accurate progress indicators with smooth percentage estimation.
  - Near-instant transcode completion notification (under 60 seconds for a 5-minute video).

#### Persona B: The Viewer ("Hoàng Long" - Mobile & Desktop Consumer)
- **Goal**: Seamless, buffer-free playback across varying network conditions (4G mobile to high-speed fiber).
- **Pain Points**: Long initial load times (Time-To-First-Frame > 3s), playback stalls when network bandwidth fluctuates, lack of scrubber preview thumbnails.
- **UX Requirements**:
  - Fast-start ABR HLS streaming with optimized segment durations (2s to 4s).
  - 4 resolution rungs (`1080p`, `720p`, `480p`, `360p`) perfectly aligned on Group-of-Pictures (GOP) boundaries to prevent audio/video desync during quality switching.
  - High-fidelity poster thumbnail and storyboard sprite sheets for scrubbing.

---

## 2. Business Context & Invariants

### 2.1 State Machine Lifecycle
The video asset lifecycle must adhere to strict state transition invariants across upload, transcoding, and delivery:

```
                  ┌──────────────────────┐
                  │      REGISTERED      │
                  └──────────┬───────────┘
                             │ Initiate Direct Upload
                             ▼
                  ┌──────────────────────┐
                  │      UPLOADING       │ (Client streaming bytes to GCS)
                  └──────────┬───────────┘
                             │ Finalize Upload / GCS Event
                             ▼
                  ┌──────────────────────┐
                  │       UPLOADED       │ (Source object validated in raw bucket)
                  └──────────┬───────────┘
                             │ Submit GCP Transcoder Job
                             ▼
                  ┌──────────────────────┐
                  │      PROCESSING      │ (Serverless GCP encoding active)
                  └──────────┬───────────┘
                             │
               ┌─────────────┴─────────────┐
               │ Pub/Sub SUCCESS           │ Pub/Sub FAILED / Timeout
               ▼                           ▼
    ┌──────────────────────┐    ┌──────────────────────┐
    │        READY         │    │        FAILED        │
    └──────────────────────┘    └──────────────────────┘
```

### 2.2 Core Domain Invariants
1. **Invariant 1 (Zero Local Media IO)**: The Go API server and VPS host shall never buffer, transcode, or download the raw media file locally. All media bits flow directly between Client <-> GCS and GCS <-> Transcoder API.
2. **Invariant 2 (ABR Ladder Alignment)**: Every transcoded video must output exactly 4 video streams (`1080p`, `720p`, `480p`, `360p`) and 1 stereo audio stream (`128 kbps AAC`), muxed into HLS fMP4/TS segments with strict 2-second keyframe alignment (`GOP = 2s`).
3. **Invariant 3 (Idempotent Event Ingestion)**: Webhooks or Pub/Sub notifications indicating job completion must be processed idempotently. Duplicate delivery of a `JOB_COMPLETE` message must not corrupt existing rendition metadata or trigger redundant database transactions.
4. **Invariant 4 (Asset Isolation & Retention)**: Raw source videos in `bitmill-raw-videos` must be governed by an automated GCS Lifecycle Management rule (auto-deletion or transition to Coldline after 7 days) to conserve storage costs, while processed HLS assets in `bitmill-processed-videos` persist indefinitely.

---

## 3. FinOps & GCP Credit Optimization Analysis

The user possesses an active Google Cloud Free Trial credit of **₫7,791,151.00 VND (~$300.00 USD)**. The architecture must maximize cost efficiency and prevent unexpected runaway billing:

| Service | GCP Pricing Metric | Free Tier / Projected Cost | Optimization Strategy |
| :--- | :--- | :--- | :--- |
| **Cloud Storage (GCS)** | Standard Storage: \$0.020 / GB / month<br>Class A Ops: \$0.05 / 10,000<br>Class B Ops: \$0.004 / 10,000 | First 5 GB free per month in `us` regions.<br>Projected cost: < \$2.50 / month | 7-day TTL lifecycle rule on raw uploads; GCS CORS caching (`max_age_seconds: 3600`) to minimize OPTIONS preflight requests. |
| **Transcoder API** | HD Output: \$0.015 / minute<br>SD Output: \$0.010 / minute | First 240 minutes of output video free per month.<br>Projected cost: \$15.00 for ~1,000 minutes | Combine all 4 renditions in a single multi-output job rather than executing separate jobs per resolution. |
| **Cloud Pub/Sub** | \$40.00 per TiB of message ingestion/delivery | First 10 GiB free per month.<br>Projected cost: \$0.00 (transcoding events are ~1KB each) | Compact JSON payload schema; acknowledge messages immediately to prevent requeue loops. |
| **Cloud Egress** | Standard Internet Egress: \$0.12 / GB | GCS to same-region Transcoder: **\$0.00 (Free)**<br>Viewer playback: Covered by trial | Keep GCS raw bucket, processed bucket, and Transcoder API region in the same geographical region (e.g. `asia-southeast1` or `us-central1`). |

**Conclusion**: The \$300 USD credit provides over **12+ months** of development, staging, and medium-scale production workload without requiring out-of-pocket expenditure.

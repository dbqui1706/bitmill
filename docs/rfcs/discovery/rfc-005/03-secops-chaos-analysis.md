# 🛡️ SecOps, Security Hardening & Chaos Recovery Specification: GCP Migration

**Document ID**: `RFC-DISCOVERY-005`  
**Subsystem**: Google Cloud IAM, GCS Bucket Security, Signed URL V4 Validation, Pub/Sub Webhook Authentication, FMEA & Chaos Recovery  
**Status**: DRAFT (Review Ready)  
**Author**: Principal SecOps & QA Chaos Specialist  
**Target Environment**: Google Cloud Platform, Linux VPS (`103.90.224.69`)  
**Target Platform**: Bitmill Media Platform (`go-streaming`)  

---

## 1. Failure Modes & Effects Analysis (FMEA)

The migration introduces interactions with external cloud-managed APIs. The FMEA matrix quantifies the Risk Priority Number (RPN = Severity × Occurrence × Detection) across failure scenarios:

| Failure Mode | S | O | D | RPN | Root Cause | Mitigation & Guardrail |
| :--- | :---: | :---: | :---: | :---: | :--- | :--- |
| **FM-1: Service Account Key Compromise** | 9 | 2 | 4 | **72** | Developer commits `credentials.json` to public Git repository or leaks file. | Add `*credentials*.json` and `gcp-*.json` to root `.gitignore`. Enforce IAM Least Privilege (no `roles/owner` or `roles/editor`). |
| **FM-2: GCS Raw Bucket Public Write Exploit** | 9 | 1 | 3 | **27** | Misconfigured bucket ACL allows anonymous users to upload arbitrary payloads. | Enforce **Uniform Bucket-Level Access (UBLA)**. Reject `allUsers` write permissions. Only allow uploads via time-limited V4 Signed URLs. |
| **FM-3: Poison Pill / Corrupted Source Video** | 6 | 4 | 2 | **48** | User uploads 0-byte file, unparsable header, or corrupted H.264 bitstream. | GCP Transcoder validates container atom before encoding. Transcoder returns `FAILED` with descriptive diagnostic string. Webhook marks video `FAILED`. |
| **FM-4: Pub/Sub Webhook Forgery (Spoofing)** | 8 | 2 | 3 | **48** | Malicious actor discovers webhook endpoint and sends fake `READY` status payload. | Enforce Google Cloud OIDC token verification (`Authorization: Bearer <ID_TOKEN>`) using Google public cert keys. |
| **FM-5: Duplicate Pub/Sub Message Delivery** | 5 | 5 | 2 | **50** | At-least-once delivery of Pub/Sub causes two concurrent webhook calls for the same job. | Implement database idempotency check via PostgreSQL `SELECT FOR UPDATE` or conditional status update: `UPDATE videos SET status = 'READY' WHERE id = $1 AND status = 'PROCESSING'`. |
| **FM-6: Orphaned GCS Storage Leak** | 4 | 4 | 2 | **32** | Raw video files accumulate indefinitely after transcoding, draining credit. | Apply automated GCS Lifecycle Rule: `DeleteAfterAge(7 days)` on `bitmill-raw-videos`. |

---

## 2. GCP IAM Least Privilege Configuration

Following enterprise security standards, the Service Account used by Bitmill must adhere strictly to the principle of least privilege. **NEVER assign `Project Owner` or `Project Editor`**.

### 2.1 Dedicated Service Account
- **Service Account Name**: `bitmill-backend-sa`
- **Email**: `bitmill-backend-sa@{PROJECT_ID}.iam.gserviceaccount.com`

### 2.2 Minimal Required IAM Roles

```
┌──────────────────────────────────────────────────────────────────────────────────────────────────┐
│                               SERVICE ACCOUNT IAM PERMISSION MATRIX                              │
├────────────────────────────────┬───────────────────────────┬─────────────────────────────────────┤
│ Role Name                      │ GCP Resource Scope        │ Purpose                             │
├────────────────────────────────┼───────────────────────────┼─────────────────────────────────────┤
│ `roles/storage.objectAdmin`    │ `bitmill-raw-videos`      │ Issue Signed URLs, read raw objects,│
│                                │ `bitmill-processed-videos`│ delete temporary test files.        │
├────────────────────────────────┼───────────────────────────┼─────────────────────────────────────┤
│ `roles/transcoder.admin`       │ Project Level             │ Create and inspect serverless       │
│                                │                           │ transcoding jobs.                   │
├────────────────────────────────┼───────────────────────────┼─────────────────────────────────────┤
│ `roles/pubsub.publisher`       │ `bitmill-transcoder-events`│ Transcoder service agent needs this │
│                                │                           │ to publish job completion events.   │
├────────────────────────────────┼───────────────────────────┼─────────────────────────────────────┤
│ `roles/pubsub.subscriber`      │ `bitmill-transcoder-sub`  │ Bitmill backend pull subscription   │
│                                │                           │ (for dev/worker fallback mode).     │
└────────────────────────────────┴───────────────────────────┴─────────────────────────────────────┘
```

> [!IMPORTANT]
> The Cloud Transcoder Service Agent (`service-{PROJECT_NUMBER}@gcp-sa-transcoder.iam.gserviceaccount.com`) also requires:
> 1. `roles/storage.objectViewer` on `bitmill-raw-videos` (to read source videos).
> 2. `roles/storage.objectAdmin` on `bitmill-processed-videos` (to write transcoded HLS segments and manifests).
> 3. `roles/pubsub.publisher` on the target topic (to publish completion events).

---

## 3. Storage Security & Signed URL V4 Validation

Following `@skill:gcs-security-assessment`:
1. **Uniform Bucket-Level Access (UBLA)**: Enabled on both buckets to prevent ACL drift.
2. **Public Access Prevention (PAP)**:
   - `bitmill-raw-videos`: Enforce `publicAccessPrevention = "enforced"`. No object in this bucket may ever be made public.
   - `bitmill-processed-videos`: Allow `allUsers:objectViewer` so that HLS viewers can fetch segments without signed cookies (or restrict via Cloud CDN signed keys in later phases).
3. **Signed URL V4 Constraints**:
   - Expiration limit: Maximum 15 minutes (`expiry = 15 * time.Minute`).
   - Content-Type restriction: Signed URL specifies `video/mp4` or `video/*` to prevent MIME-type confusion attacks.
   - Hash / MD5 verification: Client provides `Content-MD5` header during PUT to verify data integrity against bit rot.

---

## 4. Webhook Security & OIDC Token Verification

When Google Cloud Pub/Sub invokes the Push Webhook endpoint:
1. Google attaches an HTTP header: `Authorization: Bearer <OIDC_TOKEN>`.
2. The Bitmill API verifies the OIDC token against Google's public JWKS endpoint (`https://www.googleapis.com/oauth2/v3/certs`):
   - Token signature is cryptographically valid.
   - `iss` equals `https://accounts.google.com`.
   - `aud` matches the registered Bitmill Webhook URL (`https://devlungtung.htcwood.com/api/v1/webhooks/gcp-transcoder` or configured Service Account).
   - Token is not expired.
3. If token validation fails, the API responds with `401 Unauthorized` and aborts request processing immediately.

---

## 5. Chaos Recovery & Resilience Verification Plan

| Test ID | Chaos Scenario | Expected Behavior & Recovery Path |
| :--- | :--- | :--- |
| **CHAOS-1** | Transcode 0-byte corrupt file | Transcoder fails within 5s with invalid media error. Pub/Sub event reports `FAILED`. Video status marked `FAILED`. No panic. |
| **CHAOS-2** | Transcode network partition | Transcoder retries internally. If permanent failure occurs, DLQ captures message. |
| **CHAOS-3** | Duplicate Pub/Sub message replay | First call updates video to `READY` and inserts renditions. Second call detects status is already `READY`, logs duplicate, and returns HTTP 200 without DB error. |
| **CHAOS-4** | Client drops upload mid-stream | Client disconnects at 45%. GCS lifecycle rule purges orphaned raw object after 7 days. Video record remains `UPLOADING` until cleanup job marks `EXPIRED`. |

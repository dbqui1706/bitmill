# Bitmill Operations & Troubleshooting Runbook

This runbook documents operational procedures, incident diagnosis, and remediation playbooks for the Bitmill media streaming platform.

---

## 1. Cloudflare R2 Storage Hygiene & Orphan Cleanup

### Problem Statement
When client uploads are abandoned mid-flight (e.g. user closes tab after 500MB of 2GB), unfinalized multipart parts remain staged on Cloudflare R2 indefinitely, incurring silent storage fees.

### Automated Mitigation: Bucket Lifecycle Policy
Ensure your Cloudflare R2 bucket has the `AbortIncompleteMultipartUpload` rule active:
- **DaysAfterInitiation**: `3` days (automatically purges all partial parts).

### Manual Emergency Purge via CLI
If storage leakage is suspected, list and abort multipart uploads manually:
```bash
# Using AWS CLI with R2 Endpoint
aws s3api list-multipart-uploads \
  --endpoint-url "https://${CLOUDFLARE_R2_ACCOUNT_ID}.r2.cloudflarestorage.com" \
  --bucket "bitmill-raw-videos"

# Abort a specific stalled upload
aws s3api abort-multipart-upload \
  --endpoint-url "https://${CLOUDFLARE_R2_ACCOUNT_ID}.r2.cloudflarestorage.com" \
  --bucket "bitmill-raw-videos" \
  --key "raw/<video_id>/source.mp4" \
  --upload-id "<upload_id>"
```

---

## 2. RabbitMQ & Dead Letter Queue (DLQ) Remediation

### Diagnosing Failed Transcoding Tasks
When a video fails processing 3 times (e.g. malformed codec or unresolvable dimensions), RabbitMQ automatically routes the message to `video.transcoding.dlq`.

```bash
# Check queue depths via RabbitMQ Management API
curl -s -u bitmill_mq:bitmill_mq_password http://localhost:15672/api/queues/%2F/video.transcoding.dlq | jq .messages
```

### Inspecting Poison Messages
1. Open RabbitMQ Web UI at `http://localhost:15672` (Username: `bitmill_mq`, Password: `bitmill_mq_password`).
2. Navigate to **Queues** $\rightarrow$ `video.transcoding.dlq`.
3. Click **Get Messages** (ackmode: `requeue_false` or `reject_requeue_false`) to inspect payload.
4. Check the corresponding `error_message` in PostgreSQL `processing_jobs` table:
   ```sql
   SELECT video_id, status, error_message, attempt_count FROM processing_jobs WHERE status = 'DLQ';
   ```

---

## 3. Database Incident Diagnosis

### Checking Active Connections & Locks
```bash
make db-psql
```
Run diagnostic query:
```sql
SELECT pid, usename, state, query, age(clock_timestamp(), query_start) 
FROM pg_stat_activity 
WHERE state != 'idle' 
ORDER BY query_start ASC;
```

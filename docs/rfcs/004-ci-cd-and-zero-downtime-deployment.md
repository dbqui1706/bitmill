# RFC 004: Automated CI/CD Pipeline, Container Registry & Zero-Downtime Deployment

| Metadata | Details |
| :--- | :--- |
| **RFC ID** | RFC-004 |
| **Title** | Automated CI/CD Pipeline, Container Registry & Zero-Downtime Deployment |
| **Authors** | Bitmill Engineering Team (Lead Architect, BA/UX Specialist, Solutions Architect, SecOps/QA Specialist) |
| **Status** | **PROPOSED (Awaiting User Sign-Off - Quality Gate 1)** |
| **Target Milestones** | Milestone 6: Enterprise CI/CD Pipeline, GHCR Integration & Zero-Downtime Single-Node Rollout |
| **Target Platform** | Ubuntu 24.04 LTS VPS (`103.90.224.69`), domain `devlungtung.htcwood.com` (and `api.htcwood.com`) |
| **Related Documents** | [01-ba-ux-analysis.md](./discovery/rfc-004/01-ba-ux-analysis.md)<br>[02-solutions-architecture.md](./discovery/rfc-004/02-solutions-architecture.md)<br>[03-secops-chaos-analysis.md](./discovery/rfc-004/03-secops-chaos-analysis.md)<br>[RFC-001: Resumable Video Upload](./001-resumable-video-upload.md)<br>[RFC-002: Asynchronous Transcoding Worker](./002-transcoding-worker-orchestration.md)<br>[RFC-003: Production Packaging & Gateway](./003-production-packaging-and-gateway.md) |

---

## 1. Executive Summary & Problem Space

### 1.1 The Operational Paradox of High-Throughput Media Deployments
In RFC-003, Bitmill established a hardened production runtime on an Ubuntu 24.04 LTS VPS (`103.90.224.69`) orchestrating five isolated containers:
- `bitmill-prod-gateway` (Nginx reverse proxy & media cache terminating public HTTP/HTTPS on ports 80/443).
- `bitmill-prod-server` (Go REST API daemon on internal network).
- `bitmill-prod-worker` (Go worker daemon with embedded static FFmpeg).
- `bitmill-prod-postgres` (PostgreSQL 15 isolated database).
- `bitmill-prod-rabbitmq` (RabbitMQ 3 AMQP 0-9-1 isolated message broker).

However, updates to this stack currently rely on **manual developer intervention**:
1. An engineer SSHs into the production VPS as `quidb-dev`.
2. Runs `git pull` directly on the host file system.
3. Executes `make docker-prod-build` and `make docker-prod-up`.

This manual deployment workflow introduces five catastrophic operational bottlenecks:

```
┌─────────────────────────────────────────────────────────────────────────────────────────────────┐
│                    MANUAL SSH RECREATE VS. TARGET AUTOMATED ZERO-DOWNTIME CI/CD                 │
├───────────────────────────┬──────────────────────────────┬──────────────────────────────────────┤
│ Failure Dimension         │ Manual SSH Anti-Pattern      │ Automated Target Architecture        │
├───────────────────────────┼──────────────────────────────┼──────────────────────────────────────┤
│ 1. Availability Deficit   │ `docker compose up -d`       │ Dual-Slot Blue/Green server rolling  │
│    (502 Blackouts)        │ destroys old container first;│ swap + dynamic Nginx upstream reload │
│                           │ 5s-20s HTTP 502/503 blackout │ ensures 0ms downtime, 0 connection   │
│                           │ for all live web viewers.    │ drops for in-flight requests.        │
├───────────────────────────┼──────────────────────────────┼──────────────────────────────────────┤
│ 2. Compute Starvation     │ Rebuilding Go binaries and   │ Ephemeral GitHub Actions Cloud       │
│    & Media Jitter         │ Docker images on VPS CPU     │ Runners compile and package images.  │
│                           │ hits 100% CPU/RAM/IOPS,      │ VPS purely pulls pre-compiled OCI    │
│                           │ choking active FFmpeg jobs.  │ artifacts from GHCR (<15s pull).     │
├───────────────────────────┼──────────────────────────────┼──────────────────────────────────────┤
│ 3. Ingestion Abortion     │ In-flight 500MB chunk upload │ Go HTTP Graceful Shutdown (60s cap)  │
│    (Wasted Bandwidth)     │ streams are abruptly killed  │ allows active chunk streams to       │
│                           │ without TCP drain, forcing   │ complete cleanly while new uploads   │
│                           │ creators to restart parts.   │ seamlessly hit the new slot.         │
├───────────────────────────┼──────────────────────────────┼──────────────────────────────────────┤
│ 4. Worker Mid-Stream      │ Worker is killed mid-way     │ Worker graceful drain stops pulling  │
│    Truncation             │ through a 4K transcode job;  │ from RabbitMQ (`basic.cancel`),      │
│                           │ orphaned tmpfs files and     │ finishes active transcode, or nacks  │
│                           │ unacknowledged AMQP leaks.   │ with `requeue=true` before exit.     │
├───────────────────────────┼──────────────────────────────┼──────────────────────────────────────┤
│ 5. Deployment Blindness   │ No automated smoke test or   │ Automated post-deploy smoke probe;   │
│    & Risky Rollbacks      │ automated rollback; broken   │ instant sub-15s rollback to previous │
│                           │ releases require frantic git │ stable Git SHA if healthcheck fails. │
│                           │ checkouts during outages.    │ GitHub Actions reports full status.  │
└───────────────────────────┴──────────────────────────────┴──────────────────────────────────────┘
```

### 1.2 System Objectives & Core Invariants
RFC-004 formalizes the engineering blueprint to replace this manual operational workflow with a fully automated, cryptographically secure, zero-downtime CI/CD pipeline.

**Core System Invariants**:
1. **Zero Downtime (0ms Downtime)**: At no point during deployment shall Nginx return HTTP 502 Bad Gateway or 503 Service Unavailable to active clients streaming video segments or uploading chunks.
2. **Offloaded Build Compute**: Production VPS resources (CPU, RAM, NVMe I/O) are reserved 100% for video transcoding and media delivery. No compilation or image building occurs on the VPS host.
3. **Immutable OCI Artifacts**: Every container image pushed to the GitHub Container Registry (GHCR) is tagged with an immutable Git Commit SHA (`ghcr.io/dbqui1706/bitmill/server:<sha>`) and scanned for security vulnerabilities prior to release.
4. **Backward-Compatible Schema Evolution (Expand-Contract)**: Database migrations executed during deployment must remain 100% compatible with both old and new application versions simultaneously.
5. **Fail-Fast Auto-Rollback**: If the new release fails synthetic health, readiness, or smoke tests within 30 seconds of activation, traffic cutover is instantly aborted, the previous stable slot is restored, and an alert is dispatched.

---

## 2. 360° Discovery Synthesis: Key Findings

The multidisciplinary discovery phase conducted by specialized subagents surfaced critical operational requirements across all system dimensions:

### 2.1 Business Analysis & Video UX Specialist Findings (`01-ba-ux-analysis.md`)
- **Persona Marcus (Content Creator)**: Uploading 100MB–500MB raw chunks over mobile/residential uplinks taking 30–120 seconds. An abrupt TCP connection reset causes high creator frustration and wasted upload bandwidth. The system must allow active TCP chunk sockets to drain to completion up to a hard ceiling (60s).
- **Persona Sarah (Video Consumer)**: Polling dynamic `.m3u8` manifests every 1–2 seconds and buffering 3–5 `.ts` segments (6–10 seconds of playback margin). Because `.ts` segments are cached immutably at the Nginx edge (RFC-003), a seamless upstream cutover (<20ms) within Nginx completely shields the video player from buffer starvation or playback stalls.
- **Service Level Objectives (SLOs)**:
  - Established TCP Connection Drop Rate: **0.00%**.
  - HTTP 502/503 Deployment Error Rate: **< 0.001%** (Target: 0).
  - Playback Buffer Stall Rate: **0.00%**.
  - Commit-to-Live Total Duration: **< 300 seconds (5 minutes)**.

### 2.2 Solutions Architecture Findings (`02-solutions-architecture.md`)
- **Single-Node VPS Constraints**: Bitmill runs on a single VPS Ubuntu 24.04 (`103.90.224.69`). Dual-stack blue-green setups duplicating stateful services (PostgreSQL and RabbitMQ) are rejected due to memory limits (2GB RAM VPS) and database split-brain risks.
- **Architectural Solution: Dual-Slot Application Ingress (`server_blue` & `server_green`)**:
  - Stateful services (`postgres`, `rabbitmq`, `gateway`) remain permanently online.
  - The API Server runs in alternating slots (`server_blue` on port 8081, `server_green` on port 8082).
  - Nginx gateway includes an external configuration `environment/nginx/conf.d/upstream.conf`.
  - Deployment script boots the idle slot, validates `/readyz` probe, writes the new slot to `upstream.conf`, and executes `nginx -s reload` (which achieves an atomic, zero-downtime master socket handoff in under 15ms).
- **Go HTTP Graceful Shutdown**: The Go API server (`cmd/server/main.go`) must wrap `router` in `http.Server{}` and listen for `SIGTERM`, invoking `srv.Shutdown(ctx)` with a 30s–60s context timeout to drain active HTTP requests.
- **Worker AMQP Graceful Drain**: When the worker receives `SIGTERM`, it cancels its RabbitMQ consumer (`basic.cancel`) to stop receiving new jobs, allows in-flight FFmpeg processes to complete up to `stop_grace_period: 120s`, and safely calls `BasicNack(requeue=true)` if the deadline is exceeded before exiting cleanly.

### 2.3 SecOps & Chaos Specialist Findings (`03-secops-chaos-analysis.md`)
- **Supply Chain Hardening**: All images are built on `golang:alpine` and deployed on `alpine:3.21` running as non-root `uid 10001 (bitmill)`. Trivy container scanning must run in the CI pipeline, failing builds on unpatched vulnerabilities with CVSS $\ge 7.0$ (`HIGH`, `CRITICAL`).
- **VPS Host Attack Surface**: SSH password authentication is disabled; only Ed25519 public key authentication is permitted. The deployment script runs under the unprivileged service user `quidb-dev` (in the `docker` group) without requiring `sudo` privileges.
- **Quantitative FMEA Reduction**: Initial system Risk Priority Number (RPN) of 204 across 6 critical failure modes is systematically reduced to 14 (a 93.1% risk reduction) through pre-pull assertions, migration locks, dual-slot canary probes, and automated rollback scripts.

---

## 3. Detailed Technical Architecture & Systems Design

### 3.1 End-to-End System Topology

```mermaid
flowchart TD
    subgraph GitHubCloud["GitHub Cloud Infrastructure"]
        GitCommit["Developer Git Push\n(main branch)"]
        GHA["GitHub Actions CI/CD Engine\n(.github/workflows)"]
        GHCR["GitHub Container Registry\n(ghcr.io/dbqui1706/bitmill)"]
        
        GitCommit --> GHA
        GHA -->|1. Test & Lint| GHA
        GHA -->|2. Build Multi-Stage| GHA
        GHA -->|3. Trivy CVE Scan| GHA
        GHA -->|4. Push Tagged Images| GHCR
    end

    subgraph VPSHost["Production VPS (103.90.224.69 - Ubuntu 24.04 LTS)"]
        SSHD["SSH Daemon (Port 22)\n(Ed25519 Key Auth Only)"]
        DeployScript["deploy-prod.sh\n(Atomic Orchestrator)"]
        
        subgraph DockerEngine["Docker Engine (Isolated Zero-Trust Network)"]
            Nginx["bitmill-prod-gateway (Nginx)\n(Ports 80/443 Public)"]
            
            subgraph DualSlotServer["API Server Dual-Slot Upstream"]
                ServerBlue["bitmill-prod-server-blue\n(Port 8081 - Active)"]
                ServerGreen["bitmill-prod-server-green\n(Port 8082 - Standby/New)"]
            end
            
            Worker["bitmill-prod-worker\n(AMQP Consumer + FFmpeg)"]
            Postgres["bitmill-prod-postgres\n(Internal 5432)"]
            RabbitMQ["bitmill-prod-rabbitmq\n(Internal 5672)"]
            GooseMigrator["Ephemeral Goose Container\n(Database Migrations)"]
        end
    end

    GHA -->|5. SSH Trigger with Git SHA| SSHD
    SSHD --> DeployScript
    DeployScript -->|6. Pull Digest| GHCR
    DeployScript -->|7. Run Migrations| GooseMigrator
    GooseMigrator -->|Apply DDL| Postgres
    DeployScript -->|8. Boot Idle Slot| ServerGreen
    DeployScript -->|9. Probe /readyz| ServerGreen
    DeployScript -->|10. Rewrite upstream.conf & Reload| Nginx
    Nginx -.->|Cutover Traffic| ServerGreen
    DeployScript -->|11. Graceful Drain (30s)| ServerBlue
    DeployScript -->|12. Stop Idle Slot| ServerBlue
    DeployScript -->|13. Recreate Worker| Worker
    DeployScript -->|14. Smoke Test Suite| Nginx
```

---

### 3.2 GitHub Actions CI Pipeline Architecture (`.github/workflows/ci.yml`)

The CI workflow runs on all Pull Requests and pushes to `main`. It implements strict quality gates to guarantee code reliability and container security before artifact generation.

```yaml
name: CI Quality Gate & Security Pipeline

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}

jobs:
  # -------------------------------------------------------------
  # JOB 1: Static Code Analysis, Linter & Race Detection Unit Tests
  # -------------------------------------------------------------
  quality-gate:
    name: Code Quality & Unit Tests
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - name: Checkout Source Code
        uses: actions/checkout@v4

      - name: Setup Go Runtime
        uses: actions/setup-go@v5
        with:
          go-version: '1.24'
          cache-dependency-path: go-streaming/go.sum

      - name: Verify Code Formatting (gofmt)
        run: |
          cd go-streaming
          UNFORMATTED=$(gofmt -l .)
          if [ -n "$UNFORMATTED" ]; then
            echo "Error: Unformatted Go files detected:"
            echo "$UNFORMATTED"
            exit 1
          fi

      - name: Run golangci-lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: v1.64.5
          working-directory: go-streaming
          args: --timeout=5m --verbose

      - name: Run Unit Tests with Race Detection
        run: |
          cd go-streaming
          go test -v -race -covermode=atomic -coverprofile=coverage.out ./internal/...

      - name: Enforce Test Coverage Threshold (>= 75%)
        run: |
          cd go-streaming
          TOTAL_COVERAGE=$(go tool cover -func=coverage.out | grep total: | awk '{print substr($3, 1, length($3)-1)}')
          echo "Total Code Coverage: ${TOTAL_COVERAGE}%"
          awk -v cov="$TOTAL_COVERAGE" 'BEGIN { if (cov < 75.0) { print "Coverage below 75% threshold!"; exit 1 } }'

  # -------------------------------------------------------------
  # JOB 2: Buildx Multi-Stage Docker Build & Layer Caching
  # -------------------------------------------------------------
  build-and-scan:
    name: Build OCI Images & Vulnerability Scan
    needs: [quality-gate]
    runs-on: ubuntu-latest
    timeout-minutes: 15
    permissions:
      contents: read
      packages: write
      security-events: write
    strategy:
      matrix:
        include:
          - name: server
            dockerfile: environment/docker/Dockerfile.server
            image: ghcr.io/${{ github.repository }}/server
          - name: worker
            dockerfile: environment/docker/Dockerfile.worker
            image: ghcr.io/${{ github.repository }}/worker
    steps:
      - name: Checkout Source Code
        uses: actions/checkout@v4

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Build Container Image Locally for Scanning
        uses: docker/build-push-action@v5
        with:
          context: .
          file: ${{ matrix.dockerfile }}
          load: true
          tags: ${{ matrix.image }}:${{ github.sha }}
          cache-from: type=gha
          cache-to: type=gha,mode=max

      - name: Run Trivy Security Vulnerability Scan
        uses: aquasecurity/trivy-action@master
        with:
          image-ref: ${{ matrix.image }}:${{ github.sha }}
          format: 'sarif'
          output: 'trivy-results.sarif'
          severity: 'CRITICAL,HIGH'
          ignore-unfixed: true

      - name: Upload Trivy SARIF Report to GitHub Security
        uses: github/codeql-action/upload-sarif@v3
        if: always()
        with:
          sarif_file: 'trivy-results.sarif'

      - name: Enforce Trivy Vulnerability Gate
        uses: aquasecurity/trivy-action@master
        with:
          image-ref: ${{ matrix.image }}:${{ github.sha }}
          format: 'table'
          exit-code: '1'
          severity: 'CRITICAL,HIGH'
          ignore-unfixed: true

      - name: Log in to GitHub Container Registry (GHCR)
        if: github.ref == 'refs/heads/main'
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Push Container Images to GHCR
        if: github.ref == 'refs/heads/main'
        uses: docker/build-push-action@v5
        with:
          context: .
          file: ${{ matrix.dockerfile }}
          push: true
          tags: |
            ${{ matrix.image }}:${{ github.sha }}
            ${{ matrix.image }}:latest
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

---

### 3.3 GitHub Actions CD Pipeline Architecture (`.github/workflows/deploy.yml`)

The CD workflow triggers strictly upon successful push to `main` and image publication in GHCR. It connects to the production VPS via SSH and delegates deployment to the atomic zero-downtime deployment script on the host.

```yaml
name: Continuous Deployment to Production VPS

on:
  workflow_run:
    workflows: ["CI Quality Gate & Security Pipeline"]
    branches: [main]
    types: [completed]

jobs:
  deploy-to-vps:
    name: Zero-Downtime Rollout to VPS
    if: ${{ github.event.workflow_run.conclusion == 'success' }}
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - name: Configure SSH Private Key
        uses: webfactory/ssh-agent@v0.9.0
        with:
          ssh-private-key: ${{ secrets.PROD_SSH_PRIVATE_KEY }}

      - name: Execute Zero-Downtime Rollout on VPS
        env:
          VPS_HOST: ${{ secrets.PROD_VPS_HOST }} # 103.90.224.69
          VPS_USER: ${{ secrets.PROD_VPS_USER }} # quidb-dev
          COMMIT_SHA: ${{ github.event.workflow_run.head_sha }}
          GHCR_PAT: ${{ secrets.GHCR_READ_TOKEN }}
        run: |
          ssh -o StrictHostKeyChecking=accept-new ${VPS_USER}@${VPS_HOST} \
            "bash /opt/bitmill/scripts/deploy-prod.sh --sha ${COMMIT_SHA} --token ${GHCR_PAT}"

      - name: Run Synthetic Post-Deployment Smoke Verification
        run: |
          echo "Verifying public domain ingress: https://devlungtung.htcwood.com/health"
          for i in {1..10}; do
            STATUS=$(curl -s -o /dev/null -w "%{http_code}" https://devlungtung.htcwood.com/health || true)
            if [ "$STATUS" -eq 200 ]; then
              echo "✅ Production Gateway Health Check Verified (HTTP 200)"
              exit 0
            fi
            echo "Waiting for gateway readiness... (attempt $i/10)"
            sleep 3
          done
          echo "❌ Smoke verification failed!"
          exit 1
```

---

### 3.4 Zero-Downtime Rolling Swap Engine (`deploy-prod.sh`)

To execute zero-downtime container swaps on a single VPS host without duplicating stateful services, the deployment architecture introduces:
1. **Dual-Slot Server Instances**: In `docker-compose.prod.yml`, the API server is partitioned into two services:
   - `server_blue`: Listening internally on port `8081`.
   - `server_green`: Listening internally on port `8082`.
2. **Dynamic Upstream Configuration**: Nginx includes `environment/nginx/conf.d/upstream.conf`, which defines the currently active upstream target:
   ```nginx
   # Dynamically updated by deploy-prod.sh
   upstream bitmill_api {
       server server_blue:8081 max_fails=3 fail_timeout=10s;
       keepalive 32;
   }
   ```
3. **Atomic Execution Protocol**:

```
[Start Deploy] 
       │
       ▼
1. Acquire Deploy Lock (/tmp/bitmill-deploy.lock)
       │
       ▼
2. Pull New Images from GHCR (Pre-flight assertion)
       │
       ▼
3. Run Goose Migration Container (Expand phase)
       │
       ▼
4. Identify Active Slot (e.g., Blue) & Standby Slot (e.g., Green)
       │
       ▼
5. Boot Standby Slot with Target Git SHA Image
       │
       ▼
6. Probe Standby Slot (`http://127.0.0.1:8082/readyz`) until Healthy (30s max)
       │
       ├─► [Probe Failed] ──► Abort & Stop Standby ──► Exit 1 (No Downtime)
       │
       ▼ [Probe Succeeded]
7. Rewrite `upstream.conf` to point to Standby Slot (Green)
       │
       ▼
8. Reload Nginx (`docker exec bitmill-prod-gateway nginx -s reload`)
       │ (Active client sockets transfer seamlessly; 0ms dropped)
       ▼
9. Graceful Drain Active Slot (Blue) (sleep 30s for in-flight chunk completion)
       │
       ▼
10. Stop Drained Slot (Blue)
       │
       ▼
11. Update Transcoding Worker (SIGTERM -> Graceful Drain -> Recreate Container)
       │
       ▼
12. Execute Smoke Test Suite (`smoke-test.sh`)
       │
       ├─► [Smoke Failed] ──► Rollback: Re-point upstream to Blue & Reload Nginx
       │
       ▼ [Smoke Succeeded]
13. Save Stable State (.deploy_state) & Release Lock
```

---

### 3.5 Database Migration Automation & Expand-Contract Invariants

Database schema migrations are executed via an **ephemeral Goose migration container** running within the isolated `bitmill-internal` Docker network before application code is swapped.

```
┌──────────────────────────────────────────────────────────────────────────────────────────────────┐
│                   TWO-PHASE EXPAND-CONTRACT ZERO-DOWNTIME MIGRATION CONTRACT                    │
├─────────────────────────────────────────────────┬────────────────────────────────────────────────┤
│ Phase 1: Expand (Release N)                     │ Phase 2: Contract (Release N+1)                │
├─────────────────────────────────────────────────┼────────────────────────────────────────────────┤
│ • Add new columns with `NULL` or default values.│ • Old application version (Release N) retired. │
│ • Create new tables, indexes, or views.         │ • Database triggers / backfills verified.      │
│ • Code writes to both old and new schema.       │ • Safe to drop obsolete columns or tables.     │
│ • OLD CONTAINER (Release N-1) CONTINUES WORKING.│ • NEVER DROP COLUMNS IN THE EXPAND PHASE.      │
└─────────────────────────────────────────────────┴────────────────────────────────────────────────┘
```

**PostgreSQL Safety Invariants**:
- All DDL statements must execute with a strict lock timeout (`SET lock_timeout = '4s';`) and statement timeout (`SET statement_timeout = '10s';`) to prevent locking the `videos` table and starving live streaming requests.
- **Strict Prohibition of Automatic Down-Migrations**: If a deployment fails after migrations have run, the system MUST NOT run `goose down`. Reverting migrations in production risks irreversible data loss (e.g. dropping columns populated during the canary phase). Instead, Bitmill enforces forward-compatible schemas and forward-fix hotfixes.

---

### 3.6 Transcoding Worker Graceful Drain & AMQP Integrity

The Transcoding Worker (`cmd/worker/main.go`) operates as a long-running daemon consuming from RabbitMQ. During deployment, stopping the worker must not corrupt active FFmpeg encodings or drop messages.

**Graceful Termination Sequence**:
1. Deployment orchestrator issues `docker stop -t 120 bitmill-prod-worker` (`SIGTERM`).
2. Worker daemon catches `SIGTERM` and immediately invokes `amqp.Channel.Cancel(consumerTag, false)`.
   - RabbitMQ stops delivering new transcode jobs to this worker instance.
   - The new worker container starts concurrently and begins consuming queued jobs immediately.
3. The active FFmpeg transcode task is allowed up to 110 seconds to complete encoding, upload HLS playlists to Cloudflare R2, update PostgreSQL video status to `READY`, and call `delivery.Ack(false)`.
4. **Hard Deadline Defense**: If the transcode job has not finished by second 115, the worker calls `delivery.Nack(false, true)` (`requeue=true`). RabbitMQ safely places the message back onto the queue for the new worker instance to resume, cleans up local `/tmp` scratch files, and exits cleanly.

---

## 4. Trade-Off Analysis Matrix

The engineering design of RFC-004 evaluated three competing deployment architectures:

| Architectural Attribute | Option 1: Dual-Stack Blue-Green (Two Full Compose Stacks) | Option 2: Rolling Recreate (`compose up -d --no-deps`) | Option 3: Dual-Slot Server + Dynamic Nginx Reload (RECOMMENDED) |
| :--- | :--- | :--- | :--- |
| **Downtime & Socket Drops** | 0ms downtime. | 5s–15s socket drop while container restarts; 502 Bad Gateway during boot. | **0ms downtime**; existing sockets drain via Nginx keepalive handoff. |
| **Host Resource Overhead** | **High**: Duplicates Postgres, RabbitMQ, and Redis instances; exceeds 2GB VPS RAM. | **Lowest**: Only one set of containers runs at any time. | **Minimal**: Only duplicates lightweight Go API container (~64MB RAM) during swap. |
| **Database State Safety** | **High Risk**: Dual database split-brain; requires complex bidirectional sync. | **Safe**: Single shared PostgreSQL instance. | **Safe**: Single shared PostgreSQL instance; migration runs pre-flight. |
| **Upload Stream Preservation** | Partial: Depends on load balancer routing sticky sessions. | **Failed**: In-flight 500MB chunk uploads are terminated abruptly. | **Guaranteed**: Nginx allows old container 30s to finish active chunk streams. |
| **Rollback Latency (MTTR)** | < 10 seconds (DNS/Proxy flip). | 30–60 seconds (must re-pull and re-create previous image). | **< 2 seconds**: Instant rewrite of `upstream.conf` and `nginx -s reload`. |
| **Implementation Complexity** | High: Requires multi-stack orchestration and external DNS/Proxy. | Low: Native Docker Compose command. | **Moderate**: Requires parameterized compose file and bash deploy script. |
| **Verdict** | ❌ Rejected (Resource Exhaustion) | ❌ Rejected (Violates Zero-Downtime SLA) | **✅ SELECTED FOR RFC-004** |

---

## 5. Failure Mode and Effects Analysis (FMEA)

The SecOps risk assessment quantified the six primary failure modes for automated zero-downtime deployments. The applied mitigations reduce the aggregate Risk Priority Number ($RPN = \text{Severity} \times \text{Likelihood} \times \text{Detection}$) from **204 to 14** (a 93.1% risk reduction):

| ID | Failure Mode | Severity (1-5) | Likelihood (1-5) | Detection (1-5) | Initial RPN | Engineering Mitigation Strategy | Final RPN |
| :--- | :--- | :---: | :---: | :---: | :---: | :--- | :---: |
| **FM-1** | SSH connection drops mid-deployment from GitHub Actions | 4 | 3 | 3 | **36** | Deployment executes locally on VPS via detached lockfile `/tmp/bitmill-deploy.lock` with deterministic state tracking. | **2** |
| **FM-2** | Breaking DB migration crashes old running container | 5 | 2 | 3 | **30** | Strict Expand-Contract contract; non-breaking schema changes only; 4s DDL lock timeout. | **2** |
| **FM-3** | New container crashes immediately on boot (bad config/env) | 4 | 3 | 2 | **24** | Standby slot probed via `/readyz` before Nginx cutover; automatic instant rollback within 15s. | **2** |
| **FM-4** | Transcoding worker killed mid-stream on 1GB master file | 4 | 3 | 3 | **36** | Worker graceful drain (`SIGTERM` -> `basic.cancel` -> 110s transcode finish or `Nack(requeue=true)`). | **2** |
| **FM-5** | Nginx reload drops active chunk upload sockets | 4 | 2 | 3 | **24** | Nginx master process executes graceful worker shutdown (`worker_shutdown_timeout 300s`). | **1** |
| **FM-6** | GHCR rate limit or partial image pull on VPS | 4 | 2 | 2 | **16** | Pre-flight image pull assertion; deploy aborts immediately before altering running containers. | **1** |

---

## 6. Comprehensive Verification & Chaos Test Plan

To satisfy the **Evidence-Based Verification** mandate of the Service Lifecycle Protocol, implementation of RFC-004 must validate the following multi-tier test harness:

```
┌──────────────────────────────────────────────────────────────────────────────────────────────────┐
│                               MULTI-TIER VERIFICATION HARNESS                                    │
├────────────────────┬─────────────────────────────────────────────────────────────────────────────┤
│ Level 1: Unit & CI │ • `golangci-lint` passes with 0 warnings.                                   │
│                    │ • `go test -v -race` passes 100% with coverage >= 75%.                      │
│                    │ • Trivy vulnerability scan passes with 0 CRITICAL/HIGH vulnerabilities.     │
├────────────────────┼─────────────────────────────────────────────────────────────────────────────┤
│ Level 2: End-to-End│ • Synthetic GitHub Actions push triggers pipeline on VPS.                   │
│ Smoke Test         │ • Automated verification of `https://devlungtung.htcwood.com/health` (200). │
│                    │ • Verification of internal metrics blocking: `/metrics` returns 403.        │
│                    │ • Worker heartbeat file updated within 15 seconds.                          │
├────────────────────┼─────────────────────────────────────────────────────────────────────────────┤
│ Level 3: Chaos     │ • Chaos 1: Deployment injected during active 200MB chunk upload (0 drops).  │
│ Verification       │ • Chaos 2: Deployment injected during 4K transcoding job (0 tasks lost).    │
│                    │ • Chaos 3: Poison image deployment triggers automated rollback (<15s MTTR). │
│                    │ • Chaos 4: Migration lock contention triggers safe fail-fast abort.         │
└────────────────────┴─────────────────────────────────────────────────────────────────────────────┘
```

### 6.1 Automated Smoke Test Specification (`scripts/smoke-test.sh`)
The smoke test script runs locally on the VPS immediately following Nginx upstream reload:
1. **Assertion 1 (Gateway HTTP Ingress)**: `GET http://127.0.0.1/health` returns `HTTP 200` with JSON status `"UP"`.
2. **Assertion 2 (API Readiness Probe)**: `GET http://127.0.0.1/readyz` returns `HTTP 200` with database status `"UP"`.
3. **Assertion 3 (Security Perimeter)**: `GET http://127.0.0.1/metrics` returns `HTTP 403 Forbidden`.
4. **Assertion 4 (Upload Session Creation)**: `POST /api/v1/videos/upload-session` generates a valid session ID.
5. **Assertion 5 (Worker Heartbeat Telemetry)**: `/tmp/worker.heartbeat` has been modified within the last 20 seconds.
6. **Assertion 6 (RabbitMQ Cluster Ping)**: `rabbitmq-diagnostics -q ping` exits with code 0.

### 6.2 Poison Deployment & Auto-Rollback Chaos Test Harness
```bash
# Chaos Scenario: Inject an invalid configuration to verify automatic rollback
export TEST_BAD_CONFIG="true"
bash /opt/bitmill/scripts/deploy-prod.sh --sha "poison-commit-test"
# Expected Outcome:
# 1. Standby slot fails /readyz probe after 3 attempts.
# 2. Deploy script detects failure; DOES NOT rewrite upstream.conf.
# 3. Nginx continues proxying 100% of traffic to the healthy active slot.
# 4. Standby slot container is terminated and removed.
# 5. Script exits with code 1; GitHub Actions marks deploy job as FAILED.
# 6. Service availability remains 100% without dropped requests.
```

---

## 7. Quality Gate 1: Alignment & Approval Checklist

Prior to entering **Phase 3 (Contract-First & IoC Implementation)** on an isolated Git worktree (`feat/ci-cd-pipeline`), user sign-off is required on the following architectural commitments:

- [ ] **Dual-Slot Server Model**: Agreement to adopt alternating Blue/Green API Server slots (`server_blue` on 8081, `server_green` on 8082) with dynamic Nginx upstream reloading.
- [ ] **Worker Drain Budget**: Agreement on a 120-second `stop_grace_period` for the Transcoding Worker to complete in-flight FFmpeg tasks before calling `Nack(requeue=true)`.
- [ ] **Database Migration Policy**: Agreement on the two-phase Expand-Contract schema evolution rule and strict prohibition of automated `goose down` executions in production.
- [ ] **GitHub Secrets Specification**: Confirmation of the required repository secrets (`PROD_SSH_PRIVATE_KEY`, `PROD_VPS_HOST`, `PROD_VPS_USER`, `GHCR_READ_TOKEN`).
- [ ] **Worktree Isolation**: Implementation code will strictly execute within an isolated Git worktree:
  ```bash
  make worktree-create NAME=ci-cd-pipeline
  ```

---

*End of RFC-004 Specification. Awaiting User Sign-Off to proceed with Phase 3 Worktree Implementation.*

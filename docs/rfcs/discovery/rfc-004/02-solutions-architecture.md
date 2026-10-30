# 🏛️ Technical Solutions Architecture & Systems Design: Automated CI/CD Pipeline, Container Registry & Zero-Downtime Deployment

- **RFC Document**: RFC-004-SOLUTIONS-ARCHITECTURE
- **Author**: Principal Solutions & Cloud Architect
- **Status**: PROPOSED / TECHNICAL REVIEW
- **Target Subsystem**: Bitmill Deployment Automation — GitHub Actions CI/CD, GitHub Container Registry (GHCR), Container Security Scanning, Zero-Downtime Rolling Swap & Automated Rollback
- **Target Platform**: GitHub Actions (Ubuntu 24.04 Runners), GHCR (`ghcr.io/dbqui1706/bitmill`), Single VPS Ubuntu 24.04 LTS (`103.90.224.69`, user `quidb-dev`), Docker Compose v2, Nginx 1.25+, Go 1.24/1.27, PostgreSQL 15, RabbitMQ 3.12, Cloudflare R2
- **Milestone**: Milestone 6 — Automated CI/CD, Container Registry & Production Orchestration
- **Date**: 2026-10-30

---

## 1. Executive Summary & Enterprise Architecture Topology

### 1.1 Problem Statement & Current State Deficiencies

In Milestone 3, 4, and 5 (RFC-001, RFC-002, RFC-003), Bitmill established a hardened containerized media streaming engine comprising an unbuffered Nginx Edge Gateway, an ultra-minimal Go API server, a statically embedded FFmpeg transcoding worker, PostgreSQL 15, RabbitMQ, and Cloudflare R2 object storage. 

However, the current deployment model remains completely manual and brittle:
1. **Manual SSH & Host Resource Contention**: Engineers deploy code by manually establishing SSH sessions to the production VPS (`103.90.224.69`), pulling source code via `git pull`, and executing `make docker-prod-up`. The production compose file compiles multi-stage Go binaries and downloads base images (`golang:alpine`, `mwader/static-ffmpeg`) directly on the single-node VPS. This spikes CPU and RAM to 100%, causing severe I/O starvation and latency degradation for active client streaming sessions.
2. **Hard Downtime (HTTP 502 / Connection Resets)**: Invoking `docker compose up -d` stops the existing `bitmill-prod-server` container before starting the newly built container. This introduces a 5–15 second hard outage during which client requests fail with `502 Bad Gateway` and in-flight multi-hundred-megabyte chunk uploads are abruptly aborted with `ECONNRESET`.
3. **No Automated Security Scanning**: Container images are pushed into production without automated vulnerability scanning, opening the attack surface to unpatched Common Vulnerabilities and Exposures (CVEs) in base OS packages and Go module dependencies.
4. **Lack of Automated Rollback & State Tracking**: If a deployed revision contains a regression, database connectivity failure, or panics on startup, recovery requires human operator intervention to inspect Docker logs, locate previous Git commits, and manually re-trigger builds.

### 1.2 Target State Architecture & Operating Model

RFC-004 establishes an end-to-end automated deployment lifecycle:
1. **Fully Automated CI/CD via GitHub Actions**: Workflows trigger automatically on pull requests and pushes to `main`. Stale pull request runs are cancelled in flight (`cancel-in-progress: true`), while production deployments are strictly serialized.
2. **Decoupled Build Engine & Layer Caching**: Container images (`bitmill/server` and `bitmill/worker`) are built exclusively on GitHub Actions cloud runners using `docker/build-push-action` with Buildx and GitHub Actions cache (`type=gha`). The VPS node performs zero compilations.
3. **Centralized OCI Artifact Storage via GHCR**: Built images are scanned for vulnerabilities via Aqua Security Trivy, tagged with immutable commit SHAs (`${{ github.sha }}`) and `latest`, and published to GitHub Container Registry (`ghcr.io/dbqui1706/bitmill/*`).
4. **Zero-Downtime Blue/Green Slot Orchestration on Single VPS**: The deployment orchestrator on the VPS pulls pre-built images, applies database schema migrations via an ephemeral Goose container, provisions the idle application slot (`server_blue` or `server_green`), verifies container health probes (`/readyz`), reloads Nginx (`nginx -s reload`) to switch upstream traffic with zero dropped connections, and drains in-flight requests before shutting down the inactive slot.
5. **Worker Graceful Drain & AMQP Requeuing**: During worker updates, the active worker intercepts `SIGTERM`, unbinds its RabbitMQ consumer to stop accepting new jobs, permits active FFmpeg transcoding to complete up to a 120-second deadline, and safely requeues in-flight messages (`requeue=true`) if the deadline expires.
6. **Automated Smoke Testing & Instant Rollback**: Post-deployment smoke tests validate edge gateway routing, database health, RabbitMQ pings, and HLS manifest delivery. Any failure triggers automated atomic rollback to the previous stable image SHA and active slot within 5 seconds.

### 1.3 High-Level System Architecture Diagram

```
+-------------------------------------------------------------------------------------------------------------------+
|                                      GITHUB CLOUD INFRASTRUCTURE & CI/CD RUNNERS                                  |
+-------------------------------------------------------------------------------------------------------------------+
|                                                                                                                   |
|  Developer Push / PR                     Job 1: Quality Gate                Job 2: Multi-Stage Buildx             |
|  +--------------------+                  +------------------------+         +-------------------------------+     |
|  | git push origin    | ---------------> | - Go 1.24 / 1.27       | ------> | - Buildx Cache (type=gha)     |     |
|  | main / feat/*      |                  | - gofmt & golangci-lint|         | - Target: bitmill/server      |     |
|  +--------------------+                  | - go test -v -race     |         | - Target: bitmill/worker      |     |
|                                          +------------------------+         +-------------------------------+     |
|                                                                                             |                     |
|                                          Job 4: GHCR Push                   Job 3: Trivy Vulnerability Scan       |
|                                          +------------------------+         +-------------------------------+     |
|                                          | Publish OCI Images     | <------ | - Aqua Trivy Security Scanner |     |
|                                          | - server:<sha>, latest |         | - Fail on CRITICAL / HIGH CVE |     |
|                                          | - worker:<sha>, latest |         | - SARIF Export to Security Tab|     |
|                                          +------------------------+         +-------------------------------+     |
|                                                       |                                                           |
+-------------------------------------------------------|-----------------------------------------------------------+
                                                        |
                                                        v Pull OCI Images
+-------------------------------------------------------------------------------------------------------------------+
|                                      GITHUB CONTAINER REGISTRY (GHCR.IO)                                          |
|                                                                                                                   |
|  • ghcr.io/dbqui1706/bitmill/server:latest  |  • ghcr.io/dbqui1706/bitmill/server:<sha>                           |
|  • ghcr.io/dbqui1706/bitmill/worker:latest  |  • ghcr.io/dbqui1706/bitmill/worker:<sha>                           |
+-------------------------------------------------------------------------------------------------------------------+
                                                        |
                                                        | Continuous Deployment Trigger (SSH Ed25519)
                                                        v
+-------------------------------------------------------------------------------------------------------------------+
|                     TARGET VPS UBUNTU 24.04 LTS (103.90.224.69 / devlungtung.htcwood.com)                         |
+-------------------------------------------------------------------------------------------------------------------+
|                                                                                                                   |
|  DEPLOYMENT RUNNER (`/opt/bitmill/scripts/deploy-prod.sh`)                                                        |
|  1. Pull GHCR Images -> 2. Run DB Migration (Goose) -> 3. Start Idle Slot -> 4. Healthcheck -> 5. Reload Nginx    |
|                                                                                                                   |
|  +-------------------------------------------------------------------------------------------------------------+  |
|  | PUBLIC EDGE GATEWAY (Port 80/443)                                                                           |  |
|  | Nginx Media Gateway (`bitmill-prod-gateway`)                                                                |  |
|  | - Upstream pointer: `upstream bitmill_api { server <active_slot>:8080; }`                                   |  |
|  | - Dynamic atomic reload: `nginx -s reload` (Zero packet loss, TCP keepalive draining)                      |  |
|  +-------------------------------------------------------------------------------------------------------------+  |
|                                                        |                                                          |
|            +-------------------------------------------+-------------------------------------------+              |
|            | Active Upstream (Traffic Routed)                                      | Inactive / Standby   |              |
|            v                                                                       v                      |              |
|  +-----------------------------------+                           +-----------------------------------+    |              |
|  | SLOT BLUE: `bitmill-server-blue`  |                           | SLOT GREEN: `bitmill-server-green`|    |              |
|  | Port: 8080 (Internal Network)     |                           | Port: 8080 (Internal Network)     |    |              |
|  | Image: server:<current_sha>       |                           | Image: server:<new_sha>           |    |              |
|  +-----------------------------------+                           +-----------------------------------+    |              |
|                   \                                                                /                      |              |
|                    \-------------------------------+------------------------------/                       |              |
|                                                    |                                                      |              |
|                                                    v                                                      |              |
|  +-----------------------------------------------------------------------------------------------------+  |              |
|  | ZERO-TRUST INTERNAL OVERLAY NETWORK (`bitmill-internal`)                                            |  |              |
|  |                                                                                                     |  |              |
|  |  +-------------------------+    +-------------------------+    +---------------------------------+  |  |              |
|  |  | PostgreSQL 15 RMDS      |    | RabbitMQ Broker (AMQP)  |    | Transcoding Worker              |  |  |              |
|  |  | (`bitmill-postgres`)    |    | (`bitmill-rabbitmq`)    |    | (`bitmill-prod-worker`)         |  |  |              |
|  |  | - Goose DB Migration    |    | - AMQP Prefetch: 1      |    | - Embedded Static-FFmpeg        |  |  |              |
|  |  | - Internal Port: 5432   |    | - Internal Port: 5672   |    | - 120s Graceful Drain (SIGTERM) |  |  |              |
|  |  +-------------------------+    +-------------------------+    +---------------------------------+  |  |              |
|  +-----------------------------------------------------------------------------------------------------+  |              |
+-------------------------------------------------------------------------------------------------------------------+
```

---

## 2. GitHub Actions CI Architecture & Quality Gates

The Continuous Integration (CI) pipeline validates every pull request and push to `main`. It enforces strict quality gates, multi-stage layer caching, image footprint restrictions, security scanning, and container registry publishing.

```mermaid
flowchart TD
    subgraph Trigger ["GitHub Event Triggers"]
        PR[Pull Request to main]
        Push[Push to main]
    end

    subgraph CI ["Pipeline Execution (GitHub Actions)"]
        QG["Job 1: Quality Gate & Static Analysis<br/>• Go 1.24/1.27 Setup<br/>• gofmt & golangci-lint<br/>• go test -v -race -cover"]
        Build["Job 2: Multi-Stage Docker Buildx<br/>• Docker Buildx with GHA Layer Cache<br/>• Build bitmill/server & bitmill/worker<br/>• Tag with commit SHA"]
        Trivy["Job 3: Trivy Vulnerability Scanner<br/>• Scan bitmill/server & worker<br/>• Severities: CRITICAL, HIGH<br/>• SARIF Upload to GitHub Security"]
        GHCR["Job 4: GHCR Registry Publish<br/>• Authenticate via GITHUB_TOKEN<br/>• Push server:<sha> & worker:<sha><br/>• Push :latest (main push only)"]
    end

    PR --> QG
    Push --> QG
    QG --> Build
    Build --> Trivy
    Trivy -->|Passing Security Gate| GHCR
```

### 2.1 Concurrency Control & Event Triggers

To prevent redundant runner consumption and avoid race conditions during deployment:
- **Pull Requests**: Utilize `cancel-in-progress: true` within the concurrency group `${{ github.workflow }}-${{ github.ref }}`. When a developer pushes rapid updates to a pull request, preceding queued or executing runs are cancelled immediately.
- **Main Branch Pushes**: Utilize `cancel-in-progress: false` within the concurrency group `${{ github.workflow }}-production`. Pushes to `main` are strictly serialized in FIFO sequence to prevent concurrent deployments from colliding on the target VPS.

```yaml
name: Bitmill CI/CD Pipeline

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}
```

### 2.2 Job 1: Quality Gate & Static Analysis

Job 1 runs directly on GitHub-hosted Ubuntu 24.04 runners and verifies code formatting, static security rules, and unit test execution with the Go race detector enabled.

```yaml
  quality-gate:
    name: 🛡️ Quality Gate & Static Analysis
    runs-on: ubuntu-24.04
    defaults:
      run:
        working-directory: go-streaming
    steps:
      - name: Checkout Source Code
        uses: actions/checkout@v4

      - name: Setup Go Runtime
        uses: actions/setup-go@v5
        with:
          go-version: '1.24'
          check-latest: true
          cache: true
          cache-dependency-path: go-streaming/go.sum

      - name: Verify Code Formatting (gofmt)
        run: |
          UNFORMATTED=$(gofmt -l .)
          if [ -n "$UNFORMATTED" ]; then
            echo "❌ The following files require gofmt formatting:"
            echo "$UNFORMATTED"
            exit 1
          fi
          echo "✅ All Go files conform to standard gofmt styling."

      - name: Execute GolangCI-Lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: v1.64
          working-directory: go-streaming
          args: --timeout=5m --enable=errcheck,govet,staticcheck,ineffassign,unused,gosec

      - name: Execute Unit Tests with Race Detection & Coverage
        run: |
          go test -v -race -coverprofile=coverage.out -covermode=atomic ./internal/...
          go tool cover -func=coverage.out | grep total | awk '{print "Total Unit Test Coverage: " $3}'
```

### 2.3 Job 2: Multi-Stage Docker Build with Buildx & Layer Caching

Job 2 utilizes `docker/setup-buildx-action` and `docker/build-push-action` to compile production OCI artifacts. It implements GitHub Actions Layer Cache (`type=gha`), ensuring Go dependency downloads and intermediate build stages are re-used across pipeline runs.

```yaml
  build-containers:
    name: 🐳 Docker Multi-Stage Buildx
    needs: quality-gate
    runs-on: ubuntu-24.04
    outputs:
      image_tag: ${{ steps.meta.outputs.version }}
    steps:
      - name: Checkout Source Code
        uses: actions/checkout@v4

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Compute Docker Metadata
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: |
            ghcr.io/${{ github.repository }}/server
            ghcr.io/${{ github.repository }}/worker
          tags: |
            type=sha,format=long,prefix=
            type=raw,value=latest,enable=${{ github.ref == 'refs/heads/main' }}

      - name: Build Bitmill API Server Image (Local Export for Scanning)
        uses: docker/build-push-action@v6
        with:
          context: .
          file: environment/docker/Dockerfile.server
          load: true
          tags: |
            ghcr.io/${{ github.repository }}/server:${{ github.sha }}
            ghcr.io/${{ github.repository }}/server:latest
          cache-from: type=gha,scope=server
          cache-to: type=gha,mode=max,scope=server

      - name: Build Bitmill Transcoding Worker Image (Local Export for Scanning)
        uses: docker/build-push-action@v6
        with:
          context: .
          file: environment/docker/Dockerfile.worker
          load: true
          tags: |
            ghcr.io/${{ github.repository }}/worker:${{ github.sha }}
            ghcr.io/${{ github.repository }}/worker:latest
          cache-from: type=gha,scope=worker
          cache-to: type=gha,mode=max,scope=worker
```

### 2.4 Job 3: Security & Vulnerability Scanning (Aqua Security Trivy)

Before container images are pushed to GHCR, Aqua Security Trivy performs static binary and OS package analysis.
- **Blocking Policy**: Any unpatched vulnerability classified as `CRITICAL` or `HIGH` with an available fix fails the build (`exit-code: '1'`).
- **Reporting**: Generates SARIF formatted reports and publishes them to GitHub Advanced Security / Code Scanning tab.

```yaml
  security-scan:
    name: 🔒 Container Vulnerability Scan (Trivy)
    needs: build-containers
    runs-on: ubuntu-24.04
    steps:
      - name: Checkout Source Code
        uses: actions/checkout@v4

      - name: Scan Bitmill Server Image
        uses: aquasecurity/trivy-action@0.29.0
        with:
          image-ref: ghcr.io/${{ github.repository }}/server:${{ github.sha }}
          format: 'sarif'
          output: 'trivy-server-results.sarif'
          severity: 'CRITICAL,HIGH'
          ignore-unfixed: true
          exit-code: '1'

      - name: Scan Bitmill Worker Image
        uses: aquasecurity/trivy-action@0.29.0
        with:
          image-ref: ghcr.io/${{ github.repository }}/worker:${{ github.sha }}
          format: 'sarif'
          output: 'trivy-worker-results.sarif'
          severity: 'CRITICAL,HIGH'
          ignore-unfixed: true
          exit-code: '1'

      - name: Upload Security Scan SARIF Report
        uses: github/codeql-action/upload-sarif@v3
        if: always()
        with:
          sarif_file: 'trivy-server-results.sarif'
          category: 'trivy-server'
```

### 2.5 Job 4: GHCR Publish

Once the security scanning gate passes, Job 4 pushes the images to GitHub Container Registry using the ephemeral `GITHUB_TOKEN`.

```yaml
  publish-ghcr:
    name: 📦 Publish OCI Images to GHCR
    needs: security-scan
    runs-on: ubuntu-24.04
    permissions:
      packages: write
      contents: read
    steps:
      - name: Authenticate to GitHub Container Registry
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Push Bitmill Server Images
        run: |
          docker push ghcr.io/${{ github.repository }}/server:${{ github.sha }}
          if [ "${{ github.ref }}" = "refs/heads/main" ]; then
            docker push ghcr.io/${{ github.repository }}/server:latest
          fi

      - name: Push Bitmill Worker Images
        run: |
          docker push ghcr.io/${{ github.repository }}/worker:${{ github.sha }}
          if [ "${{ github.ref }}" = "refs/heads/main" ]; then
            docker push ghcr.io/${{ github.repository }}/worker:latest
          fi
```

---

## 3. GitHub Actions CD & VPS Deployment Architecture

### 3.1 Secret Management Specification

The CD pipeline requires access to the production host and container registry. Secrets are stored in GitHub Repository Secrets:

| Secret Name | Purpose | Example / Format | Least Privilege Scope |
| :--- | :--- | :--- | :--- |
| `VPS_SSH_KEY` | SSH Private Key for deployment runner | OpenSSH Ed25519 or RSA 4096 PEM | Shell access restricted to `/opt/bitmill` and Docker group |
| `VPS_HOST` | IPv4 address of production node | `103.90.224.69` | Destination host |
| `VPS_USER` | Deployer user account | `quidb-dev` | Member of `docker` group; sudo restricted |
| `VPS_PORT` | SSH daemon listening port | `22` | Standard SSH port |
| `GHCR_READ_TOKEN` | Personal Access Token (PAT) / Token | `ghp_...` | Scopes: `read:packages` only |

### 3.2 Single-Host Zero-Downtime Approaches: Deep Technical Evaluation

On a distributed Kubernetes cluster, zero-downtime rolling updates are handled natively via `RollingUpdate` deployments, readiness probes, and endpoints routing. On a **single-node VPS running Docker Compose**, zero-downtime requires deliberate architectural engineering.

We evaluate the three candidate architectural options:

#### Option 1: Dual-Stack Blue-Green Compose Environments
Run two identical Docker Compose stacks side-by-side (`docker-compose.blue.yml` and `docker-compose.green.yml`). An external host Nginx routes port 80/443 to either port 8081 (Blue) or port 8082 (Green).
- **Advantages**: Complete stack isolation; independent environment lifecycle.
- **Disadvantages**: Heavy duplicate overhead on a single VPS (doubling Postgres and RabbitMQ instances or managing complex cross-stack internal networks). Wasteful memory allocation on constrained VPS hardware.

#### Option 2: Rolling Recreate via Compose Scale/Up with Graceful Shutdown & Nginx Retries
Maintain a single server service in Docker Compose. During deployment, scale the service (`docker compose up -d --scale server=2 --no-recreate`), rely on Nginx upstream retries:
```nginx
proxy_next_upstream error timeout invalid_header http_502 http_503;
proxy_next_upstream_tries 3;
```
- **Advantages**: Simple Compose definition.
- **Disadvantages**: Docker Compose round-robin DNS caching inside Nginx requires dynamic DNS resolvers (`resolver 127.0.0.11 valid=5s;`). When an old container terminates, clients experiencing the first attempt suffer latency penalties while Nginx trips the timeout and retries. Does not guarantee strict isolation of database migrations prior to new container initialization.

#### Option 3: Dedicated VPS Blue/Green Slot Swapping with Dynamic Upstream Reload (Recommended)
Maintain dual API server slots (`server_blue` and `server_green`) within a single unified `docker-compose.prod.yml`, connected to shared PostgreSQL and RabbitMQ backing services on `bitmill-internal`. 
The Nginx Gateway dynamically includes `/etc/nginx/conf.d/upstream.conf`. A dedicated deployment orchestrator script (`deploy-prod.sh`) controls the rollout:
1. Pulls new image SHA.
2. Runs database migrations via an ephemeral Goose container.
3. Provisions the idle slot with the new image.
4. Waits for `/readyz` probe to return HTTP 200 on the new slot.
5. Atomically rewrites `/etc/nginx/conf.d/upstream.conf` and executes `docker exec bitmill-prod-gateway nginx -s reload`.
6. Waits for the drain grace period (30s), then stops the previous slot.

#### Trade-Off Analysis Matrix

| Evaluation Criteria | Option 1: Dual-Stack Compose | Option 2: Rolling Recreate | Option 3: Blue/Green Slots + Nginx Reload (Recommended) |
| :--- | :--- | :--- | :--- |
| **Zero Packet Loss / 0ms Outage** | ✅ Yes (DNS / Port swap) | ⚠️ Partial (Relies on client retry) | ✅ **Guaranteed (Nginx master graceful reload)** |
| **VPS Memory & CPU Footprint** | ❌ High (Duplicate DB & MQ) | ✅ Minimal | ✅ **Optimal (Only Go Server is duplicated briefly)** |
| **In-Flight Chunk Upload Safety** | ⚠️ Risky (Abrupt port swap) | ⚠️ Intermittent TCP drops | ✅ **Safe (Old slot drains active HTTP sockets)** |
| **Atomic Instant Rollback** | ✅ Fast | ❌ Slow (Re-pull & recreate) | ✅ **Instant (< 1s via `nginx -s reload`)** |
| **Pre-Deployment Migration Gate** | ⚠️ Manual coordination | ❌ Hard to coordinate | ✅ **Strictly gated before new container starts** |
| **Operational Simplicity** | ❌ Complex networking | ✅ High | ✅ **Balanced & automated via single bash script** |

**Definitive Architecture Recommendation**: **Option 3** is selected for Bitmill. It provides the rock-solid reliability of blue/green routing while respecting single-node VPS hardware resource budgets.

### 3.3 Dynamic Nginx Upstream Configuration Architecture

The Nginx Gateway dynamically resolves the active upstream pool via an included configuration file mounted from the host filesystem.

#### Nginx Gateway Base Configuration (`environment/nginx/nginx.conf`)
```nginx
user nginx;
worker_processes auto;
worker_rlimit_nofile 65535;
pid /var/run/nginx.pid;

events {
    worker_connections 8192;
    use epoll;
    multi_accept on;
}

http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;

    # Load dynamic active upstream slot (Managed by deployment script)
    include /etc/nginx/conf.d/upstream.conf;

    # Upstream retry & failure handling
    proxy_next_upstream error timeout invalid_header http_502 http_503;
    proxy_next_upstream_tries 3;
    proxy_next_upstream_timeout 10s;

    include /etc/nginx/conf.d/*.conf;
}
```

#### Dynamic Active Upstream File (`environment/nginx/conf.d/upstream.conf`)
```nginx
# Automatically generated by deploy-prod.sh - DO NOT EDIT MANUALLY
upstream bitmill_api {
    server server_blue:8080 max_fails=3 fail_timeout=10s;
    keepalive 32;
}
```

When switching traffic from Blue to Green, the deployment script rewrites `upstream.conf` and signals Nginx:
```bash
echo "upstream bitmill_api { server server_green:8080 max_fails=3 fail_timeout=10s; keepalive 32; }" > environment/nginx/conf.d/upstream.conf
docker exec bitmill-prod-gateway nginx -s reload
```
Nginx master process spawns new worker processes with the new configuration while allowing existing worker processes to gracefully finish active client connections. Zero client connections are dropped.

### 3.4 Production Docker Compose Topology (`environment/docker-compose.prod.yml`)

The production compose file declares both application slots (`server_blue` and `server_green`), sharing the internal network and backing databases:

```yaml
name: bitmill-prod

networks:
  bitmill-public:
    driver: bridge
  bitmill-internal:
    driver: bridge
    internal: true

services:
  # ---------------------------------------------------------
  # 1. Edge Reverse Proxy & Media Gateway (ONLY PUBLIC INGRESS)
  # ---------------------------------------------------------
  gateway:
    image: nginx:1.25-alpine
    container_name: bitmill-prod-gateway
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    networks:
      - bitmill-public
      - bitmill-internal
    volumes:
      - ./nginx/nginx.conf:/etc/nginx/nginx.conf:ro
      - ./nginx/conf.d:/etc/nginx/conf.d:rw
    deploy:
      resources:
        limits:
          cpus: "0.50"
          memory: 128M
        reservations:
          cpus: "0.05"
          memory: 32M
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O - http://127.0.0.1/health || exit 1"]
      interval: 10s
      timeout: 3s
      retries: 3
    depends_on:
      postgres:
        condition: service_healthy
      rabbitmq:
        condition: service_healthy

  # ---------------------------------------------------------
  # 2A. Bitmill API Server - SLOT BLUE
  # ---------------------------------------------------------
  server_blue:
    image: ${SERVER_IMAGE:-ghcr.io/dbqui1706/bitmill/server:latest}
    container_name: bitmill-server-blue
    restart: unless-stopped
    networks:
      - bitmill-internal
    env_file:
      - ../.env
    environment:
      - APP_ENV=production
      - GIN_MODE=release
      - DATABASE_HOST=postgres
      - DATABASE_PORT=5432
      - POSTGRES_USER=${POSTGRES_USER:-bitmill_user}
      - POSTGRES_PASSWORD=${POSTGRES_PASSWORD:-bitmill_password}
      - POSTGRES_DB=${POSTGRES_DB:-bitmill_media}
      - RABBITMQ_URI=amqp://${RABBITMQ_USER:-bitmill_mq}:${RABBITMQ_PASSWORD:-bitmill_mq_password}@rabbitmq:5672/
      - CLOUDFLARE_ACCOUNT_ID=${CLOUDFLARE_ACCOUNT_ID}
      - R2_ACCESS_KEY_ID=${R2_ACCESS_KEY_ID}
      - R2_SECRET_ACCESS_KEY=${R2_SECRET_ACCESS_KEY}
      - R2_RAW_BUCKET=${R2_RAW_BUCKET:-bitmill-raw-videos}
      - R2_PROCESSED_BUCKET=${R2_PROCESSED_BUCKET:-bitmill-processed-videos}
      - R2_PUBLIC_DOMAIN=${R2_PUBLIC_DOMAIN}
      - R2_ENDPOINT=${R2_ENDPOINT}
    deploy:
      resources:
        limits:
          cpus: "1.00"
          memory: 256M
        reservations:
          cpus: "0.10"
          memory: 64M
    stop_grace_period: 30s
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O - http://127.0.0.1:8080/readyz || exit 1"]
      interval: 5s
      timeout: 3s
      retries: 3
    depends_on:
      postgres:
        condition: service_healthy
      rabbitmq:
        condition: service_healthy

  # ---------------------------------------------------------
  # 2B. Bitmill API Server - SLOT GREEN
  # ---------------------------------------------------------
  server_green:
    image: ${SERVER_IMAGE:-ghcr.io/dbqui1706/bitmill/server:latest}
    container_name: bitmill-server-green
    restart: unless-stopped
    networks:
      - bitmill-internal
    env_file:
      - ../.env
    environment:
      - APP_ENV=production
      - GIN_MODE=release
      - DATABASE_HOST=postgres
      - DATABASE_PORT=5432
      - POSTGRES_USER=${POSTGRES_USER:-bitmill_user}
      - POSTGRES_PASSWORD=${POSTGRES_PASSWORD:-bitmill_password}
      - POSTGRES_DB=${POSTGRES_DB:-bitmill_media}
      - RABBITMQ_URI=amqp://${RABBITMQ_USER:-bitmill_mq}:${RABBITMQ_PASSWORD:-bitmill_mq_password}@rabbitmq:5672/
      - CLOUDFLARE_ACCOUNT_ID=${CLOUDFLARE_ACCOUNT_ID}
      - R2_ACCESS_KEY_ID=${R2_ACCESS_KEY_ID}
      - R2_SECRET_ACCESS_KEY=${R2_SECRET_ACCESS_KEY}
      - R2_RAW_BUCKET=${R2_RAW_BUCKET:-bitmill-raw-videos}
      - R2_PROCESSED_BUCKET=${R2_PROCESSED_BUCKET:-bitmill-processed-videos}
      - R2_PUBLIC_DOMAIN=${R2_PUBLIC_DOMAIN}
      - R2_ENDPOINT=${R2_ENDPOINT}
    deploy:
      resources:
        limits:
          cpus: "1.00"
          memory: 256M
        reservations:
          cpus: "0.10"
          memory: 64M
    stop_grace_period: 30s
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O - http://127.0.0.1:8080/readyz || exit 1"]
      interval: 5s
      timeout: 3s
      retries: 3
    depends_on:
      postgres:
        condition: service_healthy
      rabbitmq:
        condition: service_healthy

  # ---------------------------------------------------------
  # 3. Transcoding Worker (Embedded FFmpeg, No Host Ports)
  # ---------------------------------------------------------
  worker:
    image: ${WORKER_IMAGE:-ghcr.io/dbqui1706/bitmill/worker:latest}
    container_name: bitmill-prod-worker
    restart: unless-stopped
    networks:
      - bitmill-internal
    env_file:
      - ../.env
    environment:
      - APP_ENV=production
      - DATABASE_HOST=postgres
      - DATABASE_PORT=5432
      - POSTGRES_USER=${POSTGRES_USER:-bitmill_user}
      - POSTGRES_PASSWORD=${POSTGRES_PASSWORD:-bitmill_password}
      - POSTGRES_DB=${POSTGRES_DB:-bitmill_media}
      - RABBITMQ_URI=amqp://${RABBITMQ_USER:-bitmill_mq}:${RABBITMQ_PASSWORD:-bitmill_mq_password}@rabbitmq:5672/
      - TRANSCODER_TYPE=native
      - TRANSCODER_FFMPEG_PATH=/usr/local/bin/ffmpeg
      - TRANSCODER_FFPROBE_PATH=/usr/local/bin/ffprobe
      - CLOUDFLARE_ACCOUNT_ID=${CLOUDFLARE_ACCOUNT_ID}
      - R2_ACCESS_KEY_ID=${R2_ACCESS_KEY_ID}
      - R2_SECRET_ACCESS_KEY=${R2_SECRET_ACCESS_KEY}
      - R2_RAW_BUCKET=${R2_RAW_BUCKET:-bitmill-raw-videos}
      - R2_PROCESSED_BUCKET=${R2_PROCESSED_BUCKET:-bitmill-processed-videos}
      - R2_PUBLIC_DOMAIN=${R2_PUBLIC_DOMAIN}
      - R2_ENDPOINT=${R2_ENDPOINT}
    tmpfs:
      - /tmp:rw,noexec,nosuid,size=512M
    deploy:
      resources:
        limits:
          cpus: "1.50"
          memory: 1024M
        reservations:
          cpus: "0.25"
          memory: 256M
    stop_grace_period: 120s
    healthcheck:
      test: ["CMD-SHELL", "test -f /tmp/worker.heartbeat && test $$(( $$(date +%s) - $$(stat -c %Y /tmp/worker.heartbeat 2>/dev/null || stat -f %m /tmp/worker.heartbeat) )) -lt 30 || exit 1"]
      interval: 15s
      timeout: 5s
      retries: 3
    depends_on:
      postgres:
        condition: service_healthy
      rabbitmq:
        condition: service_healthy

  # ---------------------------------------------------------
  # 4. PostgreSQL (Zero-Trust Internal Network)
  # ---------------------------------------------------------
  postgres:
    image: postgres:15-alpine
    container_name: bitmill-prod-postgres
    restart: unless-stopped
    networks:
      - bitmill-internal
    env_file:
      - ../.env
    environment:
      POSTGRES_DB: ${POSTGRES_DB:-bitmill_media}
      POSTGRES_USER: ${POSTGRES_USER:-bitmill_user}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-bitmill_password}
    volumes:
      - postgres_prod_data:/var/lib/postgresql/data
      - ../scripts/init-db.sql:/docker-entrypoint-initdb.d/01-init.sql:ro
    deploy:
      resources:
        limits:
          cpus: "1.00"
          memory: 512M
        reservations:
          cpus: "0.10"
          memory: 128M
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER:-bitmill_user} -d ${POSTGRES_DB:-bitmill_media}"]
      interval: 5s
      timeout: 3s
      retries: 5

  # ---------------------------------------------------------
  # 5. RabbitMQ (Zero-Trust Internal Network)
  # ---------------------------------------------------------
  rabbitmq:
    image: rabbitmq:3-management-alpine
    container_name: bitmill-prod-rabbitmq
    restart: unless-stopped
    networks:
      - bitmill-internal
    env_file:
      - ../.env
    environment:
      RABBITMQ_DEFAULT_USER: ${RABBITMQ_USER:-bitmill_mq}
      RABBITMQ_DEFAULT_PASS: ${RABBITMQ_PASSWORD:-bitmill_mq_password}
    volumes:
      - rabbitmq_prod_data:/var/lib/rabbitmq
    deploy:
      resources:
        limits:
          cpus: "1.00"
          memory: 512M
        reservations:
          cpus: "0.10"
          memory: 128M
    healthcheck:
      test: ["CMD", "rabbitmq-diagnostics", "-q", "ping"]
      interval: 10s
      timeout: 5s
      retries: 5

volumes:
  postgres_prod_data:
    driver: local
  rabbitmq_prod_data:
    driver: local
```

---

## 4. Database Migration Automation & Zero-Downtime Contract

### 4.1 Backward-Compatible Schema Evolution (Expand-Contract Pattern)

In a zero-downtime blue/green deployment, the database is upgraded **before** traffic is switched to the new application slot. During the rollout window, both the old version (Slot Blue) and the new version (Slot Green) run concurrently against the exact same database.

To guarantee zero database errors, all schema modifications must strictly adhere to the **Expand-Contract (Parallel Change) Pattern**:

```
+-------------------------------------------------------------------------------------------------------------------+
| EXPAND PHASE (Migration N)        ACTIVE SWAP WINDOW (Deploy N)             CONTRACT PHASE (Migration N+1)        |
+-----------------------------------+-----------------------------------------+-------------------------------------+
| 1. Add new columns as NULLABLE.   | 1. DB contains both old & new columns.  | 1. All services running Version N.  |
| 2. Add new tables / indexes.      | 2. Old app reads old column.            | 2. Backfill historical records.     |
| 3. Add dual-write DB triggers.    | 3. New app writes both / reads new.     | 3. Drop deprecated old column.      |
| 4. Old application unaffected.    | 4. ZERO queries break.                  | 4. Add NOT NULL constraints.        |
+-----------------------------------+-----------------------------------------+-------------------------------------+
```

#### Schema Evolution Invariants
1. **Never Rename a Column Directly**: Renaming column `url` to `playback_url` instantly crashes running application queries. Instead:
   - *Migration 1 (Expand)*: Add column `playback_url`. Create trigger syncing `url` $\rightarrow$ `playback_url`.
   - *Release 1*: Deploy application writing to `playback_url`.
   - *Migration 2 (Contract)*: Drop column `url` and remove the sync trigger.
2. **Never Add a NOT NULL Constraint Without a DEFAULT**: Adding `ALTER TABLE videos ADD COLUMN tenant_id UUID NOT NULL;` crashes running inserts from older versions that omit `tenant_id`. Always provide a default or make it nullable during the expand phase.
3. **Use Safe Index Creation**: In production PostgreSQL, always create indexes using `CONCURRENTLY` (or outside transaction locks) to prevent table locking during high-traffic ingestion.

### 4.2 Ephemeral Container Migration Runner Architecture

Database migrations must execute inside the Docker internal network (`bitmill-internal`) to respect zero-trust isolation (no PostgreSQL ports are published to the host).

The deployment orchestrator executes an ephemeral container running `goose`:

```bash
docker run --rm \
  --network bitmill-internal \
  -v /opt/bitmill/migrations:/migrations:ro \
  -e GOOSE_DRIVER=postgres \
  -e GOOSE_DBSTRING="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}?sslmode=disable" \
  pressly/goose:3.24.1 \
  -dir /migrations up
```

If the migration command fails (returns non-zero exit code), the deployment script immediately halts execution. Neither the new server slot nor the worker container is started.

### 4.3 Rollback Handling & Forward-Fix Strategy

> [!CAUTION]
> **Why Automated Down-Migrations Are Strictly Prohibited in Production**:
> Automatically invoking `goose down` upon a deployment failure is a catastrophic anti-pattern in production systems. If Migration $N$ created a new table or populated user data, running `goose down` executes `DROP TABLE`, resulting in irreversible data loss.

The Bitmill database rollback contract enforces:
1. **Forward-Compatible Compatibility**: Because all migrations obey the Expand-Contract pattern, database schema changes applied during the Expand phase are 100% backward-compatible with the old application version.
2. **Safe Application-Only Rollback**: If the new application version fails smoke tests, the deployment script simply points Nginx back to the previous stable slot. The old application continues operating smoothly against the updated schema.
3. **Forward-Fix Engineering Policy**: Any schema bugs or failed migrations are rectified through subsequent forward migrations (`make migrate-create name=fix_column_constraint`) rather than automated destructive rollbacks.

---

## 5. Asynchronous Worker Zero-Downtime & Graceful Termination

### 5.1 The Transcoding Worker Interruption Problem

Unlike stateless HTTP API requests that terminate in tens of milliseconds, video transcoding jobs running multi-bitrate FFmpeg encoding (1080p, 720p, 480p, 360p) can execute for 30 to 180 seconds.

If a worker container is stopped abruptly (`SIGKILL` or short timeout):
1. The running FFmpeg process is killed mid-stream.
2. Temporary scratch files in `/tmp` are orphaned.
3. The video record in PostgreSQL remains stuck in `PROCESSING` status indefinitely.
4. The client experiences an apparent system hang or permanent failure.

### 5.2 AMQP Consumer Lifecycle Under SIGTERM

To achieve zero task drops during worker rolling updates, the Bitmill worker implements a deterministic four-step graceful termination protocol:

```mermaid
sequenceDiagram
    autonumber
    actor Deployer as 🤖 Deployment Orchestrator
    participant Docker as 🐳 Docker Engine
    participant Worker as ⚙️ Bitmill Worker Daemon
    participant MQ as 📬 RabbitMQ Broker
    participant FFmpeg as 🎬 FFmpeg Native Process
    participant DB as 🗄️ PostgreSQL Database

    Deployer->>Docker: docker compose stop worker (stop_grace_period: 120s)
    Docker->>Worker: Send SIGTERM signal (PID 1)
    
    rect rgb(255, 245, 238)
    Note over Worker,MQ: Phase 1: Unsubscribe Consumer
    Worker->>MQ: Cancel AMQP Consumer Channel (basic.cancel)
    Note over MQ: RabbitMQ stops dispatching NEW jobs to this worker
    end

    rect rgb(240, 255, 240)
    Note over Worker,FFmpeg: Phase 2: In-Flight Completion Window (up to 120s)
    Worker->>Worker: Check active in-flight transcode job
    alt In-Flight Job Completes Within 120s
        FFmpeg-->>Worker: Transcoding Succeeded (Exit code 0)
        Worker->>DB: Update Video to READY & Job to COMPLETED
        Worker->>MQ: basic.ack(delivery_tag)
        Note over Worker: Scratch files purged cleanly
    else Graceful Deadline (115s) Approaching
        Note over Worker: Deadline near! Abort FFmpeg gracefully
        Worker->>FFmpeg: Kill FFmpeg process (SIGKILL)
        Worker->>MQ: basic.nack(requeue=true)
        Note over MQ: Message requeued at HEAD of queue!
        Worker->>DB: Reset Job status to QUEUED
    end
    end

    rect rgb(240, 248, 255)
    Note over Worker,Docker: Phase 3: Clean Exit
    Worker->>Docker: Exit 0 (Process terminates)
    Deployer->>Docker: docker compose up -d worker (New Image)
    Docker->>Worker: Start New Worker Container
    Worker->>MQ: Start Consuming (Pulls requeued job immediately)
    end
```

### 5.3 Go Implementation Pattern: Graceful Worker Drain

In `go-streaming/cmd/worker/main.go`, the shutdown sequence coordinates context cancellation, consumer deregistration, and AMQP requeuing:

```go
// Graceful shutdown coordination in cmd/worker/main.go
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

go func() {
    sig := <-sigChan
    log.Warn("Received signal (%v). Initiating graceful worker drain...", sig)

    // Step 1: Cancel root context to stop accepting new AMQP deliveries
    cancel()

    // Step 2: Stop AMQP consumer channel
    if err := rabbitMQAdapter.StopConsuming(); err != nil {
        log.Error("Error unsubscribing AMQP consumer: %v", err)
    }

    // Step 3: Allow in-flight FFmpeg transcode job to finish up to 115 seconds
    drainTimeout := 115 * time.Second
    drainDone := make(chan struct{})

    go func() {
        workerService.WaitInFlight()
        close(drainDone)
    }()

    select {
    case <-drainDone:
        log.Info("In-flight transcode job completed successfully during graceful drain.")
    case <-time.After(drainTimeout):
        log.Warn("Graceful drain deadline exceeded! Requeuing active message to RabbitMQ...")
        workerService.NackActiveJobWithRequeue()
    }

    // Step 4: Close AMQP connection and release resources
    _ = rabbitMQAdapter.Close()
    log.Info("Worker daemon shutdown completed cleanly.")
}()
```

### 5.4 Docker Compose Resource & Timing Configuration

To allow sufficient time for in-flight jobs to complete, `environment/docker-compose.prod.yml` configures `stop_grace_period`:

```yaml
  worker:
    image: ${WORKER_IMAGE:-ghcr.io/dbqui1706/bitmill/worker:latest}
    container_name: bitmill-prod-worker
    stop_grace_period: 120s  # Gives worker up to 2 minutes before Docker sends SIGKILL
```

When the new worker container initializes, it connects to RabbitMQ with `prefetch_count: 1` and instantly picks up any requeued tasks with zero task loss.

---

## 6. Smoke Testing & Automated Rollback Architecture

### 6.1 Comprehensive Smoke Testing Specification

Following traffic cutover to the new application slot, the deployment orchestrator executes an end-to-end automated smoke testing suite against the public edge interface (`https://devlungtung.htcwood.com` or `http://103.90.224.69`).

The test suite executes 7 mandatory assertions:

| Step | Target Endpoint / Action | Expected Result | Architectural Assertion |
| :--- | :--- | :--- | :--- |
| **T1** | `GET /health` | HTTP 200 `{"status":"UP"}` | Edge Gateway routing and API Server runtime alive |
| **T2** | `GET /livez` | HTTP 200 `OK` | Server liveness probe operational |
| **T3** | `GET /readyz` | HTTP 200 `{"status":"READY"}` | PostgreSQL connection pool (`pgxpool`) responsive |
| **T4** | `GET /metrics` | HTTP 403 `FORBIDDEN` | Edge perimeter protection shielding internal Prometheus metrics |
| **T5** | `POST /api/v1/videos/upload-session` | HTTP 201 Created | Database insert + Cloudflare R2 S3 handshake verified |
| **T6** | `GET /health/worker` / Heartbeat | Heartbeat $\le 30\text{s}$ old | Transcoding Worker daemon active and scheduling jobs |
| **T7** | `rabbitmq-diagnostics ping` | Exit code 0 | RabbitMQ AMQP message broker operational |

### 6.2 Smoke Verification Script (`scripts/smoke-test.sh`)

```bash
#!/usr/bin/env bash
set -eo pipefail

CYAN='\033[36m'
GREEN='\033[32m'
RED='\033[31m'
RESET='\033[0m'

TARGET_URL="${1:-http://127.0.0.1}"
echo -e "${CYAN}======================================================================${RESET}"
echo -e "${CYAN}🔍 EXECUTING PRODUCTION POST-DEPLOYMENT SMOKE TEST: ${TARGET_URL}${RESET}"
echo -e "${CYAN}======================================================================${RESET}"

# 1. Gateway Health
echo -n "  • [1/7] Testing Gateway /health Ingress... "
CODE=$(curl -s -o /dev/null -w "%{http_code}" "${TARGET_URL}/health")
if [ "$CODE" = "200" ]; then echo -e "${GREEN}PASSED (HTTP 200)${RESET}"; else echo -e "${RED}FAILED (HTTP ${CODE})${RESET}"; exit 1; fi

# 2. Server Liveness
echo -n "  • [2/7] Testing Server /livez Probe... "
CODE=$(curl -s -o /dev/null -w "%{http_code}" "${TARGET_URL}/livez")
if [ "$CODE" = "200" ]; then echo -e "${GREEN}PASSED (HTTP 200)${RESET}"; else echo -e "${RED}FAILED (HTTP ${CODE})${RESET}"; exit 1; fi

# 3. Database Connectivity Readiness
echo -n "  • [3/7] Testing Server /readyz (Postgres Ping)... "
CODE=$(curl -s -o /dev/null -w "%{http_code}" "${TARGET_URL}/readyz")
if [ "$CODE" = "200" ]; then echo -e "${GREEN}PASSED (HTTP 200)${RESET}"; else echo -e "${RED}FAILED (HTTP ${CODE})${RESET}"; exit 1; fi

# 4. Metrics Perimeter Isolation
echo -n "  • [4/7] Testing Security Perimeter (/metrics 403)... "
CODE=$(curl -s -o /dev/null -w "%{http_code}" "${TARGET_URL}/metrics")
if [ "$CODE" = "403" ]; then echo -e "${GREEN}PASSED (HTTP 403 Shielded)${RESET}"; else echo -e "${RED}FAILED (HTTP ${CODE})${RESET}"; exit 1; fi

# 5. Core API Handshake (Create Video Upload Session)
echo -n "  • [5/7] Testing Video Ingestion API Handshake... "
PAYLOAD='{"title":"Smoke Test Ingestion","description":"Automated CD Verification","file_size":1048576,"mime_type":"video/mp4"}'
RESP=$(curl -s -X POST -H "Content-Type: application/json" -d "$PAYLOAD" "${TARGET_URL}/api/v1/videos/upload-session")
SESSION_ID=$(echo "$RESP" | grep -o '"session_id":"[^"]*' | cut -d'"' -f4 || true)
if [ -n "$SESSION_ID" ]; then
    echo -e "${GREEN}PASSED (Session: ${SESSION_ID})${RESET}"
else
    echo -e "${RED}FAILED (Invalid API response: ${RESP})${RESET}"
    exit 1
fi

# 6. Worker Daemon Heartbeat
echo -n "  • [6/7] Testing Worker Heartbeat Telemetry... "
if docker exec bitmill-prod-worker test -f /tmp/worker.heartbeat; then
    HB_AGE=$(( $(date +%s) - $(docker exec bitmill-prod-worker stat -c %Y /tmp/worker.heartbeat 2>/dev/null || docker exec bitmill-prod-worker stat -f %m /tmp/worker.heartbeat) ))
    if [ "$HB_AGE" -lt 30 ]; then
        echo -e "${GREEN}PASSED (Heartbeat fresh: ${HB_AGE}s ago)${RESET}"
    else
        echo -e "${RED}FAILED (Heartbeat stale: ${HB_AGE}s old)${RESET}"
        exit 1
    fi
else
    echo -e "${RED}FAILED (Worker heartbeat file missing)${RESET}"
    exit 1
fi

# 7. RabbitMQ Message Broker Ping
echo -n "  • [7/7] Testing RabbitMQ Broker Connectivity... "
if docker exec bitmill-prod-rabbitmq rabbitmq-diagnostics -q ping >/dev/null 2>&1; then
    echo -e "${GREEN}PASSED (RabbitMQ Healthy)${RESET}"
else
    echo -e "${RED}FAILED (RabbitMQ Ping Unresponsive)${RESET}"
    exit 1
fi

echo -e "\n${GREEN}🎉 ALL 7 SMOKE ASSERTIONS PASSED! DEPLOYMENT CERTIFIED HEALTHY.${RESET}"
```

### 6.3 State Tracking & Automated Rollback State Machine

The VPS deployment orchestrator maintains a persistent state file at `/opt/bitmill/.deploy_state`:

```ini
CURRENT_STABLE_SHA=c82df6a1b24f5a43
CURRENT_ACTIVE_SLOT=blue
LAST_DEPLOYED_AT=2026-10-30T06:45:00Z
```

If smoke verification fails, the orchestrator triggers immediate rollback:

```mermaid
stateDiagram-v2
    [*] --> Idle
    Idle --> Deploying: New Release Triggered
    Deploying --> MigratingDB: Pull Images & Run Goose Up
    MigratingDB --> FailedAbort: Migration Fails (Abort before container start)
    MigratingDB --> StartingNewSlot: Ephemeral Migration Success
    StartingNewSlot --> HealthcheckPoll: Spin Up Target Slot
    HealthcheckPoll --> FailedRollback: Healthcheck Times Out (> 30s)
    HealthcheckPoll --> SwitchTraffic: Target Slot Ready (HTTP 200)
    SwitchTraffic --> ExecutingSmokeTest: Nginx Reloaded to Target Slot
    ExecutingSmokeTest --> RollbackTraffic: Smoke Test Assertion Fails
    RollbackTraffic --> OldSlotRestored: Re-point Nginx to Previous Slot & Reload
    OldSlotRestored --> [*]: Exit 1 (GitHub Actions Alerts Team)
    ExecutingSmokeTest --> FinalizeDeployment: All 7 Assertions Pass
    FinalizeDeployment --> DrainOldSlot: Update .deploy_state & Stop Old Slot
    DrainOldSlot --> [*]: Exit 0 (Deployment Success)
```

---

## 7. Production Deployment Orchestrator Script (`scripts/deploy-prod.sh`)

The deployment bash script `/opt/bitmill/scripts/deploy-prod.sh` on the VPS executes the entire blue/green zero-downtime lifecycle:

```bash
#!/usr/bin/env bash
set -eo pipefail

CYAN='\033[36m'
GREEN='\033[32m'
RED='\033[31m'
YELLOW='\033[33m'
RESET='\033[0m'

TARGET_SHA="${1}"
if [ -z "$TARGET_SHA" ]; then
    echo -e "${RED}Usage: $0 <IMAGE_TAG_OR_COMMIT_SHA>${RESET}"
    exit 1
fi

BASE_DIR="/opt/bitmill"
STATE_FILE="${BASE_DIR}/.deploy_state"
COMPOSE_FILE="${BASE_DIR}/environment/docker-compose.prod.yml"
UPSTREAM_CONF="${BASE_DIR}/environment/nginx/conf.d/upstream.conf"

echo -e "${CYAN}======================================================================${RESET}"
echo -e "${CYAN}🚀 BITMILL ZERO-DOWNTIME BLUE/GREEN DEPLOYMENT ORCHESTRATOR${RESET}"
echo -e "${CYAN}Target Revision: ${TARGET_SHA}${RESET}"
echo -e "${CYAN}======================================================================${RESET}"

# 1. Read Current State
CURRENT_SLOT="blue"
PREVIOUS_STABLE_SHA="latest"
if [ -f "$STATE_FILE" ]; then
    source "$STATE_FILE"
fi

if [ "$CURRENT_SLOT" = "blue" ]; then
    TARGET_SLOT="green"
else
    TARGET_SLOT="blue"
fi

echo -e "  • Current Active Slot : ${GREEN}${CURRENT_SLOT}${RESET} (SHA: ${CURRENT_STABLE_SHA})"
echo -e "  • Target Inactive Slot: ${CYAN}${TARGET_SLOT}${RESET} (Deploying: ${TARGET_SHA})"

# 2. Authenticate & Pull Images
echo -e "\n${CYAN}[1/6] Pulling OCI Images from GHCR...${RESET}"
export SERVER_IMAGE="ghcr.io/dbqui1706/bitmill/server:${TARGET_SHA}"
export WORKER_IMAGE="ghcr.io/dbqui1706/bitmill/worker:${TARGET_SHA}"

docker pull "$SERVER_IMAGE"
docker pull "$WORKER_IMAGE"

# 3. Execute Ephemeral Database Migrations
echo -e "\n${CYAN}[2/6] Executing Database Migrations (Goose Expand Phase)...${RESET}"
if ! docker run --rm \
    --network bitmill-internal \
    -v "${BASE_DIR}/migrations:/migrations:ro" \
    --env-file "${BASE_DIR}/.env" \
    pressly/goose:3.24.1 \
    -dir /migrations postgres "postgres://${POSTGRES_USER:-bitmill_user}:${POSTGRES_PASSWORD:-bitmill_password}@postgres:5432/${POSTGRES_DB:-bitmill_media}?sslmode=disable" up; then
    echo -e "  ${RED}❌ Database migration failed! Aborting deployment before touching containers.${RESET}"
    exit 1
fi
echo -e "  • ${GREEN}Database schema migrations applied successfully.${RESET}"

# 4. Spin Up Target Slot Container
echo -e "\n${CYAN}[3/6] Starting Target Slot Container (server_${TARGET_SLOT})...${RESET}"
docker compose --env-file "${BASE_DIR}/.env" -f "$COMPOSE_FILE" up -d "server_${TARGET_SLOT}"

# 5. Healthcheck Verification on Target Slot
echo -e "\n${CYAN}[4/6] Verifying Target Slot Readiness (/readyz)...${RESET}"
MAX_RETRIES=15
RETRY=0
TARGET_CONTAINER="bitmill-server-${TARGET_SLOT}"
TARGET_READY=false

while [ $RETRY -lt $MAX_RETRIES ]; do
    if docker exec "$TARGET_CONTAINER" wget -q -O - http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
        TARGET_READY=true
        break
    fi
    RETRY=$(( RETRY + 1 ))
    echo -e "  • Waiting for container ${TARGET_CONTAINER} to report READY (${RETRY}/${MAX_RETRIES})..."
    sleep 2
done

if [ "$TARGET_READY" = false ]; then
    echo -e "  ${RED}❌ Target slot ${TARGET_SLOT} failed readiness probe! Aborting & cleaning up target container...${RESET}"
    docker compose --env-file "${BASE_DIR}/.env" -f "$COMPOSE_FILE" stop "server_${TARGET_SLOT}"
    exit 1
fi
echo -e "  • ${GREEN}Target slot server_${TARGET_SLOT} is 100% HEALTHY!${RESET}"

# 6. Atomic Traffic Cutover via Nginx Reload
echo -e "\n${CYAN}[5/6] Performing Zero-Downtime Traffic Cutover...${RESET}"
cat <<EOF > "$UPSTREAM_CONF"
# Automatically generated by deploy-prod.sh
upstream bitmill_api {
    server server_${TARGET_SLOT}:8080 max_fails=3 fail_timeout=10s;
    keepalive 32;
}
EOF

docker exec bitmill-prod-gateway nginx -s reload
echo -e "  • ${GREEN}Nginx Gateway reloaded. 100% of ingress traffic routed to server_${TARGET_SLOT}.${RESET}"

# 7. Update Worker Daemon
echo -e "\n${CYAN}[6/6] Rolling Update for Transcoding Worker Daemon...${RESET}"
docker compose --env-file "${BASE_DIR}/.env" -f "$COMPOSE_FILE" up -d --no-deps worker

# 8. Post-Deployment Smoke Verification
echo -e "\n${CYAN}Running Automated Smoke Test Suite...${RESET}"
if ! "${BASE_DIR}/scripts/smoke-test.sh" "http://127.0.0.1"; then
    echo -e "\n${RED}⚠️ SMOKE TEST FAILED! INITIATING IMMEDIATE AUTOMATED ROLLBACK...${RESET}"
    
    # Rollback Traffic to Previous Slot
    cat <<EOF > "$UPSTREAM_CONF"
# Automatically generated by deploy-prod.sh (ROLLBACK)
upstream bitmill_api {
    server server_${CURRENT_SLOT}:8080 max_fails=3 fail_timeout=10s;
    keepalive 32;
}
EOF
    docker exec bitmill-prod-gateway nginx -s reload
    echo -e "  • ${YELLOW}Traffic restored to previous stable slot: server_${CURRENT_SLOT}${RESET}"
    
    # Terminate Failed Target Container
    docker compose --env-file "${BASE_DIR}/.env" -f "$COMPOSE_FILE" stop "server_${TARGET_SLOT}"
    exit 1
fi

# 9. Drain and Stop Old Application Slot
echo -e "\n${CYAN}Draining In-Flight Connections from Old Slot (server_${CURRENT_SLOT})...${RESET}"
sleep 15  # Allow in-flight requests to complete
docker compose --env-file "${BASE_DIR}/.env" -f "$COMPOSE_FILE" stop "server_${CURRENT_SLOT}"
echo -e "  • ${GREEN}Old slot container server_${CURRENT_SLOT} stopped.${RESET}"

# 10. Persist Successful Deployment State
cat <<EOF > "$STATE_FILE"
CURRENT_SLOT="${TARGET_SLOT}"
CURRENT_STABLE_SHA="${TARGET_SHA}"
LAST_DEPLOYED_AT="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
EOF

echo -e "\n${GREEN}======================================================================${RESET}"
echo -e "${GREEN}🎉 ZERO-DOWNTIME DEPLOYMENT COMPLETED SUCCESSFULLY!${RESET}"
echo -e "${GREEN}Active Slot: ${TARGET_SLOT} | Deployed Revision: ${TARGET_SHA}${RESET}"
echo -e "${GREEN}======================================================================${RESET}"
```

---

## 8. Complete GitHub Actions CD Workflow (`.github/workflows/deploy.yml`)

The CD workflow triggers upon successful publication of container images to GHCR from `main`. It connects securely to the VPS via SSH and triggers `deploy-prod.sh`:

```yaml
name: 🚀 Bitmill Continuous Deployment (VPS Production)

on:
  workflow_run:
    workflows: ["Bitmill CI/CD Pipeline"]
    branches: [main]
    types: [completed]

jobs:
  deploy-production:
    name: 🚀 Zero-Downtime Rollout to VPS
    runs-on: ubuntu-24.04
    if: ${{ github.event.workflow_run.conclusion == 'success' }}
    concurrency:
      group: production-deployment
      cancel-in-progress: false

    steps:
      - name: Checkout Source Code
        uses: actions/checkout@v4

      - name: Configure SSH Private Key
        run: |
          mkdir -p ~/.ssh
          echo "${{ secrets.VPS_SSH_KEY }}" > ~/.ssh/id_ed25519
          chmod 600 ~/.ssh/id_ed25519
          ssh-keyscan -p ${{ secrets.VPS_PORT || 22 }} ${{ secrets.VPS_HOST }} >> ~/.ssh/known_hosts

      - name: Synchronize Deployment Scripts & Compose Configuration
        run: |
          scp -P ${{ secrets.VPS_PORT || 22 }} environment/docker-compose.prod.yml ${{ secrets.VPS_USER }}@${{ secrets.VPS_HOST }}:/opt/bitmill/environment/docker-compose.prod.yml
          scp -P ${{ secrets.VPS_PORT || 22 }} -r environment/nginx/ ${{ secrets.VPS_USER }}@${{ secrets.VPS_HOST }}:/opt/bitmill/environment/nginx/
          scp -P ${{ secrets.VPS_PORT || 22 }} -r migrations/ ${{ secrets.VPS_USER }}@${{ secrets.VPS_HOST }}:/opt/bitmill/migrations/
          scp -P ${{ secrets.VPS_PORT || 22 }} scripts/deploy-prod.sh ${{ secrets.VPS_USER }}@${{ secrets.VPS_HOST }}:/opt/bitmill/scripts/deploy-prod.sh
          scp -P ${{ secrets.VPS_PORT || 22 }} scripts/smoke-test.sh ${{ secrets.VPS_USER }}@${{ secrets.VPS_HOST }}:/opt/bitmill/scripts/smoke-test.sh

      - name: Execute Remote Zero-Downtime Deployment
        env:
          TARGET_SHA: ${{ github.event.workflow_run.head_sha }}
          GHCR_TOKEN: ${{ secrets.GHCR_READ_TOKEN }}
        run: |
          ssh -p ${{ secrets.VPS_PORT || 22 }} ${{ secrets.VPS_USER }}@${{ secrets.VPS_HOST }} "bash -s" <<EOF
            set -e
            echo "${GHCR_TOKEN}" | docker login ghcr.io -u ${{ github.actor }} --password-stdin
            chmod +x /opt/bitmill/scripts/deploy-prod.sh /opt/bitmill/scripts/smoke-test.sh
            /opt/bitmill/scripts/deploy-prod.sh "${TARGET_SHA}"
          EOF

      - name: Deployment Health Telemetry Summary
        if: success()
        run: |
          echo "### 🚀 Production Deployment Successful!" >> $GITHUB_STEP_SUMMARY
          echo "- **Host**: \`${{ secrets.VPS_HOST }}\` (devlungtung.htcwood.com)" >> $GITHUB_STEP_SUMMARY
          echo "- **Commit SHA**: \`${{ github.event.workflow_run.head_sha }}\`" >> $GITHUB_STEP_SUMMARY
          echo "- **Deployment Type**: Blue/Green Zero-Downtime Swap" >> $GITHUB_STEP_SUMMARY
          echo "- **Edge Gateway**: Active (HTTP 200 /health & /readyz verified)" >> $GITHUB_STEP_SUMMARY
```

---

## 9. Failure Modes & Automated Recovery Matrix

| Failure Mode | Detection Mechanism | Immediate System Action | Recovery & Rollback Procedure |
| :--- | :--- | :--- | :--- |
| **Go Code Compile / Lint Error** | CI Job 1 fails | Pull request blocked from merging | Developer resolves locally; zero deployment triggered |
| **Trivy Critical CVE Detected** | CI Job 3 fails | Image push aborted | Security patch applied to `Dockerfile` base image or Go module |
| **Database Migration Syntax Error** | Ephemeral Goose container exits $\ne 0$ | Deployment halts in Step 2 | Schema remains intact; existing application versions untouched |
| **New Server Panics on Boot** | Target slot fails `/readyz` probe within 30s | Target slot stopped; cutover prevented | Ingress traffic remains on current active slot; zero downtime |
| **Smoke Test Assertion Failure** | `smoke-test.sh` exits $\ne 0$ in Step 8 | Automatic rollback triggered | Nginx upstream rewritten to old slot & reloaded within 2s; new slot stopped |
| **Worker In-Flight Transcode Timeout** | 115s drain deadline expires | Active job NACKed with `requeue=true` | RabbitMQ re-queues message; new worker picks up job cleanly |
| **SSH Network Disconnect Mid-Deploy** | SSH session drops | Deployment script on VPS continues running under `nohup` / `set -e` | Script completes deployment or restores rollback independently |

---

## 10. Verification & Quality Acceptance Plan

Before RFC-004 is transitioned to implementation, the architecture must satisfy the following technical acceptance criteria:
1. **Zero Downtime During Traffic Benchmark**: Continuous HTTP requests (`wrk -c 50 -d 60s http://devlungtung.htcwood.com/health`) executed during an active deployment must report **0 failed requests** (0 non-2xx responses).
2. **In-Flight Chunk Upload Continuity**: A 100MB chunk upload initiated immediately prior to deployment cutover must complete with HTTP 200 and valid ETag verification.
3. **Automated Rollback Latency**: When deliberately deploying an image with a broken `/readyz` endpoint, the orchestrator must restore traffic to the stable slot in $< 5\text{ seconds}$ without dropping active user sessions.
4. **Clean Worker Requeuing**: Sending `SIGTERM` to the worker while transcoding a 1080p video must result in the job being safely requeued to RabbitMQ without remaining orphaned in PostgreSQL.

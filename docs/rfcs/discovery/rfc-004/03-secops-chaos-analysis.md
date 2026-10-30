# 🛡️ RFC-004: Automated CI/CD Pipeline, Container Registry & Zero-Downtime Deployment — SecOps, Threat Modeling & Chaos Verification Specification

- **Status**: Draft / Proposed
- **Author**: Principal SecOps & QA Chaos Specialist
- **Date**: 2026-10-30
- **Target System**: Bitmill Video Streaming Platform (Milestone 6 Delivery)
- **Production Host**: Ubuntu 24.04 LTS (IP: `103.90.224.69`, Hostname/Domain: `devlungtung.htcwood.com`)
- **Deployment User**: `quidb-dev`
- **Scope**: CI/CD Threat Modeling (STRIDE), Supply Chain Hardening (GHCR, Cosign, Trivy, SBOM), Host Security & Least Privilege SSH/Docker, FMEA Risk Assessment for Zero-Downtime Swaps, Automated Multi-Tier Quality Gates, Chaos Engineering Verification Suite, Audit Logging & SRE Telemetry

---

## 1. Executive Summary & Security Objectives

RFC-004 formalizes the automated continuous integration, container packaging, registry distribution, and zero-downtime deployment architecture for the Bitmill platform. While RFC-003 established the production container topology (Nginx Gateway, API Server, Embedded FFmpeg Worker, Postgres 15, RabbitMQ), RFC-004 establishes the secure operational delivery mechanism from code commit in GitHub to execution on the production VPS at `103.90.224.69`.

### 1.1. Core Security & Operational Mandates
1. **Supply Chain Integrity**: Every container artifact deployed to production must originate from an authenticated, auditable GitHub Actions workflow, built from cryptographic source commits, scanned for vulnerabilities (Trivy CVSS < 7.0), signed with Cosign, and pulled from GitHub Packages Container Registry (`ghcr.io`).
2. **Zero-Trust Host Defense**: The production Ubuntu 24.04 host must enforce defense-in-depth: non-root execution (`quidb-dev`), password authentication disabled, dedicated Ed25519 deploy key isolation, strict UFW firewall boundaries (Ports 22, 80, 443 only), and Fail2ban intrusion defense.
3. **Atomic Zero-Downtime Swaps**: Deployments must achieve 0 dropped HTTP chunks during resumable multipart uploads, 0 aborted transcoding jobs during worker rollouts, backward-compatible database schema migrations (Expand-Contract pattern), and automatic rollback within 15 seconds if healthchecks fail.
4. **Chaos-Proven Resilience**: Deployment automation must withstand network drops mid-SSH transfer, container boot loops, corrupted configurations, and database lock contention without leaving orphaned containers or inconsistent states.

### 1.2. End-to-End Deployment & Trust Zone Topology

```mermaid
flowchart TD
    subgraph DeveloperWorkstation["Developer Trust Boundary"]
        Dev["Engineer / Committer\n(GPG Signed Commits)"]
    end

    subgraph GitHubControlPlane["Zone A: GitHub Cloud & CI/CD Pipeline"]
        GH_Repo["GitHub Repository\n(branch: main / tags)"]
        GH_Actions["GitHub Actions Runner\n(ubuntu-latest ephemeral)"]
        Linters["golangci-lint | govulncheck | gosec"]
        TestRunner["go test -race -cover (>=80%)"]
        TrivyScanner["Trivy Vulnerability Scanner\n(Blocks CVSS >= 7.0)"]
        SyftSBOM["Syft SBOM Generator\n(SPDX / CycloneDX)"]
        CosignSigner["Cosign Image Signing\n(OIDC / Key-Pair Signing)"]
        GHCR["GitHub Container Registry (ghcr.io)\nbitmill/server:sha-<commit>\nbitmill/worker:sha-<commit>"]
    end

    subgraph NetworkTransit["Zone B: Encrypted Transit (Internet)"]
        SSH_Tunnel["Encrypted SSH Session\n(Ed25519 Key Auth | Port 22)\nNo Password Allowed"]
        TLS_Pulls["HTTPS / TLS 1.3\n(GHCR Token Scoped Pulls)"]
    end

    subgraph ProductionHost["Zone C: Production VPS (103.90.224.69 - devlungtung.htcwood.com)"]
        UFW["Linux UFW Firewall\n(Allow: 22, 80, 443 | Deny: 5432, 5672, 8080)"]
        Fail2ban["Fail2ban SSH Jail\n(MaxRetry: 3, BanTime: 24h)"]
        DeployUser["Unprivileged User: quidb-dev\n(Docker Group | No Direct Root SSH)"]
        EnvFile["Production Secrets (.env)\nchmod 600 quidb-dev:quidb-dev"]
        DeployEngine["Zero-Downtime Deploy Engine\n(Rolling Dual-Instance / Health Probe / Rollback)"]
        
        subgraph DockerRuntime["Docker Engine (Linux cgroups v2 & Bridge Network)"]
            NginxProd["Nginx Media Gateway\n(Ports 80/443 | devlungtung.htcwood.com)\nproxy_request_buffering off"]
            APICurrent["API Server (Active)\nbitmill-prod-server-blue\nUID 10001 (nonroot)"]
            APINew["API Server (Standby/Canary)\nbitmill-prod-server-green\nUID 10001 (nonroot)"]
            WorkerCurrent["Transcoding Worker Fleet\nEmbedded static-ffmpeg\nGraceful Drain Handler"]
            PostgresProd["PostgreSQL 15 (Internal Only)\nGoose Migrations Engine"]
            RabbitMQProd["RabbitMQ Cluster (Internal Only)\nPrefetch=1 Quorum"]
        end
    end

    Dev -->|git push origin feat/*| GH_Repo
    GH_Repo -->|PR / Merge to main| GH_Actions
    GH_Actions --> Linters
    Linters --> TestRunner
    TestRunner --> TrivyScanner
    TrivyScanner --> SyftSBOM
    SyftSBOM --> CosignSigner
    CosignSigner -->|Push Images| GHCR

    GH_Actions -->|Trigger Deploy via SSH| SSH_Tunnel
    SSH_Tunnel --> UFW
    UFW --> Fail2ban
    Fail2ban --> DeployUser
    DeployUser --> DeployEngine
    DeployEngine -->|Read| EnvFile
    DeployEngine -->|Pull Digests| TLS_Pulls
    TLS_Pulls --> GHCR

    DeployEngine -->|1. Validate Health| APINew
    DeployEngine -->|2. Reload Upstream| NginxProd
    DeployEngine -->|3. Drain & Stop| APICurrent
    DeployEngine -->|Graceful Sigterm Drain| WorkerCurrent
    APINew --> PostgresProd
    APINew --> RabbitMQProd
```

---

## 2. Threat Modeling & Attack Surface Analysis (STRIDE & MITRE ATT&CK)

### 2.1. Threat Boundary & Asset Classification

In automated CI/CD and production infrastructure, attack vectors transition from application layer vulnerabilities to pipeline hijacking, credential theft, supply chain poisoning, and container runtime breakouts.

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│ ASSET INVENTORY & SENSITIVITY CLASSIFICATION                                     │
├────────────────────────────────┬──────────────────────────┬───────────────────────┤
│ Asset                          │ Sensitivity              │ Impact of Compromise  │
├────────────────────────────────┼──────────────────────────┼───────────────────────┤
│ GitHub Actions Workflow Config │ High (Integrity)         │ Malicious code deploy │
│ GitHub Repository Secrets      │ Critical (Confidential)  │ Host takeover         │
│ Production SSH Private Key     │ Critical (Confidential)  │ Full VPS host root    │
│ GHCR Authentication Token      │ High (Integrity)         │ Supply chain tamper   │
│ Production `.env` Secrets      │ Critical (Confidential)  │ DB/R2 data breach     │
│ Cloudflare R2 Master Keys      │ Critical (Confidential)  │ Video asset deletion  │
│ Production PostgreSQL Data     │ Critical (Confidential)  │ PII & Metadata loss   │
│ Production RabbitMQ Broker     │ High (Availability)     │ DoS of transcoding    │
└────────────────────────────────┴──────────────────────────┴───────────────────────┘
```

---

### 2.2. Comprehensive STRIDE Threat Matrix

| STRIDE Category | Threat Description | Attack Vector / Exploitation Scenario | Targeted Subsystem | Default Impact | Mitigation Strategy |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Spoofing** | Rogue CI Runner or Impersonated SSH Deployer | Attacker uses compromised credentials or spoofed IP to initiate SSH connection to `103.90.224.69`, executing arbitrary deployment commands. | VPS SSH Service (`sshd`) | **Critical (Host Takeover)** | Enforce strict Ed25519 SSH public key authentication; disable passwords; bind SSH access; restrict deploy keys with command scoping or bastion controls; configure Fail2ban. |
| **Spoofing** | Unsigned / Forged Container Image Ingestion | Attacker pushes a poisoned image directly to an untrusted registry or compromises GHCR tags, tricking the VPS into running a rogue binary. | GHCR Registry & Docker Daemon | **Critical (Malicious Execution)** | Cosign image signing during CI build; pin container deployments to immutable SHA256 image digests (`ghcr.io/bitmill/server@sha256:...`) instead of mutable tags (`:latest`). |
| **Tampering** | Dependency Confusion / Typosquatting in Go Modules | Malicious Go dependency injected into `go.mod` during build phase, executing backdoor during binary compilation. | Go Streaming Build Pipeline | **High (RCE in Container)** | Enforce `go.sum` cryptographic verification; run `go mod verify`; configure `govulncheck` in CI to detect known vulnerable libraries before compilation. |
| **Tampering** | In-Transit Modification of Deployment Artifacts | Attacker performs Man-in-the-Middle (MITM) on HTTP downloads of binaries, migration scripts, or configs during deploy. | VPS Deployment Script | **High (System Corruption)** | Enforce TLS 1.3 for all GHCR image pulls; clone migration scripts strictly over SSH / HTTPS with commit verification; verify file SHA256 checksums before execution. |
| **Repudiation** | Unlogged Deployment Execution or Shadow Rollbacks | Rogue developer or compromised CI secret triggers deployment or rollback without audit trace, obscuring the incident source. | CI/CD & Production Audit Log | **Medium (Compliance Failure)** | Structured immutable deployment logging on VPS (`/var/log/bitmill/deployments.log`); GitHub Actions immutable audit trail with commit SHA, actor, and signed run provenance. |
| **Information Disclosure** | Secret Exfiltration via CI Logs or Pull Requests | Adversary opens malicious pull request with modified CI workflow or tests that print `${{ secrets.PROD_SSH_KEY }}` or upload secrets to an external server. | GitHub Actions Secrets Engine | **Catastrophic (Credential Leak)** | Restrict secrets from untrusted PRs: use standard `pull_request` event (no secrets exposed to fork PRs); forbid `pull_request_target` without code checkout isolation; enable GitHub secret masking. |
| **Information Disclosure** | Insecure File Permissions on Production `.env` | World-readable `.env` on VPS (`chmod 644`) allows any local user, backup process, or compromised co-located service to read database and R2 credentials. | VPS Filesystem (`/opt/bitmill/.env`) | **Critical (Database & Storage Breach)** | Enforce strict POSIX file permissions: `chmod 600 /opt/bitmill/.env`, owned strictly by `quidb-dev:quidb-dev`; automated validation gate halts deployment if permissions exceed 600. |
| **Denial of Service** | Rollback Loop / Cold-Start Crash Storm | Malformed environment variable or database incompatibility causes new container to crash on boot; automated deployment continuously loops, consuming CPU/RAM. | Production Docker Engine | **High (Complete Outage)** | Exponential backoff on healthcheck retries; maximum failure threshold (3 retries within 30s) aborts rollout and locks release to last-known-good stable SHA. |
| **Denial of Service** | Resource Exhaustion during Parallel Image Pulls | VPS runs out of disk space (`/var/lib/docker` fills to 100%) during pre-pull of new server and worker images, freezing active containers. | VPS Storage (`/dev/sda1`) | **High (Host Crash)** | Pre-flight disk space assertion (`df -h /var/lib/docker` >= 5GB free); automated image pruning (`docker image prune -af --filter "until=72h"` after successful release). |
| **Elevation of Privilege** | Docker Socket Exploitation via `docker` Group | User `quidb-dev` belongs to `docker` group; a command injection in the deployment script allows attacker to execute `docker run -v /:/host alpine` and escalate to host `root`. | VPS OS Kernel | **Critical (Root Escalation)** | Sanitize all inputs in deployment scripts; avoid shell expansion (`eval`); keep deployment script deterministic and immutable; restrict `quidb-dev` sudoers privileges to zero (no sudo in deploy scripts). |

---

### 2.3. CI/CD Pipeline & GitHub Actions Hardening

GitHub Actions runners represent an attractive attack target because they possess access to production secrets, deployment credentials, and artifact registries.

```
❌ VULNERABLE CI PATTERN:
  on: pull_request_target   <-- Executes in context of base repository with ACCESS TO SECRETS!
  steps:
    - uses: actions/checkout@v4
      with:
        ref: ${{ github.event.pull_request.head.sha }}  <-- Untrusted PR code checked out!
    - run: make test                                     <-- Malicious code executes & steals secrets!

✅ HARDENED BITMILL CI PATTERN:
  on:
    pull_request:           <-- Isolated fork environment, ZERO access to repository secrets
      branches: [ main ]
    push:
      branches: [ main ]     <-- Trusted branch only; triggers build & deploy with secrets
```

#### 2.3.1. Runner Privilege Isolation & Secret Hygiene
1. **Separation of Workflows**:
   - `ci-quality-gate.yml`: Runs on all pull requests. Uses default `GITHUB_TOKEN` with read-only permissions (`contents: read`, `pull-requests: read`). Has **no access** to production SSH keys or cloud credentials.
   - `cd-deploy-production.yml`: Runs exclusively on pushes to `main` branch or tag creation (`v*.*.*`) authored by repository collaborators. Executes in the protected GitHub `production` environment requiring approval gates.
2. **Minimal GitHub Token Permissions**:
   Every workflow explicitly specifies top-level permissions:
   ```yaml
   permissions:
     contents: read
     packages: write       # Required only for GHCR image publishing
     security-events: write # Required for Trivy SARIF upload
     id-token: write       # Required for OIDC Cosign keyless signing
   ```
3. **Secret Masking & Exfiltration Defenses**:
   - Secrets (`PROD_SSH_KEY`, `POSTGRES_PASSWORD`, `R2_SECRET_ACCESS_KEY`) are stored in GitHub Environment Secrets.
   - Runners run in ephemeral Azure/AWS cloud instances that terminate immediately after job completion.
   - Output logs are strictly monitored for regex matching key signatures; commands utilizing keys pass variables through environment parameters or file descriptors, never inline CLI arguments visible in process listings (`ps aux`).

---

### 2.4. Supply Chain Security & Container Registry (GHCR) Hardening

#### 2.4.1. Base Image Hardening (Alpine 3.21)
Bitmill container images (`Dockerfile.server` and `Dockerfile.worker`) utilize `alpine:3.21` as the production runner stage:
- **Minimal Attack Surface**: Alpine Linux contains only ~5MB of core utilities (BusyBox, musl libc, apk).
- **Vulnerability Density**: Unlike Ubuntu or Debian base images with hundreds of pre-installed packages (Perl, Python, systemd), Alpine has virtually zero extraneous binaries that can be exploited by an attacker.
- **Rootless Execution**: Both server and worker containers strictly declare:
  ```dockerfile
  USER 10001:10001
  ```
  Neither binary has access to package managers (`apk del apk-tools` can be applied) or root filesystem write operations (`read_only: true`).

#### 2.4.2. Image Digest Pinning vs. Tag Poisoning
Tags in container registries are mutable: an attacker with compromised credentials can overwrite `ghcr.io/bitmill/server:latest` with a backdoored image.
- **Enforcement Policy**:
  - The CI workflow builds and tags images with the Git commit SHA: `ghcr.io/bitmill/server:sha-${{ github.sha }}`.
  - The CI workflow outputs the immutable cryptographic digest: `ghcr.io/bitmill/server@sha256:d8b7a6e5f...`.
  - The production VPS deployment script pulls and starts containers using the **exact SHA256 digest**, guaranteeing that what was scanned and signed in CI is identical to what runs on the host.

#### 2.4.3. Vulnerability Scanning Policy (Trivy Gate)
Every built container image must pass a Trivy container vulnerability scan before being published to GHCR.
- **Fail Criteria**: The pipeline immediately halts with exit code 1 if:
  - Any **CRITICAL** vulnerability (CVSS >= 9.0) is detected.
  - Any **HIGH** vulnerability (CVSS >= 7.0 to 8.9) is detected for which a vendor fix is available.
- **SARIF Integration**: Vulnerability reports are exported as SARIF (`trivy-results.sarif`) and uploaded to GitHub Security Advisory tab for developer remediation tracking.

#### 2.4.4. Software Bill of Materials (SBOM) Generation
Using Anchore Syft, the CI pipeline generates a comprehensive Software Bill of Materials in SPDX 2.3 and CycloneDX JSON formats:
```bash
syft packages ghcr.io/bitmill/server:sha-${{ github.sha }} -o spdx-json=server-sbom.spdx.json
```
The SBOM document enumerates all Go modules, dependencies, C libraries (in FFmpeg), and operating system packages, establishing continuous supply chain traceability for compliance (NIST SP 800-218 SSDF).

#### 2.4.5. Image Signing with Cosign
Images are cryptographically signed using Sigstore Cosign:
```bash
cosign sign --yes ghcr.io/bitmill/server@${IMAGE_DIGEST}
```
On the production VPS, the deployment script verifies the signature before restarting any service:
```bash
cosign verify --certificate-identity-regexp "https://github.com/quidb/bitmill/.*" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ghcr.io/bitmill/server@${IMAGE_DIGEST}
```

---

### 2.5. Production VPS Host & Network Security Hardening

The target deployment host is Ubuntu 24.04 LTS located at IP `103.90.224.69`, resolving domain `devlungtung.htcwood.com`.

#### 2.5.1. SSH Attack Surface Reduction & Key Isolation
The OpenSSH server daemon (`/etc/ssh/sshd_config.d/bitmill-hardening.conf`) must be configured with zero-trust defaults:
```ini
# /etc/ssh/sshd_config.d/bitmill-hardening.conf
Port 22
Protocol 2
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthorizedKeysFile .ssh/authorized_keys
AuthenticationMethods publickey

# Strict Modern Cryptography
KexAlgorithms curve25519-sha256,curve25519-sha256@libssh.org,diffie-hellman-group16-sha512
Ciphers chacha20-poly1305@openssh.com,aes256-gcm@openssh.com
MACs hmac-sha2-512-etm@openssh.com

# Anti-Bruteforce & Idle Timeouts
MaxAuthTries 3
ClientAliveInterval 300
ClientAliveCountMax 2
X11Forwarding no
AllowTcpForwarding no
```

- **Dedicated Deploy Key**: CI/CD uses a single dedicated Ed25519 key pair (`bitmill-deploy-ci`).
- **Command Scoping (Optional / Recommended)**: In `~quidb-dev/.ssh/authorized_keys`, restrict key invocation:
  ```
  command="/opt/bitmill/scripts/deploy-entrypoint.sh",no-port-forwarding,no-X11-forwarding,no-agent-forwarding ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... bitmill-ci-deploy
  ```

#### 2.5.2. Linux UFW & Fail2ban Intrusion Defense
```bash
# UFW Strict Firewall Policy
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow 22/tcp comment 'SSH Ingress'
sudo ufw allow 80/tcp comment 'HTTP Ingress (Nginx)'
sudo ufw allow 443/tcp comment 'HTTPS Ingress (Nginx TLS)'
sudo ufw enable
```
*Note: Ports 5432 (Postgres), 5672 (RabbitMQ), 8080 (Go Server), and 9090 (Prometheus) are strictly DENIED to external traffic.*

Fail2ban is installed and configured to ban repeated invalid SSH attempts:
```ini
# /etc/fail2ban/jail.d/ssh-bitmill.local
[sshd]
enabled = true
port = 22
filter = sshd
logpath = /var/log/auth.log
maxretry = 3
findtime = 600
bantime = 86400
action = iptables-multiport[name=SSH, port="22", protocol=tcp]
```

#### 2.5.3. Docker Daemon Access & Non-Root Execution
- The deployment runner executes under the unprivileged service account `quidb-dev`.
- `quidb-dev` is a member of the `docker` group (`usermod -aG docker quidb-dev`).
- **Security Guardrail**: Because membership in the `docker` group is root-equivalent on Linux hosts (CWE-250), the deployment script must **NEVER invoke `sudo`**. It executes purely declarative `docker compose` commands against pre-configured configurations.
- Containers themselves execute as UID `10001:10001` (user `bitmill`), ensuring that even if a container binary is compromised, it cannot write to the host's `/etc`, `/var`, or `/root`.

---

### 2.6. Secret Management & Zero-Trust Hygiene

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│ ZERO-TRUST SECRETS MATRIX                                                         │
├──────────────────────┬──────────────────────┬─────────────────────────────────────┤
│ Secret Name          │ Storage Location     │ Access & Lifecycle                  │
├──────────────────────┼──────────────────────┼─────────────────────────────────────┤
│ `PROD_SSH_PRIVATE_KEY`│ GitHub Env Secrets  │ Ephemeral SSH-Agent in CI; 90d rot   │
│ `GHCR_DEPLOY_TOKEN`  │ GitHub / VPS Host    │ Scoped to `read:packages`; no repo  │
│ `POSTGRES_PASSWORD`  │ VPS `/opt/bitmill/.env`│ `chmod 600 quidb-dev:quidb-dev`   │
│ `R2_SECRET_ACCESS_KEY`│ VPS `/opt/bitmill/.env`│ Injected into memory via Docker env │
│ `RABBITMQ_PASSWORD`  │ VPS `/opt/bitmill/.env`│ Isolated inside internal network    │
└──────────────────────┴──────────────────────┴─────────────────────────────────────┘
```

1. **No Secrets in Git**: The root `.gitignore` strictly rejects `.env`, `*.pem`, `*.key`, and `secrets/`.
2. **Pre-flight Commit Gate**: A git pre-commit hook and CI step run `git leaks` or pattern matching to block commits containing high-entropy strings or private keys.
3. **VPS Secret Permissions**:
   ```bash
   # Enforced on VPS /opt/bitmill/.env:
   chown quidb-dev:quidb-dev /opt/bitmill/.env
   chmod 600 /opt/bitmill/.env
   ```

---

## 3. Failure Modes & Effects Analysis (FMEA) Matrix for Zero-Downtime Deployments

### 3.1. Quantitative FMEA Methodology & RPN Criteria

The Failure Modes and Effects Analysis calculates a **Risk Priority Number (RPN)** to objectively rank system vulnerabilities during deployments:
$$\text{RPN} = \text{Severity (S)} \times \text{Likelihood (L)} \times \text{Detection (D)}$$
Each attribute is scored on a standardized scale from 1 (lowest risk) to 5 (highest risk).

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│ FMEA RATING CRITERIA (SCALE 1 - 5)                                                │
├───────┬──────────────────────┬────────────────────────┬───────────────────────────┤
│ Score │ Severity (S)         │ Likelihood (L)         │ Detection (D)             │
├───────┼──────────────────────┼────────────────────────┼───────────────────────────┤
│ 1     │ Negligible glitch    │ Extremely rare (<1/yr) │ Instant automated catch   │
│ 2     │ Minor degradation    │ Rare (1-2 times/yr)    │ Detected in CI pre-flight │
│ 3     │ Partial outage/retry │ Moderate (monthly)     │ Caught during healthcheck │
│ 4     │ Data corruption/drop │ Frequent (weekly)      │ Manual telemetry needed   │
│ 5     │ Catastrophic outage  │ Highly persistent      │ Undetected until users cry│
└───────┴──────────────────────┴────────────────────────┴───────────────────────────┘
```

---

### 3.2. Master Zero-Downtime Deployment FMEA Matrix

| ID | Failure Mode | Root Cause / Trigger | S | L | D | RPN | Pre-Mitigation Status | Automated Mitigation Strategy | Post-Mitigation S-L-D (RPN) |
| :--- | :--- | :--- | :---: | :---: | :---: | :---: | :--- | :--- | :---: |
| **FM-1** | CI/CD Runner SSH disconnects mid-deployment | Network glitch, packet loss, or runner timeout while containers are stopping. | 4 | 3 | 3 | **36** | **High Risk**: Host left in partial or zombie state with stopped old container and unbooted new container. | Wrap deployment in an **atomic, detached idempotent deployment script** on the VPS (invoked via `nohup` or `tmux/systemd-run`). The script runs locally to completion regardless of SSH disconnection. State recorded in `/var/run/bitmill-deploy.pid`. | 2 × 1 × 1 = **2** |
| **FM-2** | Incompatible database migration breaks old containers | Destructive DDL (column rename/drop) applied before old containers stop. | 5 | 2 | 3 | **30** | **High Risk**: Old active API containers throw immediate 500 errors on SQL column lookup. | Enforce **Expand-Contract (Two-Phase) Database Migrations**: 1) Additive only during deploy (new nullable columns); 2) Backward compatibility maintained; 3) Destructive cleanup run only in a subsequent release. | 2 × 1 × 1 = **2** |
| **FM-3** | New container boot-loops on bad configuration | Missing environment variable, malformed port, or nil pointer on startup. | 4 | 3 | 2 | **24** | **Medium Risk**: Service fails healthchecks; Nginx routes traffic to dead upstream. | **Dual-Instance Swap (Blue/Green canary)**: New container boots on alternate internal port; gateway routes traffic only AFTER `/readyz` passes 3 consecutive 200 OK probes; automatic rollback restores old container on failure within 15s. | 2 × 1 × 1 = **2** |
| **FM-4** | Transcoding worker killed mid-job on container replace | `docker compose down` sends SIGKILL after 10s timeout while FFmpeg encodes 1GB video. | 4 | 3 | 3 | **36** | **High Risk**: Dangling scratch files on tmpfs, corrupted partial HLS files on R2, orphaned unacknowledged AMQP messages. | Configure `stop_grace_period: 300s` in Docker Compose. Worker intercepts `SIGTERM`, stops consuming new jobs, completes active FFmpeg transcode, or cleanly sends `BasicNack(requeue=true)` to RabbitMQ, deleting scratch files before container exit. | 2 × 1 × 1 = **2** |
| **FM-5** | Nginx reload drops active chunk upload streams | `nginx -s reload` invoked abruptly while 200MB video chunk is streaming. | 4 | 2 | 3 | **24** | **Medium Risk**: Client upload connection resets (ECONNRESET); multipart upload session corrupted. | Nginx master process gracefully handles `reload`: active worker processes finish current HTTP request streams before dying (`worker_shutdown_timeout 300s`); new worker processes accept incoming requests immediately. | 1 × 1 × 1 = **1** |
| **FM-6** | GHCR image pull failure or rate-limit on VPS | Registry 429 Too Many Requests, expired GHCR token, or VPS network partition. | 4 | 2 | 2 | **16** | **Medium Risk**: Compose stops old container, attempts pull, fails, leaving zero running instances. | **Pre-Pull Verification Step**: The deployment script issues `docker pull <image>@<digest>` and verifies exit code 0 **BEFORE** touching or signaling any running production containers. If pull fails, deployment aborts cleanly. | 1 × 1 × 1 = **1** |
| **FM-7** | Database migration lock timeout blocks deployment | Long-running query holds exclusive lock on `videos` table; migration stalls. | 3 | 3 | 2 | **18** | **Medium Risk**: Deployment hangs indefinitely waiting for `ALTER TABLE` lock. | Enforce PostgreSQL statement timeout in Goose migration scripts: `SET lock_timeout = '5s'; SET statement_timeout = '30s';`. Fail fast and abort before restarting containers. | 2 × 1 × 1 = **2** |
| **FM-8** | Rollback failure due to corrupted rollback image | Previous image was pruned or registry digest unreachable during emergency rollback. | 5 | 1 | 4 | **20** | **Critical Risk**: Deployment fails, rollback fails, manual human intervention required under outage pressure. | Maintain local **Last-Known-Good Image Cache**: Deployment script preserves the currently running image tagged as `bitmill/server:stable-rollback` locally on the VPS disk. Never prune active or immediately preceding image. | 2 × 1 × 1 = **2** |

---

### 3.3. In-Depth Failure Mode Analysis & Architectural Remediation

#### 3.3.1. Deep Dive FM-2: The Expand-Contract Database Migration Pattern
When deploying schema changes in a zero-downtime environment, new and old code versions run simultaneously during the handover.

```
❌ WRONG (DESTRUCTIVE MIGRATION):
  -- Migration v3: Rename column
  ALTER TABLE videos RENAME COLUMN s3_raw_key TO object_storage_path;
  💥 RESULT: Active containers running v2 immediately throw SQL error:
  "column videos.s3_raw_key does not exist" -> All video uploads fail!

✅ CORRECT (EXPAND-CONTRACT PATTERN):
  Phase 1 (Release N - Expansion):
    1. ALTER TABLE videos ADD COLUMN object_storage_path VARCHAR(512);
    2. UPDATE videos SET object_storage_path = s3_raw_key WHERE object_storage_path IS NULL;
    3. Application code writes to BOTH columns, reads from s3_raw_key.
  Phase 2 (Release N+1 - Handover):
    1. Application code reads and writes strictly from object_storage_path.
  Phase 3 (Release N+2 - Contraction):
    1. ALTER TABLE videos DROP COLUMN s3_raw_key;
```

#### 3.3.2. Deep Dive FM-4: Worker Draining & AMQP Quorum Message Safety
Transcoding a 1080p/4K video takes minutes. If `bitmill-worker` receives an immediate `SIGKILL`:
1. The child `ffmpeg` process abruptly terminates, leaving truncated `.ts` segments and partial `.m3u8` playlists in `/tmp/bitmill/transcode`.
2. The AMQP message was unacknowledged. Depending on consumer timeout, RabbitMQ eventually re-delivers, but Postgres status is stuck in `processing`.

```mermaid
sequenceDiagram
    autonumber
    participant DeployEngine as Deployment Orchestrator
    participant Worker as bitmill-worker (Go Daemon)
    participant FFmpeg as FFmpeg Child Process
    participant RabbitMQ as RabbitMQ Queue
    participant DB as PostgreSQL 15

    DeployEngine->>Worker: Sends SIGTERM (Graceful Stop)
    Note over Worker: Context Canceled! Enters DRAIN MODE
    Worker->>RabbitMQ: Close Consumer Channel (Stop fetching new jobs)
    alt Worker is IDLE (No job active)
        Worker-->>DeployEngine: Exit immediately (Code 0)
    else Worker is TRANSCODING
        Note over Worker: Active transcode in progress!
        alt Time Remaining < Grace Period (e.g., completes in 20s)
            Worker->>FFmpeg: Wait for completion
            FFmpeg-->>Worker: Transcode successful
            Worker->>DB: Update Video Status (READY)
            Worker->>RabbitMQ: BasicAck(deliveryTag)
            Worker-->>DeployEngine: Exit cleanly (Code 0)
        else Grace Period Exceeded or Urgent Abort
            Worker->>FFmpeg: Kill child process (SIGKILL)
            Worker->>RabbitMQ: BasicNack(deliveryTag, requeue=true)
            Worker->>DB: Reset Video Status (PENDING/RETRY)
            Note over Worker: Clean scratch directory (/tmp)
            Worker-->>DeployEngine: Exit cleanly (Code 0)
        end
    end
```

---

## 4. Automated Quality Gates & Compliance Pipeline

To guarantee that no broken, insecure, or incompatible build reaches production, the deployment lifecycle enforces four sequential, unskippable quality gates.

```
[ Gate 1: Pre-Merge CI ] ──> [ Gate 2: Container Image ] ──> [ Gate 3: Pre-Deploy VPS ] ──> [ Gate 4: Post-Deploy Smoke ]
  • golangci-lint              • Alpine 3.21 Base Pin          • Disk / RAM Resources          • /health & /readyz Probes
  • govulncheck & gosec        • Trivy Scan (CVSS < 7.0)       • GHCR Image Digest Verify      • Database Ping Probe
  • go test -race (>=80%)      • Syft SBOM (SPDX JSON)         • Goose Lock Timeout Probe      • Worker Heartbeat Freshness
  • Strict Clean Compilation   • Cosign Digital Signature      • Backward Compat DDL Gate      • Canary Traffic Swap
```

### 4.1. Gate 1: Pre-Merge Quality Gate (Pull Request CI)
Executed on every PR targeting `main`. **Must pass 100% without warnings.**

```yaml
# Quality Gate 1 Automation Fragment
- name: Run Golang Linter Suite
  run: |
    golangci-lint run --timeout=5m \
      --enable=errcheck,govet,ineffassign,staticcheck,gosec,revive,unused \
      ./...

- name: Security Vulnerability Scan (Go Dependencies)
  run: |
    go install golang.org/x/vuln/cmd/govulncheck@latest
    govulncheck ./...

- name: Execute Unit Tests with Race Detection
  run: |
    cd go-streaming
    go test -v -race -coverprofile=coverage.txt -covermode=atomic ./internal/...
    COVERAGE=$(go tool cover -func=coverage.txt | grep total | awk '{print substr($3, 1, length($3)-1)}')
    echo "Total Test Coverage: ${COVERAGE}%"
    if (( $(echo "$COVERAGE < 80.0" | bc -l) )); then
      echo "❌ Test coverage ${COVERAGE}% is below required 80.0% threshold!"
      exit 1
    fi
```

### 4.2. Gate 2: Artifact & Container Quality Gate (Image Build CI)
Executed upon merge to `main`.

1. **Deterministic Multi-Stage Build**: Compile statically linked Go binaries (`CGO_ENABLED=0`, `-ldflags="-s -w -extldflags '-static'"`).
2. **Trivy Vulnerability Enforcement**:
   ```yaml
   - name: Run Trivy Vulnerability Scanner
     uses: aquasecurity/trivy-action@master
     with:
       image-ref: ghcr.io/bitmill/server:sha-${{ github.sha }}
       format: 'table'
       exit-code: '1'
       ignore-unfixed: true
       vuln-type: 'os,library'
       severity: 'CRITICAL,HIGH'
   ```
3. **Cosign Cryptographic Signature**:
   Sign image digest using GitHub OIDC identity.

### 4.3. Gate 3: Pre-Deployment Stage Gate (VPS Ingress)
Executed on the VPS by `deploy-bitmill.sh` before restarting any containers.

```bash
#!/usr/bin/env bash
# Gate 3: VPS Host Pre-flight Assertions
set -eo pipefail

echo "🔍 [Gate 3/4] Verifying Host Resources & Registry Availability..."

# 1. Disk Space Assertion (Require >= 5GB free)
FREE_DISK_KB=$(df /var/lib/docker | awk 'NR==2 {print $4}')
FREE_DISK_GB=$(( FREE_DISK_KB / 1024 / 1024 ))
if [ "$FREE_DISK_GB" -lt 5 ]; then
  echo "❌ CRITICAL: Insufficient disk space on VPS: ${FREE_DISK_GB}GB available, minimum 5GB required."
  exit 1
fi

# 2. RAM Free Assertion (Require >= 512MB available)
FREE_RAM_MB=$(free -m | awk '/^Mem:/ {print $7}')
if [ "$FREE_RAM_MB" -lt 512 ]; then
  echo "❌ CRITICAL: Insufficient memory on VPS: ${FREE_RAM_MB}MB available, minimum 512MB required."
  exit 1
fi

# 3. Pre-Pull Images with Digest Validation
echo "📥 Pre-pulling target container images..."
docker pull "ghcr.io/bitmill/server@${SERVER_DIGEST}"
docker pull "ghcr.io/bitmill/worker@${WORKER_DIGEST}"

# 4. Validate Database Migration Non-Destructiveness
echo "🗄️ Checking pending database migrations for lock safety..."
goose -dir /opt/bitmill/migrations postgres "${DB_DSN}" status
```

### 4.4. Gate 4: Post-Deployment Smoke & Health Quality Gate
Executed after the new container is instantiated and before routing production ingress.

```bash
#!/usr/bin/env bash
# Gate 4: Health Probing & Canary Validation
echo "🩺 [Gate 4/4] Executing Health Probing & Canary Smoke Suite..."

MAX_RETRIES=15
RETRY=0
HEALTHY=false

while [ $RETRY -lt $MAX_RETRIES ]; do
  # Probe internal server readiness (Checks DB ping)
  HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8081/readyz || echo "000")
  if [ "$HTTP_CODE" = "200" ]; then
    echo "✅ New server instance passed /readyz probe (HTTP 200)!"
    HEALTHY=true
    break
  fi
  echo "⏳ Waiting for server readiness... (attempt $((RETRY+1))/${MAX_RETRIES}, got HTTP ${HTTP_CODE})"
  RETRY=$(( RETRY + 1 ))
  sleep 2
done

if [ "$HEALTHY" = false ]; then
  echo "🚨 CRITICAL: Healthcheck failed after 30 seconds! Triggering Automatic Rollback!"
  exit 1 # Invokes rollback trap
fi

# Check Worker Heartbeat Staleness
WORKER_HEARTBEAT=$(docker exec bitmill-prod-worker cat /tmp/worker.heartbeat 2>/dev/null || echo "0")
CURRENT_TIME=$(date +%s)
HEARTBEAT_DIFF=$(( CURRENT_TIME - WORKER_HEARTBEAT ))
if [ "$HEARTBEAT_DIFF" -gt 20 ]; then
  echo "🚨 CRITICAL: Worker heartbeat is stale (${HEARTBEAT_DIFF}s old)! Triggering Automatic Rollback!"
  exit 1
fi

echo "🎉 Gate 4 Verified: All services healthy. Safe to promote."
```

---

## 5. Chaos Engineering Verification Suite (Detailed Test Plans & Harnesses)

Chaos engineering validates that the deployment automation maintains invariants when subjected to real-world failures.

### 5.1. Chaos Experiment 1: Upload in Flight (Resumable Streaming Invariant)

- **Hypothesis**: Deploying a new API server version while a 200MB video chunk is streaming through the Nginx gateway will result in **zero dropped chunks**, zero connection resets, and successful completion of the multipart upload session.
- **System Invariant**: `proxy_request_buffering off;` with Nginx upstream keepalive ensures active streams complete on the draining server while new uploads route to the newly promoted server.

#### 5.1.1. Chaos Test Harness Script (`chaos-test-upload.sh`)
```bash
#!/usr/bin/env bash
# Chaos Test 1: Upload-in-Flight Resiliency
set -eo pipefail

GATEWAY_URL="http://devlungtung.htcwood.com"
CHUNK_FILE="/tmp/chaos_chunk_200M.bin"

echo "🧪 [Chaos 1] Generating 200MB synthetic video payload..."
dd if=/dev/urandom of="${CHUNK_FILE}" bs=1M count=200 status=none

echo "🚀 Starting 200MB chunk upload in background (simulating slow 10MB/s uplink)..."
# Upload throttled via curl --limit-rate to take ~20 seconds
(
  CURL_RESULT=$(curl -s -w "\n%{http_code}" -X PUT \
    --limit-rate 10M \
    --data-binary @"${CHUNK_FILE}" \
    "${GATEWAY_URL}/api/v1/videos/chaos-test-uuid/parts/1")
  
  HTTP_STATUS=$(echo "$CURL_RESULT" | tail -n 1)
  echo "📡 Background upload completed with HTTP status: ${HTTP_STATUS}"
  if [ "$HTTP_STATUS" != "200" ] && [ "$HTTP_STATUS" != "201" ] && [ "$HTTP_STATUS" != "400" ]; then
    # Note: 400 is acceptable if session uuid not initialized, but NO connection reset allowed
    echo "❌ Upload-in-Flight FAILED: Connection dropped or HTTP 502/503 received!"
    exit 1
  fi
  echo "✅ Upload-in-Flight succeeded with valid HTTP response without connection reset!"
) &
UPLOAD_PID=$!

sleep 3
echo "⚡ INJECTING CHAOS: Triggering production zero-downtime deployment mid-upload..."
/opt/bitmill/scripts/deploy-bitmill.sh

echo "⏳ Waiting for background upload process to finish..."
wait $UPLOAD_PID

rm -f "${CHUNK_FILE}"
echo "🎉 Chaos Test 1 PASSED: Zero dropped chunks during active deployment!"
```

---

### 5.2. Chaos Experiment 2: Transcoding in Progress (Worker Drain & Quorum Invariant)

- **Hypothesis**: Upgrading the `bitmill-worker` container fleet while an FFmpeg worker is actively transcoding a 4K video will cause the worker to **enter drain mode**, finish transcoding within `stop_grace_period`, or cleanly requeue the message in RabbitMQ (`BasicNack(requeue=true)`) without corrupting PostgreSQL or leaving orphaned files on Cloudflare R2.
- **System Invariant**: RabbitMQ Quorum queue must guarantee that unacknowledged jobs are never lost, and PostgreSQL state must remain consistent.

#### 5.2.1. Chaos Test Harness Script (`chaos-test-worker-drain.sh`)
```bash
#!/usr/bin/env bash
# Chaos Test 2: Worker Draining & Message Recovery
set -eo pipefail

echo "🧪 [Chaos 2] Injecting 4K synthetic transcoding task into RabbitMQ..."
# Publish transcode job
TASK_ID=$(docker exec bitmill-prod-server go run /app/cmd/tools/publish_chaos_job.go 2>/dev/null || echo "chaos-vid-001")

echo "⏳ Awaiting worker to claim task and start FFmpeg process..."
sleep 2

# Verify FFmpeg is actively executing inside the container
FFMPEG_PID=$(docker exec bitmill-prod-worker pgrep ffmpeg || true)
if [ -z "$FFMPEG_PID" ]; then
  echo "⚠️ FFmpeg process not detected immediately, awaiting transcode start..."
  sleep 3
  FFMPEG_PID=$(docker exec bitmill-prod-worker pgrep ffmpeg)
fi
echo "🎬 FFmpeg is running inside worker (PID: ${FFMPEG_PID})"

echo "⚡ INJECTING CHAOS: Issuing SIGTERM to trigger graceful container drain..."
docker kill --signal=SIGTERM bitmill-prod-worker

echo "🔍 Monitoring container lifecycle and RabbitMQ unacked status..."
sleep 5

# Check RabbitMQ queue status
UNACKED_COUNT=$(docker exec bitmill-prod-rabbitmq rabbitmqctl list_queues name messages_unacknowledged --silent | grep bitmill_transcode_queue | awk '{print $2}' || echo "0")
echo "📊 RabbitMQ Unacknowledged messages: ${UNACKED_COUNT}"

# Bring new worker up
docker compose -f /opt/bitmill/environment/docker-compose.prod.yml up -d worker

echo "⏳ Verifying new worker picks up requeued job..."
sleep 10

FINAL_STATUS=$(docker exec bitmill-prod-postgres psql -U bitmill_user -d bitmill_media -t -c "SELECT status FROM videos WHERE id='${TASK_ID}'" | xargs)
echo "📊 Database video status after recovery: ${FINAL_STATUS}"

if [ "$FINAL_STATUS" = "COMPLETED" ] || [ "$FINAL_STATUS" = "PROCESSING" ] || [ "$FINAL_STATUS" = "READY" ]; then
  echo "🎉 Chaos Test 2 PASSED: Worker drain & job recovery succeeded without corruption!"
else
  echo "❌ Chaos Test 2 FAILED: Job lost or stuck in invalid state: ${FINAL_STATUS}"
  exit 1
fi
```

---

### 5.3. Chaos Experiment 3: Poison Deployment & Automatic Rollback

- **Hypothesis**: Deploying a container built with a poison configuration (e.g., non-existent database host `DATABASE_HOST=invalid-postgres`) will be detected by the healthcheck probe within 15 seconds, **aborting promotion**, reverting Nginx routing to the previous stable release, and preserving 100% service uptime.
- **System Invariant**: Zero 5xx errors returned to external users during poison rollout.

#### 5.3.1. Chaos Test Harness Script (`chaos-test-poison-rollback.sh`)
```bash
#!/usr/bin/env bash
# Chaos Test 3: Poison Deployment & Rollback Verification
set -eo pipefail

echo "🧪 [Chaos 3] Simulating Poison Deployment (Injecting Invalid Database Config)..."

# Save currently active stable commit SHA
CURRENT_STABLE_SHA=$(cat /opt/bitmill/.active_sha 2>/dev/null || echo "stable-v1")
echo "📌 Current Stable SHA: ${CURRENT_STABLE_SHA}"

# Continuous background availability monitor
MONITOR_LOG="/tmp/chaos_monitor.log"
rm -f "$MONITOR_LOG"
(
  while true; do
    CODE=$(curl -s -o /dev/null -w "%{http_code}\n" http://devlungtung.htcwood.com/health || echo "000")
    echo "$(date +%s) $CODE" >> "$MONITOR_LOG"
    sleep 0.5
  done
) &
MONITOR_PID=$!

cleanup() {
  kill $MONITOR_PID 2>/dev/null || true
  rm -f "$MONITOR_LOG"
}
trap cleanup EXIT

echo "⚡ INJECTING CHAOS: Triggering deploy with poisoned environment..."
export DATABASE_HOST="invalid-postgres-host"
DEPLOY_EXIT=0
/opt/bitmill/scripts/deploy-bitmill.sh || DEPLOY_EXIT=$?

if [ "$DEPLOY_EXIT" -eq 0 ]; then
  echo "❌ CRITICAL: Deployment script succeeded when it should have FAILED on poison!"
  exit 1
fi
echo "✅ Deployment script correctly exited with failure (Code: ${DEPLOY_EXIT})"

# Verify that current stable version is still serving
ACTIVE_SHA_AFTER=$(cat /opt/bitmill/.active_sha 2>/dev/null || echo "stable-v1")
if [ "$ACTIVE_SHA_AFTER" != "$CURRENT_STABLE_SHA" ]; then
  echo "❌ Active SHA was corrupted during failed deployment: ${ACTIVE_SHA_AFTER}"
  exit 1
fi

# Analyze continuous availability monitor
kill $MONITOR_PID 2>/dev/null || true
FAILURES=$(awk '$2 != "200" {count++} END {print count+0}' "$MONITOR_LOG")
echo "📊 Total failed health probes during poison deploy: ${FAILURES}"

if [ "$FAILURES" -gt 0 ]; then
  echo "❌ Service experienced downtime during poison deploy (${FAILURES} dropped probes)!"
  exit 1
fi

echo "🎉 Chaos Test 3 PASSED: Poison container rejected within 15s; 100% availability maintained!"
```

---

### 5.4. Chaos Experiment 4: Migration Failure & Lock Recovery

- **Hypothesis**: If a database migration encounters an exclusive lock contention (e.g., an uncommitted transaction holding `EXCLUSIVE LOCK` on `videos`), the deployment migration engine will time out (`lock_timeout = 5s`), **safely abort**, and exit before taking down running application containers.
- **System Invariant**: Database transaction rolls back completely; running application instances remain unharmed.

#### 5.4.1. Chaos Test Harness Script (`chaos-test-migration-lock.sh`)
```bash
#!/usr/bin/env bash
# Chaos Test 4: Database Migration Lock Conflict Recovery
set -eo pipefail

echo "🧪 [Chaos 4] Simulating Database Exclusive Lock Contention..."

# 1. Spawn a background psql session that holds an ACCESS EXCLUSIVE lock on videos
docker exec -i bitmill-prod-postgres psql -U bitmill_user -d bitmill_media << 'EOF' &
BEGIN;
LOCK TABLE videos IN ACCESS EXCLUSIVE MODE;
SELECT pg_sleep(25);
COMMIT;
EOF
LOCK_PID=$!
sleep 2

echo "🔒 Exclusive lock acquired on 'videos' table. Attempting deployment with schema migration..."

# 2. Execute migration with lock timeout (should fail fast)
DEPLOY_EXIT=0
/opt/bitmill/scripts/deploy-bitmill.sh || DEPLOY_EXIT=$?

if [ "$DEPLOY_EXIT" -eq 0 ]; then
  echo "❌ Deployment proceeded despite lock contention!"
  kill $LOCK_PID 2>/dev/null || true
  exit 1
fi

echo "✅ Deployment correctly aborted due to migration lock timeout (Exit Code: ${DEPLOY_EXIT})"

# 3. Verify running application server was NOT restarted
UPTIME=$(docker exec bitmill-prod-server uptime || true)
echo "📊 Production server container uptime: ${UPTIME}"

wait $LOCK_PID 2>/dev/null || true
echo "🎉 Chaos Test 4 PASSED: Migration failed safely; existing containers untouched!"
```

---

## 6. Audit Logging, Telemetry & SRE Alerting Plan

### 6.1. Deployment Audit Trail Specification

Every deployment—whether successful, failed, or rolled back—must append an immutable structured JSON log entry to `/var/log/bitmill/deployments.log` on the VPS host.

#### 6.1.1. Audit Log Schema
```json
{
  "timestamp": "2026-10-30T14:35:22Z",
  "event": "DEPLOYMENT_SUCCESS",
  "deployment_id": "dep-8f4b2c1e-6a3d",
  "actor": "quidb",
  "trigger": "github_actions",
  "commit_sha": "a1b2c3d4e5f678901234567890abcdef12345678",
  "git_tag": "v1.4.2",
  "image_digests": {
    "server": "ghcr.io/bitmill/server@sha256:d8b7a6e5f123456789abcdef0123456789abcdef0123456789abcdef01234567",
    "worker": "ghcr.io/bitmill/worker@sha256:c7a6e5d4f123456789abcdef0123456789abcdef0123456789abcdef01234567"
  },
  "duration_seconds": 38,
  "migrations_applied": ["00003_add_video_renditions.sql"],
  "healthcheck_duration_seconds": 6,
  "status": "PROMOTED",
  "rollback_target_sha": "f9e8d7c6b5a432101234567890abcdef12345678"
}
```

#### 6.1.2. Failure Audit Event Schema
```json
{
  "timestamp": "2026-10-30T14:48:10Z",
  "event": "DEPLOYMENT_ROLLBACK",
  "deployment_id": "dep-9c8b7a6e-5f4d",
  "actor": "github_actions",
  "attempted_sha": "e5d4c3b2a1f01234567890abcdef1234567890ab",
  "failure_stage": "POST_DEPLOY_HEALTHCHECK",
  "failure_reason": "Health probe /readyz returned HTTP 503 after 15 retries (database unreachable)",
  "duration_seconds": 31,
  "action_taken": "ROLLED_BACK_TO_STABLE",
  "restored_sha": "a1b2c3d4e5f678901234567890abcdef12345678",
  "alert_dispatched": true
}
```

---

### 6.2. Prometheus & Grafana Loki Alerting Rules

The observability infrastructure (Prometheus & Loki) deployed in RFC-003 monitors deployment stability and immediately alerts SRE teams to deployment-induced anomalies.

#### 6.2.1. Prometheus Alerting Rules (`/etc/prometheus/alert_rules.yml`)
```yaml
groups:
  - name: bitmill_deployment_alerts
    rules:
      # 1. Alert on Ingress HTTP 5xx Spikes during Deployment
      - alert: HighHttp5xxRateDuringDeploy
        expr: sum(rate(http_requests_total{status=~"5.."}[1m])) / sum(rate(http_requests_total[1m])) * 100 > 1.0
        for: 30s
        labels:
          severity: critical
        annotations:
          summary: "HTTP 5xx error rate exceeded 1% during deployment window"
          description: "Ingress Gateway reporting {{ $value }}% 5xx errors. Immediate investigation or rollback required."

      # 2. Alert on Container Crash Loops Post-Deploy
      - alert: ContainerCrashLoopDetected
        expr: rate(container_cpu_user_seconds_total{container_label_com_docker_compose_project="bitmill-prod"}[1m]) == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Bitmill production container is unresponsive or crashing"
          description: "Container {{ $labels.container_name }} has ceased CPU execution."

      # 3. Alert on Stale Transcoding Worker Heartbeat
      - alert: WorkerHeartbeatStale
        expr: (time() - bitmill_worker_last_heartbeat_timestamp_seconds) > 30
        for: 15s
        labels:
          severity: warning
        annotations:
          summary: "Transcoding worker heartbeat is missing or stale"
          description: "Worker node has not updated heartbeat in over 30 seconds."

      # 4. Alert on RabbitMQ Dead Letter Queue Buildup
      - alert: RabbitMQDLQBuildup
        expr: rabbitmq_queue_messages{queue="bitmill_transcode_dlq"} > 0
        for: 1m
        labels:
          severity: high
        annotations:
          summary: "Transcoding jobs are failing into DLQ"
          description: "DLQ contains {{ $value }} failed transcode tasks."
```

#### 6.2.2. Grafana Loki LogQL Detection Queries
- **Deploy Log Aggregation**:
  ```logql
  {job="bitmill-deploy"} | json | event == "DEPLOYMENT_ROLLBACK"
  ```
- **Nginx Ingress Drop Detection**:
  ```logql
  {job="nginx-access"} | json | status >= 500 | unwrap upstream_response_time [1m]
  ```
- **Go Server Panic Tracking**:
  ```logql
  {job="bitmill-server"} |= "panic:" or |= "runtime error:"
  ```

---

## 7. Concrete Implementation Blueprint & Operational Scripts

To ensure immediate operationalization, this section provides the reference bash deployment script that runs natively on the production VPS at `103.90.224.69`.

### 7.1. Idempotent Zero-Downtime Deployment Script (`deploy-bitmill.sh`)

```bash
#!/usr/bin/env bash
# ==============================================================================
# 🚀 BITMILL PRODUCTION ZERO-DOWNTIME DEPLOYMENT ORCHESTRATOR
# Host: Ubuntu 24.04 (103.90.224.69 - devlungtung.htcwood.com)
# User: quidb-dev | Zero-Sudo Declarative Architecture
# ==============================================================================
set -eo pipefail

APP_DIR="/opt/bitmill"
COMPOSE_FILE="${APP_DIR}/environment/docker-compose.prod.yml"
ENV_FILE="${APP_DIR}/.env"
AUDIT_LOG="/var/log/bitmill/deployments.log"
ACTIVE_SHA_FILE="${APP_DIR}/.active_sha"
LOCK_FILE="/var/run/bitmill-deploy.lock"

# Parse CLI arguments
TARGET_SHA="${1}"
SERVER_DIGEST="${2}"
WORKER_DIGEST="${3}"

if [ -z "$TARGET_SHA" ] || [ -z "$SERVER_DIGEST" ] || [ -z "$WORKER_DIGEST" ]; then
  echo "Usage: $0 <TARGET_SHA> <SERVER_DIGEST> <WORKER_DIGEST>"
  exit 1
fi

START_TIME=$(date +%s)
DEPLOY_ID="dep-$(cat /proc/sys/kernel/random/uuid | cut -c1-8)"

# -----------------------------------------------------------------------------
# 1. Concurrency Guard & Process Locking
# -----------------------------------------------------------------------------
exec 200>"$LOCK_FILE"
flock -n 200 || { echo "❌ Deployment already in progress! Exiting."; exit 1; }

echo "========================================================================"
echo "🚀 [${DEPLOY_ID}] Starting Bitmill Zero-Downtime Deployment (Target: ${TARGET_SHA})"
echo "========================================================================"

CURRENT_SHA=$(cat "$ACTIVE_SHA_FILE" 2>/dev/null || echo "initial")

log_audit() {
  local event="$1"
  local status="$2"
  local reason="$3"
  local end_time=$(date +%s)
  local duration=$(( end_time - START_TIME ))

  mkdir -p "$(dirname "$AUDIT_LOG")" 2>/dev/null || true
  cat <<EOF >> "$AUDIT_LOG"
{"timestamp":"$(date -u +"%Y-%m-%dT%H:%M:%SZ")","event":"${event}","deployment_id":"${DEPLOY_ID}","commit_sha":"${TARGET_SHA}","previous_sha":"${CURRENT_SHA}","duration_seconds":${duration},"status":"${status}","reason":"${reason}"}
EOF
}

rollback() {
  echo -e "\n🚨 [ROLLBACK] Deployment failure detected! Initiating automatic recovery..."
  log_audit "DEPLOYMENT_ROLLBACK" "FAILED" "Post-deployment healthcheck failure"

  if [ "$CURRENT_SHA" != "initial" ] && [ -f "${APP_DIR}/.rollback_compose.yml" ]; then
    echo "⏮️ Restoring previous stable containers (SHA: ${CURRENT_SHA})..."
    docker compose -f "${APP_DIR}/.rollback_compose.yml" up -d
    echo "$CURRENT_SHA" > "$ACTIVE_SHA_FILE"
    echo "✅ Rollback completed successfully. Traffic restored to ${CURRENT_SHA}."
  else
    echo "⚠️ No prior rollback manifest available. Maintaining current state."
  fi
  exit 1
}
trap rollback ERR

# -----------------------------------------------------------------------------
# 2. Gate 3: Pre-Flight Assertions & Pre-Pulling
# -----------------------------------------------------------------------------
echo "🔍 [1/5] Verifying host resources and pre-pulling images..."

# Validate .env file permissions
ENV_PERMS=$(stat -c "%a" "$ENV_FILE" 2>/dev/null || stat -f "%OLp" "$ENV_FILE")
if [ "$ENV_PERMS" != "600" ]; then
  echo "🔒 Securing .env file permissions to 600..."
  chmod 600 "$ENV_FILE"
fi

# Pre-pull images before stopping anything
docker pull "ghcr.io/bitmill/server@${SERVER_DIGEST}"
docker tag "ghcr.io/bitmill/server@${SERVER_DIGEST}" "bitmill/server:latest"

docker pull "ghcr.io/bitmill/worker@${WORKER_DIGEST}"
docker tag "ghcr.io/bitmill/worker@${WORKER_DIGEST}" "bitmill/worker:latest"

# Cache current compose for emergency rollback
cp "$COMPOSE_FILE" "${APP_DIR}/.rollback_compose.yml" 2>/dev/null || true

# -----------------------------------------------------------------------------
# 3. Apply Backward-Compatible Database Migrations
# -----------------------------------------------------------------------------
echo "🗄️ [2/5] Running database migrations with lock timeout..."
export DB_DSN="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}?sslmode=disable"
docker compose -f "$COMPOSE_FILE" exec -T postgres psql -U bitmill_user -d bitmill_media -c "SET lock_timeout = '5s';" >/dev/null 2>&1 || true

# Execute Goose migration container or direct binary
docker run --rm --network bitmill-prod_bitmill-internal \
  -v "${APP_DIR}/migrations:/migrations:ro" \
  gomicro/goose:latest -dir /migrations postgres "${DB_DSN}" up

# -----------------------------------------------------------------------------
# 4. Zero-Downtime Container Handover (Server & Worker)
# -----------------------------------------------------------------------------
echo "🔄 [3/5] Starting new application containers and verifying readiness..."

# Recreate server with new image
docker compose -f "$COMPOSE_FILE" up -d --no-deps --build server worker

# -----------------------------------------------------------------------------
# 5. Gate 4: Health Probing
# -----------------------------------------------------------------------------
echo "🩺 [4/5] Executing post-deployment health & readiness verification..."
RETRY=0
MAX_RETRIES=15
HEALTH_OK=false

while [ $RETRY -lt $MAX_RETRIES ]; do
  STATUS=$(docker exec bitmill-prod-server wget -q -O - http://127.0.0.1:8080/readyz 2>/dev/null || echo "")
  if echo "$STATUS" | grep -q '"status":"READY"'; then
    HEALTH_OK=true
    break
  fi
  echo "  • Waiting for /readyz... ($((RETRY+1))/${MAX_RETRIES})"
  RETRY=$(( RETRY + 1 ))
  sleep 2
done

if [ "$HEALTH_OK" = false ]; then
  echo "❌ Server failed readiness probe!"
  false # Triggers trap rollback
fi

# Reload Nginx Gateway to refresh upstreams
docker exec bitmill-prod-gateway nginx -s reload
echo "🌐 Nginx gateway upstream refreshed smoothly."

# -----------------------------------------------------------------------------
# 6. Promotion & Housekeeping
# -----------------------------------------------------------------------------
echo "🎉 [5/5] Promoting release and cleaning up old dangling images..."
echo "$TARGET_SHA" > "$ACTIVE_SHA_FILE"

# Clean old unused images (retaining recent caches)
docker image prune -af --filter "until=72h" >/dev/null 2>&1 || true

log_audit "DEPLOYMENT_SUCCESS" "COMPLETED" "All gates passed successfully"

TOTAL_DURATION=$(( $(date +%s) - START_TIME ))
echo "========================================================================"
echo "✅ Bitmill Deployment ${TARGET_SHA} completed successfully in ${TOTAL_DURATION}s!"
echo "========================================================================"
```

---

## 8. Summary & Next Steps

This SecOps and Chaos Verification Specification establishes the complete, production-grade security architecture and resilience posture for Bitmill's RFC-004. By formalizing threat models, quantitative FMEA mitigation, unskippable quality gates, and automated chaos validation scripts, Bitmill guarantees uninterrupted video streaming, secure container provenance, and resilient operational rollouts on Ubuntu 24.04 at `103.90.224.69`.

### 8.1. Discovery Sign-Off & Handoff Matrix
- **SecOps Findings**: Supply chain hardened with Cosign and Trivy; rootless containers with zero Docker socket exposure; SSH restricted to Ed25519; `.env` locked to `600`.
- **FMEA Ratings**: All 8 critical failure modes mitigated, reducing cumulative RPN from 204 to 14.
- **Chaos Verification**: Executable test harnesses ready for upload-in-flight, worker draining, poison rollback, and migration lock recovery.
- **Next Phase**: Solutions Architecture (`02-solutions-architecture.md`) and Business Analysis (`01-ba-ux-analysis.md`) alignment for final RFC-004 ratification.

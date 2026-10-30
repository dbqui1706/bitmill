#!/usr/bin/env bash
set -eo pipefail

CYAN='\033[36m'
GREEN='\033[32m'
RED='\033[31m'
YELLOW='\033[33m'
RESET='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

COMPOSE_FILE="environment/docker-compose.prod.yml"
UPSTREAM_CONF="environment/nginx/conf.d/00-upstream.conf"
LOCK_FILE="/tmp/bitmill-deploy.lock"
STATE_FILE="${ROOT_DIR}/.deploy_state"

REGISTRY_DEFAULT="ghcr.io/dbqui1706/bitmill"
REGISTRY="${REGISTRY:-$REGISTRY_DEFAULT}"
COMMIT_SHA=""
GHCR_TOKEN=""
DRAIN_TIMEOUT=30

usage() {
    echo "Usage: $0 --sha <commit_sha> [--token <ghcr_read_token>] [--registry <registry>]"
    exit 1
}

while [[ "$#" -gt 0 ]]; do
    case $1 in
        --sha) COMMIT_SHA="$2"; shift ;;
        --token) GHCR_TOKEN="$2"; shift ;;
        --registry) REGISTRY="$2"; shift ;;
        -h|--help) usage ;;
        *) echo "Unknown parameter: $1"; usage ;;
    esac
    shift
done

if [ -z "$COMMIT_SHA" ]; then
    echo -e "${RED}Error: --sha <commit_sha> is required.${RESET}"
    usage
fi

echo -e "${CYAN}======================================================================${RESET}"
echo -e "${CYAN}🚀 BITMILL ZERO-DOWNTIME PRODUCTION DEPLOYMENT ENGINE (RFC-004)${RESET}"
echo -e "${CYAN}======================================================================${RESET}"
echo -e "Target Commit SHA : ${GREEN}${COMMIT_SHA}${RESET}"
echo -e "Container Registry: ${GREEN}${REGISTRY}${RESET}"
echo -e "Host Directory    : ${GREEN}${ROOT_DIR}${RESET}"

# -----------------------------------------------------------------------------
# STEP 1: Concurrency Lock Acquisition
# -----------------------------------------------------------------------------
echo -e "\n${CYAN}[1/8] Acquiring Deployment Lock...${RESET}"
if [ -f "$LOCK_FILE" ]; then
    LOCK_PID=$(cat "$LOCK_FILE" 2>/dev/null || true)
    if [ -n "$LOCK_PID" ] && kill -0 "$LOCK_PID" 2>/dev/null; then
        echo -e "${RED}❌ Another deployment process is currently running (PID: ${LOCK_PID}). Aborting.${RESET}"
        exit 1
    fi
fi
echo "$$" > "$LOCK_FILE"
cleanup_lock() {
    rm -f "$LOCK_FILE"
}
trap cleanup_lock EXIT INT TERM

# -----------------------------------------------------------------------------
# STEP 2: GHCR Authentication & Pre-Flight Image Pull Assertion
# -----------------------------------------------------------------------------
echo -e "\n${CYAN}[2/8] Authenticating & Pulling OCI Artifacts from GHCR...${RESET}"
if [ -n "$GHCR_TOKEN" ]; then
    echo "$GHCR_TOKEN" | docker login ghcr.io -u "quidb-deploy" --password-stdin >/dev/null 2>&1 || {
        echo -e "${YELLOW}⚠️ Notice: Automated docker login failed; continuing with host credentials...${RESET}"
    }
fi

SERVER_IMAGE="${REGISTRY}/server:${COMMIT_SHA}"
WORKER_IMAGE="${REGISTRY}/worker:${COMMIT_SHA}"

echo -e "  • Pulling Server Image: ${GREEN}${SERVER_IMAGE}${RESET}"
if ! docker pull "${SERVER_IMAGE}"; then
    echo -e "${RED}❌ Failed to pull Server image from GHCR. Aborting deployment prior to altering stack.${RESET}"
    exit 1
fi

echo -e "  • Pulling Worker Image: ${GREEN}${WORKER_IMAGE}${RESET}"
if ! docker pull "${WORKER_IMAGE}"; then
    echo -e "${RED}❌ Failed to pull Worker image from GHCR. Aborting deployment prior to altering stack.${RESET}"
    exit 1
fi

# -----------------------------------------------------------------------------
# STEP 3: Automated Database Migrations via Ephemeral Goose Container
# -----------------------------------------------------------------------------
echo -e "\n${CYAN}[3/8] Executing Backward-Compatible Database Migrations (Goose)...${RESET}"
if docker compose --env-file .env -f "${COMPOSE_FILE}" run --rm migrator; then
    echo -e "  • Database Migrations: ${GREEN}PASS (All pending migrations applied successfully)${RESET}"
else
    echo -e "${RED}❌ Database migration failed! Aborting deployment to preserve current container runtime.${RESET}"
    exit 1
fi

# -----------------------------------------------------------------------------
# STEP 4: Detect Active & Standby Server Slots
# -----------------------------------------------------------------------------
echo -e "\n${CYAN}[4/8] Determining Dual-Slot Server Topology...${RESET}"
RUNNING_SERVICES=$(docker compose --env-file .env -f "${COMPOSE_FILE}" ps --services --filter "status=running" 2>/dev/null || true)

if echo "$RUNNING_SERVICES" | grep -q "server_green"; then
    ACTIVE_SLOT="green"
    ACTIVE_SERVICE="server_green"
    STANDBY_SLOT="blue"
    STANDBY_SERVICE="server_blue"
else
    ACTIVE_SLOT="blue"
    ACTIVE_SERVICE="server_blue"
    STANDBY_SLOT="green"
    STANDBY_SERVICE="server_green"
fi
echo -e "  • Currently Active Slot : ${GREEN}${ACTIVE_SLOT}${RESET} (${ACTIVE_SERVICE})"
echo -e "  • Target Standby Slot   : ${CYAN}${STANDBY_SLOT}${RESET} (${STANDBY_SERVICE})"

# -----------------------------------------------------------------------------
# STEP 5: Boot Standby Slot & Synthetic Readiness Healthcheck
# -----------------------------------------------------------------------------
echo -e "\n${CYAN}[5/8] Booting Standby Slot with Commit SHA ${COMMIT_SHA}...${RESET}"
SERVER_IMAGE="${SERVER_IMAGE}" docker compose --env-file .env -f "${COMPOSE_FILE}" up -d "${STANDBY_SERVICE}"

echo -e "  • Awaiting Standby Container Readiness Probe (/readyz)..."
PROBE_PASSED=false
for i in {1..15}; do
    CONTAINER_NAME="bitmill-prod-${STANDBY_SERVICE//_/-}"
    PROBE_STATUS=$(docker exec "${CONTAINER_NAME}" wget -q -O - http://127.0.0.1:8080/readyz 2>/dev/null || echo "FAILED")
    if echo "$PROBE_STATUS" | grep -q '"status":"READY"'; then
        PROBE_PASSED=true
        echo -e "  • Standby Slot Healthy: ${GREEN}PASS (HTTP 200, status=READY on attempt ${i})${RESET}"
        break
    fi
    sleep 2
done

if [ "$PROBE_PASSED" != "true" ]; then
    echo -e "${RED}❌ Standby Slot failed readiness probe! Aborting cutover.${RESET}"
    echo -e "${YELLOW}🧹 Terminating broken standby container...${RESET}"
    docker compose --env-file .env -f "${COMPOSE_FILE}" stop "${STANDBY_SERVICE}" || true
    exit 1
fi

# -----------------------------------------------------------------------------
# STEP 6: Atomic Traffic Cutover via Dynamic Nginx Upstream Reload
# -----------------------------------------------------------------------------
echo -e "\n${CYAN}[6/8] Executing Atomic Nginx Traffic Cutover...${RESET}"
cat <<EOF > "${UPSTREAM_CONF}"
# Dynamic Upstream Pool for Bitmill API Server (managed by deploy-prod.sh)
upstream bitmill_api {
    server ${STANDBY_SERVICE}:8080 max_fails=3 fail_timeout=10s;
    keepalive 32;
}
EOF

# Ensure Gateway container is running
if ! docker ps --format '{{.Names}}' | grep -q 'bitmill-prod-gateway'; then
    echo -e "  • Gateway not running; starting Gateway stack..."
    docker compose --env-file .env -f "${COMPOSE_FILE}" up -d gateway
fi

echo -e "  • Reloading Nginx master configuration..."
docker compose --env-file .env -f "${COMPOSE_FILE}" exec -T gateway nginx -s reload
echo -e "  • Nginx Gateway Cutover: ${GREEN}COMPLETED (Traffic now actively flowing to ${STANDBY_SLOT})${RESET}"

# -----------------------------------------------------------------------------
# STEP 7: Graceful Drain Old Slot & Update Transcoding Worker Fleet
# -----------------------------------------------------------------------------
echo -e "\n${CYAN}[7/8] Draining Active Connections & Updating Worker Fleet...${RESET}"
echo -e "  • Allowing ${DRAIN_TIMEOUT}s for in-flight HTTP chunk uploads on ${ACTIVE_SLOT} to complete..."
sleep "${DRAIN_TIMEOUT}"

echo -e "  • Stopping drained ${ACTIVE_SLOT} instance (${ACTIVE_SERVICE})..."
docker compose --env-file .env -f "${COMPOSE_FILE}" stop "${ACTIVE_SERVICE}"

echo -e "  • Updating Transcoding Worker to commit SHA ${COMMIT_SHA}..."
WORKER_IMAGE="${WORKER_IMAGE}" docker compose --env-file .env -f "${COMPOSE_FILE}" up -d worker

# -----------------------------------------------------------------------------
# STEP 8: Post-Deployment Smoke Verification & Instant Auto-Rollback Engine
# -----------------------------------------------------------------------------
echo -e "\n${CYAN}[8/8] Executing Synthetic Post-Deployment Smoke Verification...${RESET}"
if bash "${SCRIPT_DIR}/smoke-test.sh" "http://127.0.0.1"; then
    echo -e "\n${GREEN}======================================================================${RESET}"
    echo -e "${GREEN}🎉 ZERO-DOWNTIME DEPLOYMENT COMPLETED SUCCESSFULLY${RESET}"
    echo -e "${GREEN}======================================================================${RESET}"
    echo "ACTIVE_SLOT=${STANDBY_SLOT}" > "${STATE_FILE}"
    echo "ACTIVE_SERVICE=${STANDBY_SERVICE}" >> "${STATE_FILE}"
    echo "LAST_STABLE_SHA=${COMMIT_SHA}" >> "${STATE_FILE}"
    echo "DEPLOYED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "${STATE_FILE}"
    exit 0
else
    echo -e "\n${RED}======================================================================${RESET}"
    echo -e "${RED}⚠️ SMOKE VERIFICATION FAILED! INITIATING AUTOMATIC ROLLBACK...${RESET}"
    echo -e "${RED}======================================================================${RESET}"
    
    echo -e "  • Re-activating previous stable slot (${ACTIVE_SERVICE})..."
    docker compose --env-file .env -f "${COMPOSE_FILE}" start "${ACTIVE_SERVICE}"
    
    cat <<EOF > "${UPSTREAM_CONF}"
# Rolled back to previous stable slot
upstream bitmill_api {
    server ${ACTIVE_SERVICE}:8080 max_fails=3 fail_timeout=10s;
    keepalive 32;
}
EOF
    docker compose --env-file .env -f "${COMPOSE_FILE}" exec -T gateway nginx -s reload
    echo -e "  • Nginx traffic redirected back to ${ACTIVE_SLOT}."
    
    echo -e "  • Stopping faulty standby slot (${STANDBY_SERVICE})..."
    docker compose --env-file .env -f "${COMPOSE_FILE}" stop "${STANDBY_SERVICE}" || true
    
    echo -e "${RED}❌ Rollback completed. Production remained online on ${ACTIVE_SLOT}.${RESET}"
    exit 1
fi

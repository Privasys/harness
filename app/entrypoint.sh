#!/bin/bash
set -euo pipefail

# Privasys Harness entrypoint. The Go proxy owns BOTH network edges:
#   - egress on loopback :9411  (dsh plugins -> attested peers: CAI, tools)
#   - ingress on $PORT          (platform -> dsh web)
# Fronting $PORT lets the platform health check pass from second one while
# dsh (heavy, ~40s boot) comes up on an internal loopback port behind it —
# the confidential-ai pattern (Go front on $PORT, backend behind).
#
# Env (platform-injected): PORT, PRIVASYS_MANAGER_URL, PRIVASYS_CONTAINER_NAME,
# PRIVASYS_CONTAINER_TOKEN, PRIVASYS_IMAGE_DIGEST.
# Env (image-baked topology): HARNESS_MODEL_HOST, HARNESS_TOOL_HOSTS.
# Env (optional): HARNESS_PUBLIC_HOST (browser-trust fence authority),
# PRIVASYS_BEARER (dev-only model auth; on-platform uses the attested cert).

# Send this script's stdout to stderr for the rest of the run, and dsh's with
# it. The platform's container logger captures STDERR only, so everything on
# stdout — the entrypoint's own progress echoes, the boot-smoke verdict, and
# every line dsh itself prints — was invisible in `admin/enclave/container-logs`
# while the Go proxy's log output (stderr by default) came through. That left
# the enclave debuggable only for faults the proxy happened to see, which cost
# a full investigation cycle on 2026-09-05. Ordering matters: redirect before
# the first echo so nothing is lost.
exec 1>&2

if [[ -z "${PORT:-}" ]]; then
  echo "[harness] ERROR: PORT is required"
  exit 1
fi

# --- environment by app identity -------------------------------------------
# ONE measured image serves both control planes. The launcher injects
# PRIVASYS_APP_ID; this baked (measured) map selects the public host and the
# control-plane bases for the browser shell — no per-deployment config input.
case "${PRIVASYS_APP_ID:-}" in
  be129fce-28d7-40bf-85d0-44a94a78ed43)  # harness (production)
    export HARNESS_PUBLIC_HOST="harness.apps.privasys.org"
    PV_ATTEST_BASE="https://api.developer.privasys.org"
    PV_API_BASE="https://api.privasys.org"
    ;;
  *)                                     # attested-harness (dev, and the default)
    export HARNESS_PUBLIC_HOST="${HARNESS_PUBLIC_HOST:-attested-harness.apps.test.privasys.org}"
    PV_ATTEST_BASE="https://api.developer.test.privasys.org"
    PV_API_BASE="https://api-test.privasys.org"
    ;;
esac
export HARNESS_APP_ID="${PRIVASYS_APP_ID:-590ebdc3-1b63-401f-bbb8-22d5f3886c5e}"
# Hand the environment to the browser shell: privasys-shell.js merges
# window.__PRIVASYS_CFG__ over its dev defaults (its documented seam).
DIST_INDEX=/dsh/apps/web/dist/index.html
if [[ ! -f "$DIST_INDEX" ]]; then
  echo "[harness] WARNING: ${DIST_INDEX} not found — browser shell keeps dev defaults" >&2
elif ! grep -q "__PRIVASYS_CFG__" "$DIST_INDEX"; then
  CFG_TAG="<script>window.__PRIVASYS_CFG__={appId:\"${HARNESS_APP_ID}\",appHost:\"${HARNESS_PUBLIC_HOST}\",attestBase:\"${PV_ATTEST_BASE}\",apiBase:\"${PV_API_BASE}\"};</script>"
  sed -i "s|<head>|<head>${CFG_TAG}|" "$DIST_INDEX"
  grep -q "__PRIVASYS_CFG__" "$DIST_INDEX" || echo "[harness] WARNING: __PRIVASYS_CFG__ injection failed" >&2
fi

mkdir -p "${DSH_HOME:-/dsh-home}"

DSH_PORT=3080
EGRESS_PROXY_LISTEN=127.0.0.1:9411 \
EGRESS_FORWARD_LISTEN=127.0.0.1:9412 \
INGRESS_LISTEN="0.0.0.0:${PORT}" \
DSH_UPSTREAM="http://127.0.0.1:${DSH_PORT}" \
  /usr/local/bin/egress-proxy &
PROXY_PID=$!
for i in $(seq 1 50); do
  curl -sf --max-time 2 http://127.0.0.1:9411/healthz >/dev/null 2>&1 && break
  kill -0 "$PROXY_PID" 2>/dev/null || { echo "[harness] egress-proxy died at startup" >&2; exit 1; }
  sleep 0.2
done

# The model leg rides the proxy; the stock adapter reads these.
export DEEPSEEK_BASE_URL=http://127.0.0.1:9411/model/v1
export DEEPSEEK_API_KEY="${PRIVASYS_BEARER:-unset}"

# --- governed fast path for shell egress ------------------------------------
# Exported AFTER the health wait above so this script's own loopback curl is
# not affected. Everything the agent spawns inherits these: curl, git, npm,
# pip, and dsh's own fetches (it resolves the same names via
# @deepseek-ai/dsh-http-proxy). The forward listener polices each host against
# HARNESS_EGRESS_MODE and logs every verdict — see proxy/forward.go for why we
# interpose rather than prohibit.
#
# NO_PROXY must cover loopback (the model leg, the tool shims, dsh's own web
# server) AND the enclave manager, which lives on the GATEWAY IP rather than
# loopback. The Go side sets Proxy:nil on the manager client as well; this is
# the belt to that braces, and it also covers any child process that talks to
# the manager.
MGR_HOST="$(sed -E 's#^[a-z]+://([^/:]+).*#\1#' <<<"${PRIVASYS_MANAGER_URL:-}")"
export HTTP_PROXY="http://127.0.0.1:9412"
export HTTPS_PROXY="${HTTP_PROXY}"
export NO_PROXY="localhost,127.0.0.1,::1,[::1]${MGR_HOST:+,${MGR_HOST}}"
# Lowercase too: undici reads the lowercase name first, so both casings must
# always be written together or a client sees a half-configured policy.
export http_proxy="${HTTP_PROXY}"
export https_proxy="${HTTPS_PROXY}"
export no_proxy="${NO_PROXY}"

# --- sandbox backend diagnostic ---------------------------------------------
# dsh probes its Linux sandbox chain FUNCTIONALLY: it runs the real bwrap
# profile (--ro-bind / / --dev /dev --unshare-pid --proc /proc
# --die-with-parent -- true) and takes exit 0 as usable, then falls to the
# landlock-run node addon. When BOTH rungs fail the agent gets the opaque
# "no sandbox backend is usable on this host" on every Bash call, which names
# neither rung nor the reason. Installing bubblewrap was necessary and, as of
# v1.0.1, not sufficient — so print the facts that distinguish the causes
# (binary missing / user namespaces denied / no capabilities / landlock absent)
# rather than inferring them across build-and-deploy cycles.
{
  set +e
  echo "[harness] sandbox: kernel=$(uname -r) uid=$(id -u) gid=$(id -g)"
  if command -v bwrap >/dev/null 2>&1; then
    echo "[harness] sandbox: bwrap present ($(bwrap --version 2>&1 | head -1))"
    BW_ERR=$(bwrap --ro-bind / / --dev /dev --unshare-pid --proc /proc --die-with-parent -- true 2>&1)
    echo "[harness] sandbox: bwrap STOCK profile exit=$? err=${BW_ERR:0:200}"
    # The patched profile (overlay 2e1) and two reductions, so a failure still
    # says WHICH namespace the kernel refused rather than only that one did.
    BW_U=$(bwrap --unshare-user --ro-bind / / --dev /dev --unshare-pid --proc /proc --die-with-parent -- true 2>&1)
    echo "[harness] sandbox: bwrap +unshare-user exit=$? err=${BW_U:0:200}"
    BW_P=$(bwrap --ro-bind / / --unshare-pid -- true 2>&1)
    echo "[harness] sandbox: bwrap pid-ns only exit=$? err=${BW_P:0:200}"
    BW_N=$(bwrap --ro-bind / / -- true 2>&1)
    echo "[harness] sandbox: bwrap no-ns exit=$? err=${BW_N:0:200}"
  else
    echo "[harness] sandbox: bwrap MISSING from the image"
  fi
  echo "[harness] sandbox: max_user_namespaces=$(cat /proc/sys/user/max_user_namespaces 2>/dev/null || echo n/a)"
  UNS_ERR=$(unshare -U true 2>&1)
  echo "[harness] sandbox: 'unshare -U' exit=$? err=${UNS_ERR:0:200}"
  echo "[harness] sandbox: CapEff=$(awk '/CapEff/{print $2}' /proc/self/status 2>/dev/null)"
  echo "[harness] sandbox: securityfs=$(ls /sys/kernel/security/ 2>/dev/null | tr '\n' ' ')"
  LLBIN=$(find /dsh -path '*landlock-run*' -type f 2>/dev/null | head -3 | tr '\n' ' ')
  echo "[harness] sandbox: landlock-run artefacts=${LLBIN:-NONE}"
  set -e
} || true

# Boot smoke: one headless agent turn through the proxy to Confidential AI,
# proving the model leg works IN THE ENCLAVE (on-platform: attested client
# cert, no bearer). Bounded and non-fatal — logs PASS/FAIL and never blocks
# the web server. Skipped when HARNESS_SKIP_BOOT_SMOKE is set.
if [[ -z "${HARNESS_SKIP_BOOT_SMOKE:-}" ]]; then
  echo "[harness] boot smoke: headless model turn -> ${HARNESS_MODEL_HOST}"
  SMOKE=$(timeout 240 node /dsh/apps/cli/lib/bin.js --profile headless \
    --patch /app/profile.cordis.yml \
    "Reply with exactly: ONPLATFORM MODEL OK. Do not use any tools." 2>&1 | tail -3 || true)
  if grep -q "ONPLATFORM MODEL OK" <<<"$SMOKE"; then
    echo "[harness] boot smoke PASS: on-platform model leg attested + serving"
  else
    echo "[harness] boot smoke FAIL (non-fatal): ${SMOKE}"
  fi
fi

TRUST=()
if [[ -n "${HARNESS_PUBLIC_HOST:-}" ]]; then
  TRUST=(--trusted-host "${HARNESS_PUBLIC_HOST}")
fi

# Run the COMPILED dsh (lib/bin.js), NOT `pnpm dsh` (which is
# `node --import tsx/esm src/bin.ts` — on-the-fly TS transpile of the whole
# tree, minutes-slow under the enclave's constrained CPU and the cause of the
# boot never finishing before the health check). dsh binds the internal
# loopback port (overlay webserver row 127.0.0.1:$DSH_PORT); the proxy fronts
# $PORT. `--profile web --patch` (the `web` alias rejects parent flags).
# Sessions default their workspace to the process cwd. Running from /dsh (the
# WORKDIR) made every session a workspace INSIDE the dsh checkout — dsh's own
# AGENTS.md got injected as workspace instructions and users worked in the
# harness source tree. Work belongs on the ENCRYPTED VOLUME: a persistent
# workspace directory that survives redeploys.
mkdir -p /data/workspace
# The deployment-owned skill root (presets pin skill discovery to it,
# includeDefaultRoots:false — see app/profile notes + the preset overlay).
mkdir -p /data/skills
cd /data/workspace

echo "[harness] dsh web (compiled) on 127.0.0.1:${DSH_PORT}, proxy fronts 0.0.0.0:${PORT} (pid ${PROXY_PID}, trusted-host ${HARNESS_PUBLIC_HOST:-none})"
exec node /dsh/apps/cli/lib/bin.js --profile web --patch /app/profile.cordis.yml \
  -- --no-open "${TRUST[@]}"

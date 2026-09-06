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

# --- shell + egress smoke ----------------------------------------------------
# Two deterministic checks for the path the Bash tool depends on. Both were
# broken in prod on 2026-09-06 and NO existing check noticed: the model-leg
# smoke passed throughout, because it never runs a command and never leaves
# the enclave except to Confidential AI.
#
# 1. Sandbox. dsh probes bwrap FUNCTIONALLY with the profile in
#    packages/sandbox/sandbox-local/src/profiles.ts, and reports only "no
#    sandbox backend is usable on this host" when it fails — naming neither the
#    rung nor the cause. This container is uid 0 WITHOUT CAP_SYS_ADMIN and its
#    /proc is masked, so the stock profile cannot create a namespace and cannot
#    mount procfs; overlay 2e1 patches it to --unshare-user with no PID
#    namespace and no --proc. Run the patched profile here so a regression
#    (an overlay rebase, a runtime capability change) is named at boot instead
#    of surfacing as a failed tool call in front of a user.
# 2. Egress. The shell reaches the network only through the forward proxy
#    (HTTP_PROXY, policed by HARNESS_EGRESS_MODE). Prove a real request
#    completes end to end, against our own site rather than a third party.
{
  set +e
  if command -v bwrap >/dev/null 2>&1; then
    bwrap --unshare-user --ro-bind / / --dev /dev --die-with-parent -- true >/dev/null 2>&1
    RO=$?
    bwrap --unshare-user --ro-bind / / --dev /dev --die-with-parent --tmpfs /tmp \
      --bind /data/workspace /data/workspace -- true >/dev/null 2>&1
    RW=$?
    if [[ $RO -eq 0 && $RW -eq 0 ]]; then
      echo "[harness] shell smoke PASS: bwrap sandbox usable (read-only + workspace-write)"
    else
      echo "[harness] shell smoke FAIL: bwrap read-only=${RO} workspace-write=${RW}" \
        "— the Bash tool will refuse every call (uid=$(id -u) CapEff=$(awk '/CapEff/{print $2}' /proc/self/status 2>/dev/null))"
    fi
  else
    echo "[harness] shell smoke FAIL: bwrap MISSING — the Bash tool will refuse every call"
  fi
  case "${HARNESS_EGRESS_MODE:-}" in
    open|allowlist)
      EG=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 -I https://privasys.org 2>&1)
      if [[ "$EG" =~ ^[23] ]]; then
        echo "[harness] shell smoke PASS: egress via forward proxy (mode=${HARNESS_EGRESS_MODE}, HTTP ${EG})"
      else
        echo "[harness] shell smoke FAIL: egress via forward proxy returned '${EG}' (mode=${HARNESS_EGRESS_MODE})"
      fi
      ;;
    *)
      echo "[harness] shell smoke: egress check skipped (mode=${HARNESS_EGRESS_MODE:-unset} admits no direct fetch)"
      ;;
  esac
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

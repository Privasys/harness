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
    # STORAGE goes to the DEV Drive, not production. The image's baked tool
    # topology points at the prod fleet, which is right for the read-only tools
    # (one Brave, one Lightpanda, and dev has no copy of either) but wrong the
    # moment the harness starts WRITING a user's sessions: a dev harness must
    # not create folders and store transcripts in someone's production Drive.
    # The user was also, reasonably, looking at drive.test.privasys.org and
    # wondering where their sessions had gone.
    export HARNESS_TOOL_HOSTS="${HARNESS_TOOL_HOSTS/drive=privasys-drive.apps.privasys.org/drive=drive-demo.apps.test.privasys.org}"
    # Which Drive brokers the storage CONSENT is the runtime's decision
    # (its StorageResourceApp: the fleet's Drive, the dev id on the test
    # control plane), not this script's — the harness no longer names it.
    ;;
esac
export HARNESS_APP_ID="${PRIVASYS_APP_ID:-590ebdc3-1b63-401f-bbb8-22d5f3886c5e}"

# HOME is the workspace, because HOME is what the directory picker offers.
#
# dsh's browse picker starts every new session at homedir()
# (packages/host/directory-picker-browse) and takes no configuration for it, so
# in this container a new session landed in /root — the container's writable
# layer. Anything the agent wrote there was destroyed by the next redeploy,
# exactly like the session logs were, and just as silently: the workspace
# simply came back empty and looked new.
#
# Node's homedir() reads $HOME on POSIX, so pointing HOME at the encrypted
# volume fixes the picker without patching dsh. DSH_HOME is set explicitly and
# takes precedence over the home-derived default (home-paths resolves
# configured ?? env ?? defaultDshHome()), so the measured profiles do not move.
# The workspace is user data too, so it does not live on this enclave either.
# Set below, once the tmpfs is resolved.

# --- the session root must be MEMORY, never this enclave's disk -------------
# The harness holds no durable user data. /data is encrypted and per-enclave,
# but it is still the operator's enclave keeping the user's transcripts, and
# D6' puts the durable home on the user's own Drive. So sessions live on a
# tmpfs for the life of the container and are mirrored to Drive continuously.
#
# We cannot CREATE one: this container is uid 0 without CAP_SYS_ADMIN, so
# mount(2) is refused (the same restriction that shaped the bwrap profile).
# Pick an existing tmpfs instead, and if there is genuinely none, fail the boot
# rather than quietly writing conversations to disk - silently degrading the
# data model is exactly the class of bug this change is correcting.
SESSION_ROOT=""
for cand in /dev/shm /run /tmp; do
  [[ -d "$cand" ]] || continue
  if [[ "$(stat -f -c %T "$cand" 2>/dev/null)" == "tmpfs" ]]; then
    SESSION_ROOT="${cand}/privasys-sessions"
    SESSION_ROOT_KB="$(df -k --output=size "$cand" 2>/dev/null | tail -1 | tr -d ' ')"
    echo "[harness] session root: ${SESSION_ROOT} (tmpfs on ${cand}, ${SESSION_ROOT_KB:-?} KiB)"
    break
  fi
done
if [[ -z "$SESSION_ROOT" ]]; then
  echo "[harness] ERROR: no tmpfs available for the session root. Refusing to start:"
  echo "[harness]        writing conversations to this enclave's disk is the data model"
  echo "[harness]        this build exists to prevent. Checked /dev/shm, /run, /tmp."
  exit 1
fi
mkdir -p "$SESSION_ROOT"
export HARNESS_SESSION_ROOT="$SESSION_ROOT"
# The workspace is the agent's files — user data by the same argument as the
# transcripts, and D6' puts it on the user's Drive too. HOME is what dsh's
# directory picker offers, so pointing it here is what keeps new sessions off
# the enclave's disk.
WORKSPACE_ROOT="${SESSION_ROOT%/*}/privasys-workspace"
mkdir -p "$WORKSPACE_ROOT"
export HARNESS_WORKSPACE_ROOT="$WORKSPACE_ROOT"
export HOME="$WORKSPACE_ROOT"
echo "[harness] workspace root: $WORKSPACE_ROOT (tmpfs, mirrored to Drive)"
# dsh reads its root from the composition, so hand the resolved path to the
# profile as a later patch (patches apply in order; this overrides the
# non-durable default in profile.cordis.yml).
printf -- '- id: session-persistence-jsonl
  name: "@deepseek-ai/dsh-session-persistence-jsonl"
  config:
    root: %s
'   "$SESSION_ROOT" > /run/session-root.cordis.yml
# A store from before this change is no longer read. Say so rather than
# leaving the user to wonder where their history went, and do not delete it:
# it is their data, and deleting it is their call, not this script's.
for legacy in /data/sessions /data/workspace; do
  if [[ -d "$legacy" ]] && [[ -n "$(ls -A "$legacy" 2>/dev/null)" ]]; then
    echo "[harness] NOTE: $legacy holds a legacy on-enclave store and is no longer read."
    echo "[harness]       Sessions and workspace now live in memory and on your Drive."
    echo "[harness]       It is your data, so this script will not delete it — clear it when ready."
  fi
done
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
# One dsh per signed-in user (WS5). With HARNESS_WORKERS=1 the proxy
# supervises dsh itself: a worker per subject with its own uid, home,
# session root and bearer, plus a system worker for the public shell. The
# legacy single-user layout (this script exec'ing one dsh) stays reachable
# with HARNESS_WORKERS=0 for a deployment that must roll back.
export HARNESS_WORKERS="${HARNESS_WORKERS:-1}"
if [[ "$HARNESS_WORKERS" == "1" ]]; then
  # Volume layout for unprivileged worker uids, settled BEFORE the proxy
  # starts (it lays out the per-user trees itself, and a root it finds
  # already there keeps the mode it has). Workers must traverse /data to
  # reach their own 0700 tree and must read the deployment's skills; the
  # legacy single-user stores and the proxy's own state stay root-only.
  chmod 711 /data 2>/dev/null || true
  mkdir -p /data/users && chmod 711 /data/users
  for private in /data/sessions /data/workspace /data/policy; do
    [[ -d "$private" ]] && chmod 700 "$private"
  done
  mkdir -p /data/skills && chmod -R a+rX /data/skills
  echo "[harness] volume layout for workers: /data $(stat -c '%a uid=%u' /data), /data/users $(stat -c '%a' /data/users), /data/skills $(stat -c '%a' /data/skills)"
fi
EGRESS_PROXY_LISTEN=127.0.0.1:9411 \
EGRESS_FORWARD_LISTEN=127.0.0.1:9412 \
INGRESS_LISTEN="0.0.0.0:${PORT}" \
DSH_UPSTREAM="$([[ "$HARNESS_WORKERS" == "1" ]] || echo "http://127.0.0.1:${DSH_PORT}")" \
  /usr/local/bin/egress-proxy &
PROXY_PID=$!
for i in $(seq 1 50); do
  curl -sf --max-time 2 http://127.0.0.1:9411/healthz >/dev/null 2>&1 && break
  kill -0 "$PROXY_PID" 2>/dev/null || { echo "[harness] egress-proxy died at startup" >&2; exit 1; }
  sleep 0.2
done
# Hold dsh back until the holder's data is back on the tmpfs roots. dsh
# builds its workspace list ONCE at start and never re-bootstraps, so a
# session restored from the Drive after that start stays invisible until the
# next restart. The proxy restores for the subject it remembered from the last
# sign-in and reports /storage/ready; a slow or unreachable Drive is bounded
# here so a Drive outage delays boot by at most this window, never blocks it.
for i in $(seq 1 450); do
  curl -sf --max-time 2 http://127.0.0.1:9411/storage/ready >/dev/null 2>&1 && break
  kill -0 "$PROXY_PID" 2>/dev/null || { echo "[harness] egress-proxy died during restore" >&2; exit 1; }
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
      --bind "$WORKSPACE_ROOT" "$WORKSPACE_ROOT" -- true >/dev/null 2>&1
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
  # From the WORKSPACE, and with the smoke's own session root. Running from the
  # WORKDIR (/dsh) made the smoke's session a workspace INSIDE the harness
  # source tree, and writing to the real store left its prompt sitting in the
  # user's history on every boot — a fresh harness opened showing a "dsh"
  # workspace and somebody else's conversation. A fresh harness must look fresh.
  SMOKE=$(cd "$WORKSPACE_ROOT" && timeout 240 node /dsh/apps/cli/lib/bin.js --profile headless \
    --patch /app/profile.cordis.yml --patch /app/smoke.cordis.yml \
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
# harness source tree. Work now belongs on the tmpfs workspace root resolved
# above, mirrored to the user's Drive — the enclave keeps no copy.
# The deployment-owned skill root (presets pin skill discovery to it,
# includeDefaultRoots:false — see app/profile notes + the preset overlay).
mkdir -p /data/skills
cd "$WORKSPACE_ROOT"

if [[ "$HARNESS_WORKERS" == "1" ]]; then
  # The proxy runs one dsh per user; this script only keeps the container
  # alive and forwards a stop to it (its workers die with their process
  # groups). Per-user caches live under /data/users/<key> (laid out above,
  # before the proxy started); their owning uids are unprivileged and never
  # see another user's directory.
  trap 'kill -TERM "$PROXY_PID" 2>/dev/null' TERM INT
  echo "[harness] per-user dsh workers under the proxy (pid ${PROXY_PID}); proxy fronts 0.0.0.0:${PORT} (trusted-host ${HARNESS_PUBLIC_HOST:-none})"
  wait "$PROXY_PID"
  exit $?
fi

echo "[harness] dsh web (compiled) on 127.0.0.1:${DSH_PORT}, proxy fronts 0.0.0.0:${PORT} (pid ${PROXY_PID}, trusted-host ${HARNESS_PUBLIC_HOST:-none})"
exec node /dsh/apps/cli/lib/bin.js --profile web \
  --patch /app/profile.cordis.yml --patch /run/session-root.cordis.yml \
  -- --no-open "${TRUST[@]}"

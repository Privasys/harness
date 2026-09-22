# Privasys Harness — measured app image (WS3).
#
# Three stages: the egress proxy on upstream Go (RA-TLS v2 needs no patched
# TLS stack), the vendored dsh tree at the pin, and a
# node runtime that runs both under one entrypoint. The dsh source is
# vendored AT IMAGE BUILD from the public repo at the pinned commit — the
# composition (app/profile.cordis.yml) plus this file IS the measured
# identity of the harness (D-decisions: extend, don't fork).

# ---- egress proxy (attestation authority; never Node) ---------------------
FROM golang:1.26-bookworm AS proxy-builder
ARG RA_TLS_CLIENTS_REF=a5c458d7601eb88ff4eec357037a9421294d8619
RUN git clone https://github.com/Privasys/ra-tls-clients /build/attested-harness/ra-tls-clients \
 && git -C /build/attested-harness/ra-tls-clients checkout "${RA_TLS_CLIENTS_REF}"
COPY proxy /build/attested-harness/proxy
WORKDIR /build/attested-harness/proxy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
      -o /egress-proxy ./cmd/egress-proxy

# ---- dsh at the pin -------------------------------------------------------
FROM node:22-bookworm AS dsh-builder
# dsh-v0.1.7-alpha.1 (2026-09-22). Release audit: 1299 commits past
# 0.1.6-alpha.2, and two restructures under the overlay. (1) The agent
# presets left the preset package for the web-app bundle, where each is one
# 'dsh-agent-preset' declaration whose rows nest under config.plugins and
# which that bundle's package.json must list — the routine preset is written
# and listed there now. (2) The model plugin un-split (protocols/ gone) and
# speaks ONLY the Anthropic Messages wire: 'protocol' is no longer
# configurable, so profile.cordis.yml points at the proxy's /model/v1 and
# Confidential AI serves /v1/messages beside chat completions (CAI v0.8.7,
# handler messages.go). The reproducibility trailer moved with it, and the
# row menu became a slot, so Delete session is a registered entry.
ARG DSH_PIN=c36a83ff6bb95e3f82cf79f9be7c724270a8aa61
RUN corepack enable \
 && git clone https://github.com/deepseek-ai/deepseek-harness /dsh \
 && git -C /dsh checkout "${DSH_PIN}"
WORKDIR /dsh
RUN pnpm install --frozen-lockfile
# Apply the Privasys web overlay (D8 extend-don't-fork patch queue): the sealed
# transport carrier (privasys-api-client.ts), the gated boot (apps/web main.ts),
# the auth + attestation shell and its assets, and the removal of the WebSocket
# 426 fence so the event downlinks ride SSE. apply-overlay.mjs asserts every
# anchor and FAILS the build if upstream moved one — the signal to rebase, never
# a silent skip. No package.json is touched, so the frozen lockfile still holds.
COPY web /build/web
COPY app/assert-composition.sh /app/assert-composition.sh
# The attested tools this deployment mounts (space-separated names). Each is
# one MCP row in every agent preset (overlay 2c) pointing at the proxy's
# /tool/<name>/mcp; HARNESS_TOOL_HOSTS below says which attested app answers
# for it. The rows are baked here because presets compose their own tree,
# which no runtime patch reaches. The harness's own access server is always
# mounted. A deployment with a connector adds its name here and its host below.
ARG HARNESS_TOOLS="web_search web_reader drive mail calendar files meetings"
RUN HARNESS_TOOLS="${HARNESS_TOOLS}" node /build/web/apply-overlay.mjs /dsh
# Build the frontend dist (dsh-web-app refuses to load without it) and
# materialize the web profile so its plugin node_modules are baked into the
# image — an enclave has no egress for a boot-time install, and the profile
# is deterministic from the pin, so it belongs in the measured identity.
ENV DSH_HOME=/dsh-home
# Rebrand the dsh client chrome: the document/app title (read at vite build
# time) and the brand-slot occupants (Brand.tsx overlay) carry Privasys, not
# DeepSeek. DSH_CLIENT_BUILD_PROFILE=official keeps the brand slots filled (now
# with our overridden Privasys mark/name).
ENV DSH_CLIENT_TITLE="Privasys Harness"
ENV DSH_CLIENT_BUILD_PROFILE=official
# ALLOW-LIST COMPOSITION: the web + headless profiles are pre-written to use
# @privasys/harness-bundle (bundle/harness-bundle — a reviewed allow-list
# replacement for @deepseek-ai/dsh-base: attested egress only, no
# DeepSeek-cloud reporting, cache-safe compaction) instead of dsh-base. The
# bundle package is placed in $DSH_HOME/profiles/node_modules where dsh's
# two-anchor bundle resolution finds it (installation first, then the profile
# directory); initProfile keeps pre-existing manifests, normalizeShippedProfile
# leaves non-template bundle lists untouched, and the module-fallback heal only
# manages its own installation entries — the @privasys scope is never pruned.
COPY bundle/harness-bundle /tmp/harness-bundle
RUN mkdir -p /dsh-home/profiles/node_modules/@privasys /dsh-home/profiles/web /dsh-home/profiles/headless \
 && cp -r /tmp/harness-bundle /dsh-home/profiles/node_modules/@privasys/harness-bundle \
 && printf '%s\n' \
      '{' \
      '  "name": "dsh-profile-web",' \
      '  "private": true,' \
      '  "dependencies": {},' \
      '  "dsh": { "profile": { "bundles": ["@privasys/harness-bundle", "@deepseek-ai/dsh-web-app"], "patchReload": "live" } }' \
      '}' > /dsh-home/profiles/web/package.json \
 && printf '%s\n' \
      '{' \
      '  "name": "dsh-profile-headless",' \
      '  "private": true,' \
      '  "dependencies": {},' \
      '  "dsh": { "profile": { "bundles": ["@privasys/harness-bundle", "@deepseek-ai/dsh-headless"], "patchReload": "startup" } }' \
      '}' > /dsh-home/profiles/headless/package.json
# Build the frontend, then dump-config both profiles and assert what they
# composed (app/assert-composition.sh): the agent core is present, no row the
# bundle excludes is enabled, every bundle actually loaded, and the two
# `routine` presets an unattended run is dispatched under exist. A
# composition mistake fails the build here rather than in production, and
# each check names itself when it fails.
RUN pnpm run build \
 && { pnpm dsh --profile web --dump-config > /tmp/web-dump.yml 2>/tmp/web-dump.err \
      || { echo '--- the web profile did not compose:'; cat /tmp/web-dump.err; false; }; } \
 && { pnpm dsh --profile headless --dump-config > /tmp/headless-dump.yml 2>/tmp/headless-dump.err \
      || { echo '--- the headless profile did not compose:'; cat /tmp/headless-dump.err; false; }; } \
 && sh /app/assert-composition.sh /tmp/web-dump.yml /tmp/web-dump.err /tmp/headless-dump.yml /tmp/headless-dump.err \
 && rm -f /tmp/web-dump.yml /tmp/headless-dump.yml /tmp/web-dump.err /tmp/headless-dump.err && rm -rf /dsh/.git /tmp/harness-bundle \
 && find /dsh \( -name 'AGENTS.md' -o -name 'CLAUDE.md' -o -name 'AGENTS.local.md' -o -name 'CLAUDE.local.md' \) \( -type f -o -type l \) -delete \
 && rm -rf /dsh/docs /dsh/website /dsh/snapshots /dsh/.agents \
      /dsh/README.md /dsh/README.zh.md /dsh/BRAND_GUIDELINES.md /dsh/BRAND_GUIDELINES.zh.md \
      /dsh/SAFETY.md /dsh/SAFETY.zh.md /dsh/CONTRIBUTING.md /dsh/CONTRIBUTING.zh.md
# ^ Greenfield content sweep: every AGENTS.md/CLAUDE.md in the checkout would
#   inject as workspace instructions for any session whose workspace lands in
#   /dsh (including the persisted pre-/data/workspace ones) — the
#   agent-instructions feature stays ON for users' OWN projects, only dsh's
#   files go. .agents/ additionally held the dsh-* development skills (also
#   scoped out by config), snapshots/ is test data, and the marketing/docs
#   trees are DeepSeek-authored content an fs tool could surface. LICENSE and
#   THIRD_PARTY_NOTICES.md are deliberately KEPT (MIT attribution).

# ---- reference skills at a pin --------------------------------------------
# What the assistant DOES is a folder of Markdown, not code: these are the
# deployment's reference skills, copied once into each holder's Drive and
# theirs to edit from then on (proxy capability/sync.go). Pinned and cloned
# rather than vendored, so the public repo stays the one place they live, and
# so the image's identity commits to exactly this text.
FROM node:22-bookworm AS skills-builder
ARG SKILLS_PIN=007d38d364c7219e2c74ef2fa8289d3144068e91
RUN git clone https://github.com/Privasys/agent-skills /skills \
 && git -C /skills checkout "${SKILLS_PIN}" \
 && rm -rf /skills/.git

# ---- runtime --------------------------------------------------------------
FROM node:22-bookworm-slim
# corepack prepare pins pnpm INSIDE the image: an enclave has no free
# egress for a boot-time registry fetch.
RUN corepack enable && corepack prepare pnpm@11.7.0 --activate \
 && apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl bubblewrap && rm -rf /var/lib/apt/lists/*
# bubblewrap is dsh's sandbox backend on Linux. Without it dsh refuses to run
# ANY shell command — "sandbox mode workspace-write is requested but no sandbox
# backend is usable on this host; refusing to run the command unconfined" —
# which is what the agent hit on every Bash tool call. dsh's sandbox vocabulary
# is deliberately file-effects-only (packages/sandbox/sandbox: "Network and
# process visibility are outside this vocabulary"), so bwrap confines the
# filesystem and our forward proxy governs the network. Note the profile passes
# --unshare-pid but NOT --unshare-net, by design: we interpose on egress rather
# than removing it (see proxy/forward.go).
COPY --from=dsh-builder /dsh /dsh
COPY --from=dsh-builder /dsh-home /dsh-home
COPY --from=proxy-builder /egress-proxy /usr/local/bin/egress-proxy
COPY app/profile.cordis.yml /app/profile.cordis.yml
# The routines door (a dsh plugin the proxy composes per worker) lives INSIDE
# the CLI's tree so its bare imports of dsh packages resolve from there.
COPY app/privasys-routines.mjs /dsh/apps/cli/config/privasys/privasys-routines.mjs
COPY app/smoke.cordis.yml /app/smoke.cordis.yml
# The service ceiling, IN THE IMAGE and therefore in the measurement. The
# proxy also publishes its digest at OID 5.4.9, but baking it here gives the
# stronger property: the code hash already commits to this posture, so a
# verifier who checks the measurement has checked the policy too. An
# enterprise-owned harness sets its own ceiling on the volume instead (see
# policy.Store.LoadCeiling).
COPY app/ceiling.json /app/ceiling.json
# The reference skills, in the measurement: a verifier who checks the image
# has checked the behaviour this deployment offers, before any holder edits
# their own copy.
COPY --from=skills-builder /skills /app/skills
COPY app/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
# The measured web profile (plugins + frontend) is baked at /dsh-home. Only
# sessions/settings persist, on the encrypted volume — profile.cordis.yml roots
# the JSONL session store at /data/sessions (the stock bundle roots it at
# $DSH_HOME/sessions, i.e. the container layer, where a redeploy destroys it).
ENV DSH_HOME=/dsh-home
# Fixed attested topology — the harness always calls Confidential AI and the
# platform tool apps; the 6.1 DepSet + allowed_callers enforce the actual
# attestation regardless of hostname, so these belong in the measured image
# (override at deploy for a different fleet). Model auth on-platform is the
# attested client cert, not a bearer (see the proxy's onPlatform path).
# The app's stable public host (name.domain), used for dsh's --trusted-host and
# the ingress Director's Host pinning so dsh's /api DNS-rebinding fence accepts
# the browser's sealed same-origin requests. Stable across enclaves for this app.
ENV HARNESS_PUBLIC_HOST=attested-harness.apps.test.privasys.org
# Non-attested egress posture for the agent's shell tools: tee_only |
# allowlist | open | none (proxy/forward.go). `open` is the decided default
# for the hosted product — permission to reach the wider web, not a bypass:
# every connection still goes through the measured proxy, is policed by
# hostname and is logged. The attested legs (/model, /tool) are unaffected and
# keep their dependency-set gate. Override per deployment; an unrecognised
# value falls back to `none`, never to something more permissive.
ENV HARNESS_EGRESS_MODE=open
# ENV HARNESS_EGRESS_ALLOWLIST=  # comma-separated; "*.example.com" allowed
# No telemetry leaves the enclave: any non-empty value hard-disables dsh's
# telemetry row at profile composition (profile-boot resolveTelemetryPatch).
ENV DSH_TELEMETRY_DISABLED=1
ENV HARNESS_MODEL_HOST=confidential-ai.apps.privasys.org
# One host per tool named in HARNESS_TOOLS (name=host, comma-separated).
ENV HARNESS_TOOL_HOSTS=web_search=web-search-brave.apps.privasys.org,web_reader=web-browser-lightpanda.apps.privasys.org,drive=privasys-drive.apps.privasys.org,mail=mail-connector.apps.privasys.org,calendar=calendar-connector.apps.privasys.org,files=files-connector.apps.privasys.org,meetings=meetings-connector.apps.privasys.org
# Public browser-UI shell: these prefixes are the forked dsh SPA + Privasys
# auth/attestation shell (HTML/JS/CSS — public measured code, no user data).
# The enclave session-relay serves them in the CLEAR on the gateway-terminated
# leg so the page can load before a sealed session exists; the data plane
# (/api, /privasys/attestation over sealed) stays sealed. This label is
# measured (it rides the image config), so a verifier sees exactly which paths
# are served unsealed. enclave-os requires tdx runtime with the static-unsealed
# exemption (manager.go isStaticUnsealedPath).
LABEL org.privasys.static-unsealed-prefixes="/,/assets/,/privasys/,/plugins/,/favicon.svg,/manifest.webmanifest"
# The measured manifest. The harness exposes no tools of its own; it declares
# the user-owned RESOURCES it wants brokered by the enclave runtime (consent
# on the holder's device, per-app sealed binding key, wallet push). The
# control plane reads this label on every version and hands the declaration
# to the runtime at deploy; what a user is told the app wants is therefore
# attested. The first entry is the folder in the holder's Drive, which Drive
# places under AppData/<label>/ and where sessions, policy and skills live;
# its name must match HARNESS_STORAGE_RESOURCE (default "storage") in the
# proxy. The second is the mailbox the platform's mail connector serves: the
# hosted product offers it to everyone by default, and a fleet that has no
# connector simply has no resource service for the kind, so the runtime
# refuses the ask rather than showing the holder a screen it cannot honour.
# The calendar connector is declared the same way, as the third connector
# kind: one tool name, one host, one resource line, nothing else.
#
# A fork adds or removes entries here; the proxy reads the same text back
# from HARNESS_RESOURCES and builds one broker per entry (proxy
# resources.go), so the declaration is the only place a resource is named.
ARG HARNESS_RESOURCES='[{"kind":"storage.folder","name":"storage","label":"Harness","permissions":["read","write","delete"]},{"kind":"app_storage","name":"holders","label":"Your working files","permissions":["read","write"],"options":{"unattended":true}},{"kind":"mail.mailbox","name":"mailbox","label":"Mail Connector","permissions":["read","write"]},{"kind":"calendar.events","name":"calendar","label":"Calendar Connector","permissions":["read","write"]},{"kind":"files.cloud","name":"files","label":"Files Connector","permissions":["read","write"]},{"kind":"meeting.transcripts","name":"meetings","label":"Meetings Connector","permissions":["read"]}]'
ENV HARNESS_RESOURCES=${HARNESS_RESOURCES}
LABEL org.privasys.manifest="{\"tools\":[],\"resources\":${HARNESS_RESOURCES}}"
# Link the GHCR package to this repo so its Actions inherit write access
# (avoids a personal access token — the package is published by CI).
LABEL org.opencontainers.image.source="https://github.com/Privasys/attested-harness"
WORKDIR /dsh
ENTRYPOINT ["/app/entrypoint.sh"]

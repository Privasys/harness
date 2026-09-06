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
ARG RA_TLS_CLIENTS_REF=c6c63216dc5a0915569826e3ba2c1efdf44de6b0
RUN git clone https://github.com/Privasys/ra-tls-clients /build/attested-harness/ra-tls-clients \
 && git -C /build/attested-harness/ra-tls-clients checkout "${RA_TLS_CLIENTS_REF}"
COPY proxy /build/attested-harness/proxy
WORKDIR /build/attested-harness/proxy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
      -o /egress-proxy ./cmd/egress-proxy

# ---- dsh at the pin -------------------------------------------------------
FROM node:22-bookworm AS dsh-builder
ARG DSH_PIN=d347e703908d0406b7a7ef80e3a0e594d86b2215
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
RUN node /build/web/apply-overlay.mjs /dsh
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
# Build the frontend, then dump-config both profiles: this now doubles as the
# BUILD-TIME ALLOW-LIST ASSERTION — the composed tree must carry the agent
# core and must NOT carry any row the bundle excludes (a re-pin that slips a
# dropped row back in fails the build here, not in production).
RUN pnpm run build \
 && pnpm dsh --profile web --dump-config > /tmp/web-dump.yml 2>/dev/null \
 && pnpm dsh --profile headless --dump-config > /tmp/headless-dump.yml 2>/dev/null \
 && grep -q "agent-loop" /tmp/web-dump.yml \
 && grep -q "agent-loop" /tmp/headless-dump.yml \
 && ! grep -qE "web-search-deepseek|web-fetch-http|session-log-deepseek|plugin-package-inventory-deepseek|session-telemetry-otel|tool-result-pruner|dsh-llm-pi-ai" /tmp/web-dump.yml \
 && ! grep -qE "web-search-deepseek|web-fetch-http|session-log-deepseek|plugin-package-inventory-deepseek|session-telemetry-otel|tool-result-pruner|dsh-llm-pi-ai" /tmp/headless-dump.yml \
 && test -e /dsh-home/profiles/node_modules/@privasys/harness-bundle/cordis.patch.yml \
 && rm -f /tmp/web-dump.yml /tmp/headless-dump.yml && rm -rf /dsh/.git /tmp/harness-bundle \
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

# ---- runtime --------------------------------------------------------------
FROM node:22-bookworm-slim
# corepack prepare pins pnpm INSIDE the image: an enclave has no free
# egress for a boot-time registry fetch.
RUN corepack enable && corepack prepare pnpm@11.7.0 --activate \
 && apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl && rm -rf /var/lib/apt/lists/*
COPY --from=dsh-builder /dsh /dsh
COPY --from=dsh-builder /dsh-home /dsh-home
COPY --from=proxy-builder /egress-proxy /usr/local/bin/egress-proxy
COPY app/profile.cordis.yml /app/profile.cordis.yml
COPY app/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
# The measured web profile (plugins + frontend) is baked at /dsh-home. Only
# sessions/settings persist, on the encrypted volume — the overlay points
# session persistence at /data (see profile.cordis.yml).
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
# No telemetry leaves the enclave: any non-empty value hard-disables dsh's
# telemetry row at profile composition (profile-boot resolveTelemetryPatch).
ENV DSH_TELEMETRY_DISABLED=1
ENV HARNESS_MODEL_HOST=confidential-ai.apps.privasys.org
ENV HARNESS_TOOL_HOSTS=web_search=web-search-brave.apps.privasys.org,web_reader=web-browser-lightpanda.apps.privasys.org,drive=privasys-drive.apps.privasys.org
# Public browser-UI shell: these prefixes are the forked dsh SPA + Privasys
# auth/attestation shell (HTML/JS/CSS — public measured code, no user data).
# The enclave session-relay serves them in the CLEAR on the gateway-terminated
# leg so the page can load before a sealed session exists; the data plane
# (/api, /privasys/attestation over sealed) stays sealed. This label is
# measured (it rides the image config), so a verifier sees exactly which paths
# are served unsealed. enclave-os requires tdx runtime with the static-unsealed
# exemption (manager.go isStaticUnsealedPath).
LABEL org.privasys.static-unsealed-prefixes="/,/assets/,/privasys/,/plugins/,/favicon.svg,/manifest.webmanifest"
# Link the GHCR package to this repo so its Actions inherit write access
# (avoids a personal access token — the package is published by CI).
LABEL org.opencontainers.image.source="https://github.com/Privasys/attested-harness"
WORKDIR /dsh
ENTRYPOINT ["/app/entrypoint.sh"]

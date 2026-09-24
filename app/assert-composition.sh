#!/bin/sh
# The build-time composition assertion.
#
# The composed tree is part of this app's measured identity, so the build is
# where a composition mistake must be caught: a re-pin that slips an excluded
# row back in, a bundle the composer could not read, a preset an unattended
# run is dispatched under that stopped existing. Every check below names
# itself when it fails, because a silent `grep -q` in a long && chain tells
# whoever reads the build log nothing at all (2026-09-22: two builds were
# spent guessing which link had broken).
#
# The first two profiles are what dsh ships plus our bundle and overlay. The
# third is the composition that actually BOOTS: the same web profile with the
# deployment patch (app/profile.cordis.yml) on top, the way entrypoint.sh
# starts it. A check about what this deployment decided belongs on that one.
#
# Usage: assert-composition.sh <web-dump> <web-err> <headless-dump> <headless-err>
#                              <deploy-dump> <deploy-err>
set -eu

WEB_DUMP=$1
WEB_ERR=$2
HEADLESS_DUMP=$3
HEADLESS_ERR=$4
DEPLOY_DUMP=$5
DEPLOY_ERR=$6

fail() {
	echo "composition assertion FAILED: $1" >&2
	shift
	[ $# -gt 0 ] && { echo "---" >&2; cat "$@" >&2; }
	exit 1
}

# A bundle the composer cannot read is SKIPPED with a warning, not an error:
# the profile still dumps, and the harness still starts, missing whatever that
# bundle carried. That is how a preset file with one bad indent reached
# production with a healthy /healthz and a passing boot smoke.
if grep -q "skipping profile bundle" "$WEB_ERR" "$HEADLESS_ERR" "$DEPLOY_ERR"; then
	fail "a profile bundle did not load" "$WEB_ERR" "$HEADLESS_ERR" "$DEPLOY_ERR"
fi

# The agent core must be in both faces.
grep -q "agent-loop" "$WEB_DUMP" || fail "the web profile carries no agent-loop"
grep -q "agent-loop" "$HEADLESS_DUMP" || fail "the headless profile carries no agent-loop"

# The rows the deployment excludes (web/gen-bundle.mjs) must not be ENABLED.
# A row another bundle switches off appears in the dump as `disabled: true`
# and is fine: what is forbidden is one that would run.
for row in web-search-deepseek web-fetch-http session-log-deepseek \
           plugin-package-inventory-deepseek session-telemetry-otel \
           tool-result-pruner dsh-llm-pi-ai \
           llm-deepseek-account schedule; do
	for dump in "$WEB_DUMP" "$HEADLESS_DUMP"; do
		# Read each declaration of the row as a BLOCK: from its `- id:` line
		# to the next line indented no deeper. `disabled: true` sits among
		# the row's own keys, which may be several lines down (after `name:`,
		# before `config:`), so looking at the next line alone is not enough.
		enabled=$(awk -v row="$row" '
			function indent(s) { match(s, /^ */); return RLENGTH }
			$0 ~ "^ *- id: " row "$" { inblock = 1; depth = indent($0); block = $0; off = 0; next }
			inblock {
				if (indent($0) <= depth) {
					if (!off) print block
					inblock = 0
				} else {
					block = block "\n" $0
					if ($0 ~ /^ *disabled: true$/) off = 1
					next
				}
			}
			END { if (inblock && !off) print block }
		' "$dump")
		[ -z "$enabled" ] && continue
		echo "$enabled" >&2
		fail "excluded row '${row}' is composed and enabled in $(basename "$dump")"
	done
done

# The two `routine` presets an unattended run is dispatched under
# (proxy routines.go): the permission preset in the composed tree, and the
# agent preset as the declaration the overlay wrote into the web-app bundle.
grep -qE "^ *routine:$" "$WEB_DUMP" || fail "the routine permission preset is not composed"
test -e /dsh/packages/bundle/web-app/presets/routine.patch.yml \
	|| fail "the routine agent preset was not written into the web-app bundle"

# Tool-result spill must have a budget. `dsh-spill-policy` declares the budget
# OPTIONAL and installs no listeners when it is missing, so a renamed key (as
# in 0.1.7-alpha.2, `maxInlineBytes` -> `maxInlineTokens`) neither fails the
# build nor the boot: the whole of a large tool result simply reaches the
# model, and the first sign is the bill.
grep -q "maxInlineTokens:" "$WEB_DUMP" || fail "spill-policy has no budget in the web profile"
grep -q "maxInlineTokens:" "$HEADLESS_DUMP" || fail "spill-policy has no budget in the headless profile"

# Exactly one time-context row may be ENABLED in the composition that boots.
# dsh 0.1.7-rc.2 added a time-context row to the web-app bundle (shipped
# disabled, like its new Schedule sibling) under the very id this deployment
# had been inserting its own clock under, and in that one face only. A patch
# merging into a disabled row inherits `disabled`, so the clock, and with it
# the replay's pinned time, would have gone out in silence. Two enabled rows
# would stamp every turn twice. This reads the DEPLOY dump because the clock
# is a deployment decision: the bundle dumps carry upstream's disabled row
# and nothing else.
for dump in "$DEPLOY_DUMP"; do
	clocks=$(awk '
		function indent(s) { match(s, /^ */); return RLENGTH }
		$0 ~ "^ *- id: .*time-context$" { inblock = 1; depth = indent($0); off = 0; next }
		inblock {
			if (indent($0) <= depth) { if (!off) n++; inblock = 0 }
			else { if ($0 ~ /^ *disabled: true$/) off = 1; next }
		}
		END { if (inblock && !off) n++; print n + 0 }
	' "$dump")
	[ "$clocks" = "1" ] || fail "expected exactly 1 enabled time-context row in $(basename "$dump"), found $clocks"
done

# The model leg is the api-key adapter face, pointed at the in-TCB proxy. dsh
# 0.1.7-rc.2 turned '@deepseek-ai/dsh-llm-deepseek' into a library with no
# plugin entry, so the old name would mount nothing at all; and the account
# face bills a DeepSeek account over an egress this deployment does not have.
grep -q "dsh-llm-deepseek-api-key" "$DEPLOY_DUMP" || fail "the deployment mounts no api-key model adapter"
grep -q "baseURL: http://127.0.0.1:9411/model/v1" "$DEPLOY_DUMP" || fail "the model route is not pinned to the egress proxy"

# The allow-list bundle itself must be where the profiles point.
test -e /dsh-home/profiles/node_modules/@privasys/harness-bundle/cordis.patch.yml \
	|| fail "the harness bundle is not installed in the profile root"

echo "composition assertions passed"

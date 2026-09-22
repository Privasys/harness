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
# Usage: assert-composition.sh <web-dump> <web-err> <headless-dump> <headless-err>
set -eu

WEB_DUMP=$1
WEB_ERR=$2
HEADLESS_DUMP=$3
HEADLESS_ERR=$4

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
if grep -q "skipping profile bundle" "$WEB_ERR" "$HEADLESS_ERR"; then
	fail "a profile bundle did not load" "$WEB_ERR" "$HEADLESS_ERR"
fi

# The agent core must be in both faces.
grep -q "agent-loop" "$WEB_DUMP" || fail "the web profile carries no agent-loop"
grep -q "agent-loop" "$HEADLESS_DUMP" || fail "the headless profile carries no agent-loop"

# The rows the deployment excludes (web/gen-bundle.mjs) must not be ENABLED.
# A row another bundle switches off appears in the dump as `disabled: true`
# and is fine: what is forbidden is one that would run.
for row in web-search-deepseek web-fetch-http session-log-deepseek \
           plugin-package-inventory-deepseek session-telemetry-otel \
           tool-result-pruner dsh-llm-pi-ai; do
	for dump in "$WEB_DUMP" "$HEADLESS_DUMP"; do
		# Print the row's declaration and the line after it; a declaration
		# whose next line disables it is not a mount.
		hits=$(grep -A1 -E "(^|[^-a-z])${row}:?( |$)" "$dump" 2>/dev/null || true)
		[ -z "$hits" ] && continue
		if ! printf '%s\n' "$hits" | grep -q "disabled: true"; then
			echo "$hits" >&2
			fail "excluded row '${row}' is composed and enabled in $(basename "$dump")"
		fi
	done
done

# The two `routine` presets an unattended run is dispatched under
# (proxy routines.go): the permission preset in the composed tree, and the
# agent preset as the declaration the overlay wrote into the web-app bundle.
grep -qE "^ *routine:$" "$WEB_DUMP" || fail "the routine permission preset is not composed"
test -e /dsh/packages/bundle/web-app/presets/routine.patch.yml \
	|| fail "the routine agent preset was not written into the web-app bundle"

# The allow-list bundle itself must be where the profiles point.
test -e /dsh-home/profiles/node_modules/@privasys/harness-bundle/cordis.patch.yml \
	|| fail "the harness bundle is not installed in the profile root"

echo "composition assertions passed"

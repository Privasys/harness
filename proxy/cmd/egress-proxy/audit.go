// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// The egress audit record: what this harness actually reached, as distinct
// from what it permits.
//
// This is the second line of the two the attestation panel must show. A
// harness's POSTURE ("this harness may reach any host") and its BEHAVIOUR
// ("this session reached pypi.org and nothing else") are different claims, and
// showing only the first is how an honest product acquires a false badge —
// either way round. A permissive posture with all-attested behaviour deserves
// to be seen as such, and a green-looking harness that quietly fetched from
// twenty sites must not hide it.
//
// D10's floor-of-its-steps rule needs this too: a session's assurance is the
// floor of its steps, reported PER STEP, so mounting one non-attested tool
// does not retroactively grey the attested work in the same session. That is
// only reconstructable if each step's label was recorded when it happened.
//
// Deliberately in memory and deliberately bounded. These are operational
// records, not user data: they are already in the container log, which is the
// durable copy, and D6' says the harness holds no durable per-user state of
// its own. A ring buffer that cannot grow is also one that cannot be used to
// exhaust an enclave's memory by making requests at it.

import (
	"sync"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

// auditCapacity bounds the ring. Large enough to cover a working session,
// small enough that the memory cost is irrelevant.
const auditCapacity = 256

// EgressRecord is one decision the forward proxy made.
type EgressRecord struct {
	At        time.Time        `json:"at"`
	Host      string           `json:"host"`
	Port      string           `json:"port"`
	Method    string           `json:"method"`
	Allowed   bool             `json:"allowed"`
	Assurance policy.Assurance `json:"assurance"`
	// Reason is present only on a refusal, and is the same sentence the caller
	// received, so the panel and the agent agree on what happened.
	Reason string `json:"reason,omitempty"`
}

// egressAudit is a fixed-size ring of the most recent decisions.
type egressAudit struct {
	mu   sync.RWMutex
	buf  [auditCapacity]EgressRecord
	next int
	n    int
	// counts summarise the whole run, not just the retained window, so a long
	// session's early activity still shows up in the totals after the ring has
	// wrapped. A panel that silently forgot the first hundred fetches would be
	// misleading in exactly the way this record exists to prevent.
	allowed, refused int
	hosts            map[string]int
}

func newEgressAudit() *egressAudit { return &egressAudit{hosts: map[string]int{}} }

// Record notes one decision.
func (a *egressAudit) Record(r EgressRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.buf[a.next] = r
	a.next = (a.next + 1) % auditCapacity
	if a.n < auditCapacity {
		a.n++
	}
	if r.Allowed {
		a.allowed++
		// Only successful reaches are counted per host: a refusal says where
		// the agent TRIED to go, which is interesting in the list but would
		// overstate the surface if folded into "hosts reached".
		if len(a.hosts) < 512 {
			a.hosts[r.Host]++
		}
	} else {
		a.refused++
	}
}

// Snapshot returns the retained records newest-first, with the run totals.
func (a *egressAudit) Snapshot(limit int) map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if limit <= 0 || limit > a.n {
		limit = a.n
	}
	out := make([]EgressRecord, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (a.next - 1 - i + auditCapacity*2) % auditCapacity
		out = append(out, a.buf[idx])
	}
	hosts := make(map[string]int, len(a.hosts))
	for h, c := range a.hosts {
		hosts[h] = c
	}
	// The floor of every step taken: the weakest assurance any admitted
	// request ran under. This is the single honest answer to "how attested was
	// this run", and it is why the panel must never show the posture alone.
	floor := policy.Assurance("")
	for i := 0; i < a.n; i++ {
		r := a.buf[i]
		if !r.Allowed {
			continue
		}
		if floor == "" || assuranceRank(r.Assurance) < assuranceRank(floor) {
			floor = r.Assurance
		}
	}
	return map[string]any{
		"recent":         out,
		"allowed_total":  a.allowed,
		"refused_total":  a.refused,
		"hosts_reached":  hosts,
		"assurance_floor": string(floor),
		"retained":       a.n,
		"capacity":       auditCapacity,
	}
}

// assuranceRank orders the labels from weakest to strongest, so the floor is a
// minimum. Grey is weakest; a run with no direct egress at all has no floor
// here and is attested by the legs that did run.
func assuranceRank(a policy.Assurance) int {
	switch a {
	case policy.AssuranceGrey:
		return 0
	case policy.AssuranceAmber:
		return 1
	case policy.AssuranceGreen:
		return 2
	}
	return 0
}

// audit is the process-wide record. Package-level like actingSubject, and for
// the same reason: today one container serves one user. Per-user workers will
// each carry their own, which is also what stops one tenant reading another's
// browsing in the multi-tenant build.
var audit = newEgressAudit()

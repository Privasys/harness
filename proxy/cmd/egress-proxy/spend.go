// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Consent to per-call fees on the attested tool legs.
//
// The runtime hosting a priced tool refuses a call that carries no consent
// with 402 and the exact attested price (`X-Billing-Price: N credits`), and
// accepts only a byte-exact `X-Billing-Approved: N credits` in return; a
// successful priced call is then attestable proof that the caller knew that
// price (api-fees-plan §9, pricing-plan §6b). Since 2026-09-08 the payer is
// the user the harness acts for, not the harness — so the consent must be
// the USER's, never the model's. The model cannot be asked mid-call: it would
// approve anything. Instead the user gives STANDING consent in their own
// policy document (a per-call figure, a per-session figure, per-tool
// overrides), set over the sealed session and kept in their Drive; this
// file turns that document into the header, keeps the running total the
// per-session figure bounds, and records every charge for the panel.
//
//	GET /privasys/spend    what the acting user consented to, and what this
//	                       session has charged them so far

import (
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

// creditsRe reads the price out of the runtime's header or refusal message
// ("… charges 5000 credits …"). Anchored on the unit so a caller's own wrong
// figure, echoed first in a mismatch message, is never mistaken for it.
var creditsRe = regexp.MustCompile(`(\d+) credits`)

// parseCredits returns the attested price named in a 402, or 0.
func parseCredits(header, body string) uint64 {
	for _, src := range []string{header, body} {
		if m := creditsRe.FindStringSubmatch(src); m != nil {
			if v, err := strconv.ParseUint(m[1], 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

// FeeRecord is one charge the harness consented to on the user's behalf.
type FeeRecord struct {
	At      time.Time `json:"at"`
	Server  string    `json:"server"`
	Tool    string    `json:"tool"`
	Credits uint64    `json:"credits"`
}

const feeCapacity = 256

// spendMeter keeps, per acting subject, the running total and the recent
// charges. Process-local: a restart resets the session figure, which is the
// right reading of "session" for a harness that restarts on every deploy.
type spendMeter struct {
	store *policy.Store
	mu    sync.Mutex
	total map[string]uint64
	fees  map[string][]FeeRecord
}

func newSpendMeter(store *policy.Store) *spendMeter {
	return &spendMeter{store: store, total: map[string]uint64{}, fees: map[string][]FeeRecord{}}
}

// consent decides one priced call for the acting subject.
func (m *spendMeter) consent(server, tool string, credits uint64) (bool, string) {
	sub := currentSubject()
	if sub == "" {
		return false, "no signed-in user to charge"
	}
	m.mu.Lock()
	spent := m.total[sub]
	m.mu.Unlock()
	ok, why := m.store.Effective(sub).AdmitsSpend(server, credits, spent)
	if !ok {
		log.Printf("[spend] refused %s/%s at %d credits for %.8s… (spent %d): %s", server, tool, credits, sub, spent, why)
	}
	return ok, why
}

// charged records one delivered priced call.
func (m *spendMeter) charged(server, tool string, credits uint64) {
	sub := currentSubject()
	if sub == "" {
		return
	}
	m.mu.Lock()
	m.total[sub] += credits
	list := append(m.fees[sub], FeeRecord{At: time.Now().UTC(), Server: server, Tool: tool, Credits: credits})
	if len(list) > feeCapacity {
		list = list[len(list)-feeCapacity:]
	}
	m.fees[sub] = list
	total := m.total[sub]
	m.mu.Unlock()
	log.Printf("[spend] charged %s/%s %d credits to %.8s… (session total %d)", server, tool, credits, sub, total)
}

func (m *spendMeter) snapshot(sub string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	fees := m.fees[sub]
	out := make([]FeeRecord, len(fees))
	for i := range fees {
		out[i] = fees[len(fees)-1-i] // newest first
	}
	return map[string]any{
		"session_total_credits": m.total[sub],
		"charges":               out,
	}
}

// spendConsent and spendCharged are the shim's seams. Package variables, like
// the acting-subject lookup, so the shim never reaches back into main's
// wiring; main installs the meter once the policy store exists. Without a
// meter (a build that never installed one) every priced call is refused.
var (
	spendConsent = func(server, tool string, credits uint64) (bool, string) {
		return false, "no spending policy engine is installed in this build"
	}
	spendCharged = func(server, tool string, credits uint64) {}
)

func installSpendMeter(store *policy.Store) *spendMeter {
	m := newSpendMeter(store)
	spendConsent = m.consent
	spendCharged = m.charged
	return m
}

// registerSpendAPI mounts the per-user view: consent figures per tool and
// the running total. Subject-gated: what a person spends is theirs.
func registerSpendAPI(mux *http.ServeMux, store *policy.Store, meter *spendMeter, toolNames []string) {
	mux.HandleFunc("GET /privasys/spend", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a signed-in session is required"})
			return
		}
		recordSubject(sub)
		eff := store.Effective(sub)
		caps := map[string]policy.SpendCaps{}
		for _, name := range toolNames {
			caps[name] = eff.SpendCapsFor(name)
		}
		out := meter.snapshot(sub)
		out["caps"] = caps
		out["consented"] = eff.Tenant != nil && eff.Tenant.Spend != nil
		if eff.Tenant != nil && eff.Tenant.Spend != nil {
			out["spend"] = eff.Tenant.Spend
		}
		if eff.Ceiling != nil && eff.Ceiling.Spend != nil {
			out["ceiling_spend"] = eff.Ceiling.Spend
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// priceHeaderValue is the byte-exact consent the runtime expects.
func priceHeaderValue(credits uint64) string {
	return strings.TrimSpace(strconv.FormatUint(credits, 10) + " credits")
}

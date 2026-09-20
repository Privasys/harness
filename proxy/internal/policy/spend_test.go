// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package policy

import (
	"strings"
	"testing"
)

func parseSpendDoc(t *testing.T, raw string, scope Scope) *Document {
	t.Helper()
	d, err := Parse([]byte(raw), scope)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func TestSpendNoConsentRefusesEveryPricedCall(t *testing.T) {
	ceiling := Bootstrap(ModeOpen, nil, "app")
	eff := Effective{Ceiling: ceiling}
	if ok, why := eff.AdmitsSpend("web_search", 1, 0); ok || !strings.Contains(why, "not approved") {
		t.Fatalf("no tenant document must refuse: ok=%v why=%q", ok, why)
	}
	tenant := parseSpendDoc(t, `{"policy":"privasys.harness/v1","scope":"tenant","subject":"s","egress":{"mode":"open"}}`, ScopeTenant)
	eff.Tenant = tenant
	if ok, _ := eff.AdmitsSpend("web_search", 1, 0); ok {
		t.Fatal("a tenant document without a spend block must refuse")
	}
}

func TestSpendPerCallPerSessionAndToolOverride(t *testing.T) {
	ceiling := Bootstrap(ModeOpen, nil, "app")
	tenant := parseSpendDoc(t, `{"policy":"privasys.harness/v1","scope":"tenant","subject":"s","egress":{"mode":"open"},
		"spend":{"per_call_max_credits":5000,"per_session_max_credits":12000,"tools":{"drive":100}}}`, ScopeTenant)
	eff := Effective{Ceiling: ceiling, Tenant: tenant}

	if ok, _ := eff.AdmitsSpend("web_search", 5000, 0); !ok {
		t.Fatal("at the per-call figure must be admitted")
	}
	if ok, why := eff.AdmitsSpend("web_search", 5001, 0); ok || !strings.Contains(why, "per-call") {
		t.Fatalf("above the per-call figure must be refused: %v %q", ok, why)
	}
	if ok, _ := eff.AdmitsSpend("drive", 100, 0); !ok {
		t.Fatal("tool override at its figure must be admitted")
	}
	if ok, _ := eff.AdmitsSpend("drive", 101, 0); ok {
		t.Fatal("tool override above its figure must be refused")
	}
	if ok, _ := eff.AdmitsSpend("web_search", 5000, 7000); !ok {
		t.Fatal("within the session figure must be admitted")
	}
	if ok, why := eff.AdmitsSpend("web_search", 5000, 7001); ok || !strings.Contains(why, "session") {
		t.Fatalf("past the session figure must be refused: %v %q", ok, why)
	}
}

func TestSpendCeilingCapsNarrowButNeverGrant(t *testing.T) {
	ceiling := parseSpendDoc(t, `{"policy":"privasys.harness/v1","scope":"ceiling","egress":{"mode":"open"},
		"spend":{"per_call_max_credits":1000,"per_session_max_credits":3000}}`, ScopeCeiling)
	// Ceiling alone grants nothing.
	if ok, _ := (Effective{Ceiling: ceiling}).AdmitsSpend("web_search", 1, 0); ok {
		t.Fatal("a ceiling cap is not consent")
	}
	tenant := parseSpendDoc(t, `{"policy":"privasys.harness/v1","scope":"tenant","subject":"s","egress":{"mode":"open"},
		"spend":{"per_call_max_credits":5000}}`, ScopeTenant)
	eff := Effective{Ceiling: ceiling, Tenant: tenant}
	caps := eff.SpendCapsFor("web_search")
	if caps.PerCall != 1000 || caps.PerSession != 3000 {
		t.Fatalf("ceiling must narrow: %+v", caps)
	}
	if ok, _ := eff.AdmitsSpend("web_search", 1001, 0); ok {
		t.Fatal("the ceiling's per-call cap must bind")
	}
	if ok, _ := eff.AdmitsSpend("web_search", 1000, 2001); ok {
		t.Fatal("the ceiling's per-session cap must bind")
	}
}

func TestSaveTenantRefusesSpendAboveCeiling(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.LoadCeiling("", ModeOpen, nil, "app"); err != nil {
		t.Fatalf("ceiling: %v", err)
	}
	c := parseSpendDoc(t, `{"policy":"privasys.harness/v1","scope":"ceiling","egress":{"mode":"open"},
		"spend":{"per_call_max_credits":1000}}`, ScopeCeiling)
	s.setCeiling(c)
	_, _, err := s.SaveTenant("s", []byte(`{"policy":"privasys.harness/v1","scope":"tenant","subject":"s","egress":{"mode":"open"},
		"spend":{"per_call_max_credits":2000}}`))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("widening the ceiling's spend cap must be refused, got %v", err)
	}
	if _, _, err := s.SaveTenant("s", []byte(`{"policy":"privasys.harness/v1","scope":"tenant","subject":"s","egress":{"mode":"open"},
		"spend":{"per_call_max_credits":500}}`)); err != nil {
		t.Fatalf("narrowing must be accepted: %v", err)
	}
}

func TestRoutineRunsPerDayIsUnlimitedUntilSetAndNarrowedByTheCeiling(t *testing.T) {
	ceiling := Bootstrap(ModeOpen, nil, "app")
	if got := (Effective{Ceiling: ceiling}).RoutineRunsPerDay(); got != 0 {
		t.Fatalf("no document sets a cap: unlimited, got %d", got)
	}
	tenant := parseSpendDoc(t, `{"policy":"privasys.harness/v1","scope":"tenant","subject":"s","egress":{"mode":"open"},
		"spend":{"routines":{"runs_per_day":12}}}`, ScopeTenant)
	if got := (Effective{Ceiling: ceiling, Tenant: tenant}).RoutineRunsPerDay(); got != 12 {
		t.Fatalf("the holder's figure applies, got %d", got)
	}
	capped := parseSpendDoc(t, `{"policy":"privasys.harness/v1","scope":"ceiling","egress":{"mode":"open"},
		"spend":{"routines":{"runs_per_day":8}}}`, ScopeCeiling)
	if got := (Effective{Ceiling: capped, Tenant: tenant}).RoutineRunsPerDay(); got != 8 {
		t.Fatalf("the ceiling narrows, got %d", got)
	}
	if got := (Effective{Ceiling: capped}).RoutineRunsPerDay(); got != 8 {
		t.Fatalf("the ceiling alone caps, got %d", got)
	}
}

func TestSaveTenantRefusesRunsPerDayAboveCeiling(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.LoadCeiling("", ModeOpen, nil, "app"); err != nil {
		t.Fatalf("ceiling: %v", err)
	}
	s.setCeiling(parseSpendDoc(t, `{"policy":"privasys.harness/v1","scope":"ceiling","egress":{"mode":"open"},
		"spend":{"routines":{"runs_per_day":8}}}`, ScopeCeiling))
	for _, raw := range []string{
		`{"policy":"privasys.harness/v1","scope":"tenant","subject":"s","egress":{"mode":"open"},"spend":{"routines":{"runs_per_day":20}}}`,
		`{"policy":"privasys.harness/v1","scope":"tenant","subject":"s","egress":{"mode":"open"},"spend":{"per_call_max_credits":1}}`,
	} {
		if _, _, err := s.SaveTenant("s", []byte(raw)); err == nil || !strings.Contains(err.Error(), "runs per day") {
			t.Fatalf("a document above (or silent under) the ceiling's runs cap must be refused, got %v", err)
		}
	}
	if _, _, err := s.SaveTenant("s", []byte(`{"policy":"privasys.harness/v1","scope":"tenant","subject":"s","egress":{"mode":"open"},"spend":{"routines":{"runs_per_day":5}}}`)); err != nil {
		t.Fatalf("narrowing must be accepted: %v", err)
	}
}

// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Spend tokens (acting-subject plan v2): naming the user who pays.
//
// Every attested leg the harness makes for a signed-in user (the model, the
// tools) is paid by that user, and the callee learns who from a SPEND TOKEN
// the identity provider issued to this harness for that user, bound to a
// key this process generated at boot, plus a per-request proof signed with
// that key for the callee's host. The callee's runtime verifies both and
// asserts the payer to the callee app; the token never reaches an app and
// a leaked proof is worth one call to one host for one minute.
//
// The user consented once, in the wallet, to "Privasys Harness may spend
// my credits, up to N a month". Without that consent the identity provider
// refuses the token and the leg goes out unnamed: the callee then bills the
// harness as an app (its owner) or refuses, exactly as before this shipped.
// During the dual run the transitional X-Privasys-On-Behalf-Of header still
// travels beside the token; it is removed once every callee reads the
// runtime-asserted payer.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"enclave-os-mini/clients/go/spend"
)

var (
	spendSigner *spend.Signer
	// spendNoConsent throttles the "no consent" log per subject so a user
	// who never allowed spending does not flood the log every turn.
	spendNoConsent sync.Map // sub → time.Time
)

// initSpendSigner arms the signer on platform. The app id comes from the
// runtime (PRIVASYS_APP_ID, the same value stamped at OID 3.6) and the
// issuer from PRIVASYS_ISSUER, which the runtime injects too.
func initSpendSigner(onPlatform bool) {
	if !onPlatform {
		return
	}
	appID := os.Getenv("PRIVASYS_APP_ID")
	if appID == "" {
		appID = os.Getenv("HARNESS_APP_ID")
	}
	if appID == "" {
		log.Printf("[spend] no app id in the environment: legs go out without a spend token")
		return
	}
	// Reaching the identity provider is a plain HTTPS egress, never the
	// attested client (the IdP is not an enclave) and never the agent's
	// governed forward proxy (this is the proxy's own control-plane call).
	s, err := spend.NewSigner(appID, spend.IssuerFromEnv(os.Getenv),
		spend.WithHTTPClient(&http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}}))
	if err != nil {
		log.Printf("[spend] signer disabled: %v", err)
		return
	}
	spendSigner = s
	log.Printf("[spend] signer armed for app %s (key %s), publishing %s", s.AppID(), s.Kid(), spend.WellKnownPath)
}

// decorateSpend attaches the spend token + proof for sub to an outbound
// attested request. Best effort: a user who has not consented, or an
// identity provider that is unreachable, leaves the request unnamed and
// the callee decides (refuse, or bill the harness).
func decorateSpend(req *http.Request, sub string) {
	if spendSigner == nil || sub == "" {
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
	defer cancel()
	err := spendSigner.Decorate(ctx, req, sub)
	if err == nil {
		return
	}
	if errors.Is(err, spend.ErrNoConsent) {
		if last, ok := spendNoConsent.Load(sub); !ok || time.Since(last.(time.Time)) > 10*time.Minute {
			spendNoConsent.Store(sub, time.Now())
			log.Printf("[spend] user %.8s… has not allowed the harness to spend their credits; leg to %s goes out unnamed", sub, req.URL.Host)
		}
		return
	}
	log.Printf("[spend] token for %.8s… unavailable (%v); leg to %s goes out unnamed", sub, err, req.URL.Host)
}

// spendStatus is the panel's view: whether this harness can name a payer,
// and the cap the acting user's token carries.
func spendStatus(sub string) map[string]any {
	if spendSigner == nil {
		return map[string]any{"armed": false}
	}
	out := map[string]any{"armed": true, "app_id": spendSigner.AppID(), "kid": spendSigner.Kid()}
	if sub != "" {
		out["cap"] = spendSigner.Cap(sub)
	}
	return out
}

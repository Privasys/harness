package main

import (
	"log"
	"net/http"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

// How this harness acts for a user at Drive.
//
// The holder's files.ai grant, when they approved one: the call carries an
// AppGrant credential for it and names nobody, and Drive takes the holder
// from the grant they minted. That is how an assistant reaches the holder's
// knowledge without sharing an identifier with Drive.
//
// Otherwise, during the transition, the older way: the user's identifier in
// X-Privasys-On-Behalf-Of and a spend token, which Drive compares with the
// tenant owner. It goes when wallets that approve files.ai are out.

var (
	aiBroker    *capability.Broker
	aiDriveHost string
)

// setAIGrant picks the declared files.ai resource, if any, for Drive calls.
func setAIGrant(legs []resourceLeg, driveHost string) {
	for _, l := range legs {
		if l.kind == "files.ai" {
			aiBroker, aiDriveHost = l.broker, driveHost
			return
		}
	}
}

// driveAIAuthorization is an AppGrant Authorization value for the holder's
// files.ai grant at Drive, or "" when there is none to use.
func driveAIAuthorization(host, sub string) string {
	if aiBroker == nil || host == "" || host != aiDriveHost || sub == "" {
		return ""
	}
	g := aiBroker.Granted(sub)
	if !g.UsableAI() {
		return ""
	}
	tok, err := capability.AIGrantToken(aiBroker, g)
	if err != nil {
		log.Printf("[knowledge] files.ai grant present but no token: %v", err)
		return ""
	}
	return "AppGrant " + tok
}

// actForUser sets what tells `host` which user this call is for.
func actForUser(req *http.Request, host, sub string) {
	if sub == "" {
		return
	}
	if auth := driveAIAuthorization(host, sub); auth != "" {
		req.Header.Set("Authorization", auth)
		return
	}
	req.Header.Set("X-Privasys-On-Behalf-Of", sub)
	decorateSpend(req, sub)
}

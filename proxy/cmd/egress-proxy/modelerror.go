// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Model-leg error shaping.
//
// dsh's chat-completions adapter shows the user the provider's own message
// only when the error body is OpenAI-shaped ({"error":{"message":...}}); any
// other body collapses to the adapter's generic "<provider> API error (HTTP
// n)" line, which is how a refused or offline attested model surfaced as a
// DeepSeek error in the harness UI (2026-09-10). Every error the model leg
// hands back therefore leaves this proxy in that shape, carrying OUR words:
// the attestation verdict when the dial was refused, Confidential AI's own
// reason when it answered with one, or the status when it said nothing.

// modelErrorType names the error family in the OpenAI-shaped body.
const modelErrorType = "privasys_attested_model"

// modelErrorBody renders one OpenAI-shaped error body.
func modelErrorBody(code, message string) []byte {
	out, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    modelErrorType,
			"code":    code,
		},
	})
	if err != nil {
		return []byte(`{"error":{"message":"attested model error","type":"` + modelErrorType + `"}}`)
	}
	return out
}

// modelErrorMessage phrases an upstream failure for the person reading the
// chat: what the attested model is, that it did not answer, and why.
func modelErrorMessage(status int, reason string) string {
	reason = strings.TrimSpace(reason)
	what := "The attested model (Confidential AI) did not answer"
	switch status {
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		what = "The attested model (Confidential AI) is unavailable"
	case http.StatusPaymentRequired:
		what = "The attested model (Confidential AI) needs a funded Privasys account"
	case http.StatusTooManyRequests:
		what = "The attested model (Confidential AI) is busy"
	case http.StatusUnauthorized, http.StatusForbidden:
		what = "The attested model (Confidential AI) refused this session"
	}
	if reason == "" {
		return fmt.Sprintf("%s (HTTP %d).", what, status)
	}
	return fmt.Sprintf("%s (HTTP %d): %s", what, status, reason)
}

// refusedModelLegBody is the body for a dial the attested peer gate refused:
// no upstream status exists, so the verdict is the whole story.
func refusedModelLegBody(err error) []byte {
	return modelErrorBody("attestation_refused",
		"The attested model (Confidential AI) was refused by the harness's peer gate: "+err.Error())
}

// normaliseModelError returns the body to serve for an upstream error
// response, and whether it was rewritten. A body that already carries an
// OpenAI-shaped error with a message passes through untouched (vLLM's own
// errors do); every other shape is folded into one.
func normaliseModelError(status int, body []byte) ([]byte, bool) {
	trimmed := strings.TrimSpace(string(body))
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) == nil && obj != nil {
		switch e := obj["error"].(type) {
		case map[string]any:
			if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
				return body, false
			}
			// An error object without a message: keep any code/type it names.
			parts := []string{}
			for _, k := range []string{"code", "type"} {
				if s, ok := e[k].(string); ok && s != "" {
					parts = append(parts, s)
				}
			}
			return modelErrorBody(codeFor(status), modelErrorMessage(status, strings.Join(parts, " "))), true
		case string:
			return modelErrorBody(codeFor(status), modelErrorMessage(status, e)), true
		}
		if m, ok := obj["message"].(string); ok && strings.TrimSpace(m) != "" {
			return modelErrorBody(codeFor(status), modelErrorMessage(status, m)), true
		}
		if d, ok := obj["detail"].(string); ok && strings.TrimSpace(d) != "" {
			return modelErrorBody(codeFor(status), modelErrorMessage(status, d)), true
		}
	}
	// Not JSON (a gateway's HTML or text page) or JSON with nothing to say.
	reason := ""
	if trimmed != "" && !strings.HasPrefix(trimmed, "<") && !strings.HasPrefix(trimmed, "{") {
		reason = truncate([]byte(trimmed), 300)
	}
	return modelErrorBody(codeFor(status), modelErrorMessage(status, reason)), true
}

// codeFor maps an upstream status to a stable machine code.
func codeFor(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "session_refused"
	case http.StatusPaymentRequired:
		return "account_required"
	case http.StatusTooManyRequests:
		return "busy"
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return "unavailable"
	}
	return fmt.Sprintf("upstream_%d", status)
}

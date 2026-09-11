// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func decodeModelError(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var obj struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, body)
	}
	if obj.Error == nil {
		t.Fatalf("body carries no error object: %s", body)
	}
	return obj.Error
}

func TestNormaliseModelErrorKeepsOpenAIShape(t *testing.T) {
	in := []byte(`{"error":{"message":"model qwen not found","type":"NotFoundError","code":404}}`)
	out, rewritten := normaliseModelError(404, in)
	if rewritten || string(out) != string(in) {
		t.Fatalf("an OpenAI-shaped error must pass through untouched; got rewritten=%v %s", rewritten, out)
	}
}

func TestNormaliseModelErrorFoldsStringError(t *testing.T) {
	out, rewritten := normaliseModelError(402, []byte(`{"error":"no billing account for this caller"}`))
	if !rewritten {
		t.Fatal("a string error must be rewritten")
	}
	e := decodeModelError(t, out)
	msg, _ := e["message"].(string)
	if !strings.Contains(msg, "funded Privasys account") || !strings.Contains(msg, "no billing account for this caller") {
		t.Fatalf("message must name the attested model and keep the upstream reason: %q", msg)
	}
	if e["code"] != "account_required" || e["type"] != modelErrorType {
		t.Fatalf("unexpected code/type: %v", e)
	}
	if strings.Contains(msg, "DeepSeek") {
		t.Fatalf("no DeepSeek wording may reach the user: %q", msg)
	}
}

func TestNormaliseModelErrorHandlesTextAndEmpty(t *testing.T) {
	out, _ := normaliseModelError(503, []byte("upstream not ready"))
	msg, _ := decodeModelError(t, out)["message"].(string)
	if !strings.Contains(msg, "is unavailable (HTTP 503): upstream not ready") {
		t.Fatalf("text body must become the reason: %q", msg)
	}
	out, _ = normaliseModelError(502, []byte("<html><body>502 Bad Gateway</body></html>"))
	msg, _ = decodeModelError(t, out)["message"].(string)
	if strings.Contains(msg, "<html") || !strings.HasSuffix(msg, "(HTTP 502).") {
		t.Fatalf("an HTML page must not leak into the message: %q", msg)
	}
	out, _ = normaliseModelError(500, nil)
	msg, _ = decodeModelError(t, out)["message"].(string)
	if msg != "The attested model (Confidential AI) did not answer (HTTP 500)." {
		t.Fatalf("empty body wording: %q", msg)
	}
}

func TestRefusedModelLegBody(t *testing.T) {
	e := decodeModelError(t, refusedModelLegBody(errors.New("RTMR1 mismatch")))
	msg, _ := e["message"].(string)
	if !strings.Contains(msg, "peer gate") || !strings.Contains(msg, "RTMR1 mismatch") || e["code"] != "attestation_refused" {
		t.Fatalf("refusal must carry the verdict: %v", e)
	}
}

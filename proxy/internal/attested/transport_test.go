// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package attested

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// scriptedRT answers each RoundTrip from a script and records what it was
// sent, so the retry can be checked for both its occurrence and its body.
type scriptedRT struct {
	answers []*http.Response
	bodies  []string
}

func (s *scriptedRT) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	s.bodies = append(s.bodies, body)
	resp := s.answers[0]
	s.answers = s.answers[1:]
	return resp, nil
}

func answer(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

const lapsed = `{"error":"caller attestation failed: caller presented a certificate without current evidence on this connection (attest with client evidence first)"}`

func TestStaleVerdictIsRetriedOverAFreshDial(t *testing.T) {
	rt := &scriptedRT{answers: []*http.Response{answer(403, lapsed), answer(200, "ok")}}
	evictions := 0
	req, _ := http.NewRequest("PUT", "https://drive.example/api", bytes.NewReader([]byte("payload")))
	resp, err := roundTripStaleVerdict(rt, func() { evictions++ }, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want 200 from the retry", resp.StatusCode)
	}
	if evictions != 1 {
		t.Fatalf("evictions %d, want 1 before the retry", evictions)
	}
	if len(rt.bodies) != 2 || rt.bodies[1] != "payload" {
		t.Fatalf("retry bodies %q, want the payload replayed", rt.bodies)
	}
}

func TestStaleVerdictWithoutReplayableBodyEvictsOnClose(t *testing.T) {
	rt := &scriptedRT{answers: []*http.Response{answer(403, lapsed)}}
	evictions := 0
	req, _ := http.NewRequest("POST", "https://drive.example/api", io.NopCloser(strings.NewReader("stream")))
	req.GetBody = nil
	resp, err := roundTripStaleVerdict(rt, func() { evictions++ }, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("status %d, want the refusal returned as is", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != lapsed {
		t.Fatalf("body %q, want the whole refusal readable after the peek", body)
	}
	if evictions != 0 {
		t.Fatalf("evicted before the body was closed")
	}
	resp.Body.Close()
	if evictions != 1 {
		t.Fatalf("evictions %d after close, want 1", evictions)
	}
}

func TestOtherForbiddenPassesThrough(t *testing.T) {
	rt := &scriptedRT{answers: []*http.Response{answer(403, `{"error":"no allowed-caller entry matches caller app-id"}`)}}
	evictions := 0
	req, _ := http.NewRequest("GET", "https://drive.example/api", nil)
	resp, _ := roundTripStaleVerdict(rt, func() { evictions++ }, req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(body), "allowed-caller") || evictions != 0 || len(rt.bodies) != 1 {
		t.Fatalf("a policy refusal must pass through untouched: status=%d evictions=%d calls=%d", resp.StatusCode, evictions, len(rt.bodies))
	}
}

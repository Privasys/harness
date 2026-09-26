package main

import (
	"net/http/httptest"
	"testing"
)

// A worker's bearer names its user whichever header the model adapter uses,
// and never leaves this proxy in either.
func TestWorkerTokenInEitherHeaderNamesTheSubject(t *testing.T) {
	saved := workerMgr
	t.Cleanup(func() { workerMgr = saved })
	workerMgr = &WorkerManager{byToken: map[string]*Worker{"w-tok": {Subject: "alice", Token: "w-tok"}}}

	bearer := httptest.NewRequest("POST", "/model/v1/chat/completions", nil)
	bearer.Header.Set("Authorization", "Bearer w-tok")
	if got := subjectOfEgress(bearer); got != "alice" {
		t.Fatalf("Authorization bearer: subject %q, want alice", got)
	}

	apiKey := httptest.NewRequest("POST", "/model/v1/messages", nil)
	apiKey.Header.Set("X-Api-Key", "w-tok")
	if got := subjectOfEgress(apiKey); got != "alice" {
		t.Fatalf("x-api-key (Messages wire): subject %q, want alice", got)
	}

	stranger := httptest.NewRequest("POST", "/model/v1/messages", nil)
	stranger.Header.Set("X-Api-Key", "not-a-worker")
	if got := subjectOfEgress(stranger); got != "" {
		t.Fatalf("an unknown key must name nobody, got %q", got)
	}

	dropWorkerAPIKey(apiKey.Header)
	if apiKey.Header.Get("X-Api-Key") != "" {
		t.Fatal("a worker's x-api-key must not be forwarded")
	}
	dropWorkerAPIKey(stranger.Header)
	if stranger.Header.Get("X-Api-Key") != "not-a-worker" {
		t.Fatal("a key that is not a worker's is the caller's own credential and is kept")
	}
}

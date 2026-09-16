// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestElicitParsesOnlyA428WithASchema(t *testing.T) {
	if _, ok := parseElicit(200, []byte(`{"elicit":{"message":"x","requestedSchema":{"type":"object"}}}`)); ok {
		t.Fatal("a 200 is a result, not a question")
	}
	if _, ok := parseElicit(428, []byte(`{"error":"precondition"}`)); ok {
		t.Fatal("a 428 without a schema is an error, not a question")
	}
	ask, ok := parseElicit(428, []byte(`{"elicit":{"message":"Connect your mailbox","requestedSchema":{"type":"object","properties":{"password":{"type":"string","format":"password"}}}}}`))
	if !ok || ask.Message != "Connect your mailbox" {
		t.Fatalf("ask = %+v ok=%v", ask, ok)
	}
}

func TestRPCResponsesAreRecognised(t *testing.T) {
	if _, ok := isRPCResponse([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`)); ok {
		t.Fatal("a request is not a response")
	}
	id, ok := isRPCResponse([]byte(`{"jsonrpc":"2.0","id":"elicit-abc","result":{"action":"accept","content":{"user":"a@b"}}}`))
	if !ok || id != "elicit-abc" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

var elicitIDRe = regexp.MustCompile(`"id":"(elicit-[0-9a-f]+)"`)

// The whole exchange: the question goes out on the stream, the answer comes
// back as a JSON-RPC response, the tool app is called again with the answers
// merged in, and the final result closes the stream. The answers never
// appear anywhere but in the recall.
func TestElicitationRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/tool/mail/mcp", nil)
	var recalled json.RawMessage
	var recalledAs string
	done := make(chan struct{})
	go func() {
		defer close(done)
		elicitAndRetry(rec, r, json.RawMessage(`7`), "mail", "connect_mailbox",
			json.RawMessage(`{"host":"imap.example:993"}`),
			elicitAsk{Message: "Connect your mailbox", RequestedSchema: json.RawMessage(`{"type":"object"}`)},
			"session-1", "holder-1",
			func(a json.RawMessage, elicitationID string) ([]byte, int, error) {
				recalled = a
				recalledAs = elicitationID
				return []byte(`{"linked":true}`), 200, nil
			})
	}()
	// The question is on the stream within a moment.
	var id string
	for i := 0; i < 100 && id == ""; i++ {
		if m := elicitIDRe.FindStringSubmatch(rec.Body.String()); m != nil {
			id = m[1]
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if id == "" {
		t.Fatalf("no elicitation/create on the stream: %q", rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"method":"elicitation/create"`) || !strings.Contains(out, `"privasysSession":"session-1"`) || !strings.Contains(out, `"privasysServer":"mail"`) {
		t.Fatalf("the question must carry the schema and the routing meta: %q", out)
	}
	if !deliverElicitResponse(id, "holder-1", []byte(`{"jsonrpc":"2.0","id":"`+id+`","result":{"action":"accept","content":{"user":"me@example.com","password":"s3cret"}}}`)) {
		t.Fatal("the answer was not delivered to the open call")
	}
	<-done
	if !strings.Contains(string(recalled), `"password":"s3cret"`) || !strings.Contains(string(recalled), `"host":"imap.example:993"`) {
		t.Fatalf("the tool app must be called again with the answers merged over the arguments: %s", recalled)
	}
	if recalledAs != id {
		t.Fatalf("the recall must be marked as the answers to question %s, got %q", id, recalledAs)
	}
	final := rec.Body.String()
	if !strings.Contains(final, `"id":7`) || !strings.Contains(final, `{\"linked\":true}`) {
		t.Fatalf("the final result must close the stream under the original id: %q", final)
	}
	if strings.Contains(final, "s3cret") {
		t.Fatal("the answer leaked onto the stream the model can read")
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
	}
}

func TestElicitationDeclineEndsTheCallReadably(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/tool/mail/mcp", nil)
	done := make(chan struct{})
	recalled := false
	go func() {
		defer close(done)
		elicitAndRetry(rec, r, json.RawMessage(`8`), "mail", "connect_mailbox", json.RawMessage(`{}`),
			elicitAsk{Message: "q", RequestedSchema: json.RawMessage(`{"type":"object"}`)}, "", "holder-1",
			func(json.RawMessage, string) ([]byte, int, error) { recalled = true; return nil, 0, nil })
	}()
	var id string
	for i := 0; i < 100 && id == ""; i++ {
		if m := elicitIDRe.FindStringSubmatch(rec.Body.String()); m != nil {
			id = m[1]
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	deliverElicitResponse(id, "holder-1", []byte(`{"jsonrpc":"2.0","id":"`+id+`","result":{"action":"decline"}}`))
	<-done
	if recalled {
		t.Fatal("the tool app must not be called again after a decline")
	}
	if !strings.Contains(rec.Body.String(), "declined") || !strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("a decline must end the call as a readable tool error: %q", rec.Body.String())
	}
}

// The client validates the request against MCP's wire schema before its
// handler runs, and "password" is not a format it knows: the mark must leave
// the schema and travel in _meta (first live test, 2026-09-15: the request
// was refused and the form never appeared).
func TestSecretsAreLiftedIntoMetaAndTheSchemaGoesOutStandard(t *testing.T) {
	schema, secrets := liftSecrets(json.RawMessage(`{"type":"object","properties":{"user":{"type":"string","format":"email"},"password":{"type":"string","format":"password","title":"App password"}},"required":["user","password"]}`))
	if len(secrets) != 1 || secrets[0] != "password" {
		t.Fatalf("secrets = %v", secrets)
	}
	if strings.Contains(string(schema), `"format":"password"`) {
		t.Fatalf("the non-standard format must leave the schema: %s", schema)
	}
	if !strings.Contains(string(schema), `"format":"email"`) || !strings.Contains(string(schema), `"title":"App password"`) {
		t.Fatalf("standard fields must survive: %s", schema)
	}
	// The order of the properties is the order of the form: a map would
	// have put "host" before "user" (first live form, 2026-09-16).
	ordered, _ := liftSecrets(json.RawMessage(`{"type":"object","properties":{"user":{"type":"string"},"password":{"type":"string","format":"password"},"host":{"type":"string","default":"imap.gmail.com:993"}},"required":["user","password"]}`))
	if u, p, h := strings.Index(string(ordered), `"user"`), strings.Index(string(ordered), `"password"`), strings.Index(string(ordered), `"host"`); !(u < p && p < h) {
		t.Fatalf("the properties must keep their wire order: %s", ordered)
	}
	if !strings.Contains(string(ordered), `"required":["user","password"]`) {
		t.Fatalf("the rest of the schema must pass through untouched: %s", ordered)
	}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/tool/mail/mcp", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		elicitAndRetry(rec, r, json.RawMessage(`9`), "mail", "connect_mailbox", json.RawMessage(`{}`),
			elicitAsk{Message: "q", RequestedSchema: json.RawMessage(`{"type":"object","properties":{"password":{"type":"string","format":"password"}}}`)}, "", "holder-1",
			func(json.RawMessage, string) ([]byte, int, error) { return nil, 0, nil })
	}()
	var id string
	for i := 0; i < 100 && id == ""; i++ {
		if m := elicitIDRe.FindStringSubmatch(rec.Body.String()); m != nil {
			id = m[1]
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	out := rec.Body.String()
	if strings.Contains(out, `"format":"password"`) || !strings.Contains(out, `"privasysSecrets":["password"]`) {
		t.Fatalf("the wire must carry a standard schema and the secret names in _meta: %q", out)
	}
	// A client that refuses the question ends the call readably, not as a
	// five-minute wait.
	deliverElicitResponse(id, "holder-1", []byte(`{"jsonrpc":"2.0","id":"`+id+`","error":{"code":-32602,"message":"Invalid elicitation request"}}`))
	<-done
	if !strings.Contains(rec.Body.String(), "could not be read") {
		t.Fatalf("a refused question must end the call: %q", rec.Body.String())
	}
}

func TestAnAnswerFromAnotherSubjectIsIgnored(t *testing.T) {
	ch := make(chan []byte, 1)
	elicitPending.Store("elicit-test", &pendingElicit{ch: ch, sub: "holder-1"})
	defer elicitPending.Delete("elicit-test")
	if deliverElicitResponse("elicit-test", "holder-2", []byte(`{"jsonrpc":"2.0","id":"elicit-test","result":{"action":"accept","content":{}}}`)) {
		t.Fatal("a reply from another subject must not answer the question")
	}
	if len(ch) != 0 {
		t.Fatal("nothing may have been delivered")
	}
	if !deliverElicitResponse("elicit-test", "holder-1", []byte(`{"jsonrpc":"2.0","id":"elicit-test","result":{"action":"accept","content":{}}}`)) {
		t.Fatal("the asking holder's reply is the answer")
	}
}

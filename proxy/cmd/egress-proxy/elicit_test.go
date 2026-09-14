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
	done := make(chan struct{})
	go func() {
		defer close(done)
		elicitAndRetry(rec, r, json.RawMessage(`7`), "mail", "connect_mailbox",
			json.RawMessage(`{"host":"imap.example:993"}`),
			elicitAsk{Message: "Connect your mailbox", RequestedSchema: json.RawMessage(`{"type":"object"}`)},
			"session-1",
			func(a json.RawMessage) ([]byte, int, error) {
				recalled = a
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
	if !deliverElicitResponse(id, []byte(`{"jsonrpc":"2.0","id":"`+id+`","result":{"action":"accept","content":{"user":"me@example.com","password":"s3cret"}}}`)) {
		t.Fatal("the answer was not delivered to the open call")
	}
	<-done
	if !strings.Contains(string(recalled), `"password":"s3cret"`) || !strings.Contains(string(recalled), `"host":"imap.example:993"`) {
		t.Fatalf("the tool app must be called again with the answers merged over the arguments: %s", recalled)
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
			elicitAsk{Message: "q", RequestedSchema: json.RawMessage(`{"type":"object"}`)}, "",
			func(a json.RawMessage) ([]byte, int, error) { recalled = true; return nil, 0, nil })
	}()
	var id string
	for i := 0; i < 100 && id == ""; i++ {
		if m := elicitIDRe.FindStringSubmatch(rec.Body.String()); m != nil {
			id = m[1]
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	deliverElicitResponse(id, []byte(`{"jsonrpc":"2.0","id":"`+id+`","result":{"action":"decline"}}`))
	<-done
	if recalled {
		t.Fatal("the tool app must not be called again after a decline")
	}
	if !strings.Contains(rec.Body.String(), "declined") || !strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("a decline must end the call as a readable tool error: %q", rec.Body.String())
	}
}

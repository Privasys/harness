// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

// fakeRuntime answers the manager's resource endpoints the way the enclave
// runtime does, from a per-resource script.
func fakeRuntime(t *testing.T, status map[string]string, request map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/resources/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch parts[1] {
		case "status":
			_, _ = w.Write([]byte(status[parts[0]]))
		case "request":
			_, _ = w.Write([]byte(request[parts[0]]))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withRuntime(t *testing.T, srv *httptest.Server, sub string) []resourceLeg {
	t.Helper()
	t.Setenv("PRIVASYS_MANAGER_URL", srv.URL)
	t.Setenv("PRIVASYS_CONTAINER_TOKEN", "test-token")
	prev, _ := actingSubject.Load().(string)
	actingSubject.Store(sub)
	t.Cleanup(func() { actingSubject.Store(prev) })
	return []resourceLeg{
		{name: "storage", broker: capability.NewBroker("storage")},
		{name: "mailbox", broker: capability.NewBroker("mailbox")},
	}
}

func callAccessRPC(t *testing.T, legs []resourceLeg, method string, params any) map[string]any {
	t.Helper()
	setAccessLegs(legs...)
	t.Cleanup(func() { setAccessLegs() })
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	rec := httptest.NewRecorder()
	accessShim(rec, httptest.NewRequest(http.MethodPost, "/tool/access/mcp", strings.NewReader(string(body))))
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	return resp
}

// toolText returns the decoded text payload of a tools/call result.
func toolText(t *testing.T, resp map[string]any) (map[string]any, bool) {
	t.Helper()
	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("want one content item: %v", resp)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("tool text is not JSON: %q", text)
	}
	isErr, _ := result["isError"].(bool)
	return out, isErr
}

// The catalogue names both tools and offers exactly the declared resources.
// Nothing in it names a product: the server knows declarations, not services.
func TestAccessCatalogueOffersTheDeclaredResources(t *testing.T) {
	srv := fakeRuntime(t, nil, nil)
	legs := withRuntime(t, srv, "sub-A")
	resp := callAccessRPC(t, legs, "tools/list", map[string]any{})
	tools, _ := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("want list_access and request_access, got %v", tools)
	}
	req := tools[1].(map[string]any)
	if req["name"] != "request_access" {
		t.Fatalf("second tool: %v", req["name"])
	}
	enum := req["inputSchema"].(map[string]any)["properties"].(map[string]any)["resource"].(map[string]any)["enum"].([]any)
	if len(enum) != 2 || enum[0] != "mailbox" || enum[1] != "storage" {
		t.Fatalf("resource enum: %v", enum)
	}
	desc := strings.ToLower(req["description"].(string))
	if !strings.Contains(desc, "never ask the user to type a password") {
		t.Fatal("the description must keep credentials out of the conversation")
	}
}

// Each resource reports its state as the runtime knows it, and what the
// granting service returned is passed on untouched.
func TestListAccessReportsEachResource(t *testing.T) {
	srv := fakeRuntime(t, map[string]string{
		"storage": `{"persistent":true,"kind":"storage.folder","label":"Harness","permissions":["read","write"]}`,
		"mailbox": `{"persistent":true,"kind":"mail.mailbox","label":"Mail Connector","service_result":{"account":"you@example.com"}}`,
	}, nil)
	legs := withRuntime(t, srv, "sub-A")
	out, isErr := toolText(t, callAccessRPC(t, legs, "tools/call", map[string]any{"name": "list_access"}))
	if isErr {
		t.Fatalf("list_access errored: %v", out)
	}
	items := out["resources"].([]any)
	mailbox := items[0].(map[string]any)
	if mailbox["resource"] != "mailbox" || mailbox["state"] != "approved" {
		t.Fatalf("mailbox: %v", mailbox)
	}
	if mailbox["details"].(map[string]any)["account"] != "you@example.com" {
		t.Fatalf("service result not passed on: %v", mailbox)
	}
	if items[1].(map[string]any)["state"] != "approved" {
		t.Fatalf("storage: %v", items[1])
	}

	srv2 := fakeRuntime(t, map[string]string{
		"storage": `{"declined":true,"kind":"storage.folder"}`,
		"mailbox": `{"kind":"mail.mailbox"}`,
	}, nil)
	legs = withRuntime(t, srv2, "sub-B")
	out, _ = toolText(t, callAccessRPC(t, legs, "tools/call", map[string]any{"name": "list_access"}))
	items = out["resources"].([]any)
	if items[0].(map[string]any)["state"] != "not_approved" || items[1].(map[string]any)["state"] != "declined" {
		t.Fatalf("states: %v", items)
	}
}

// A request puts a question to the user's device and tells the agent what to
// say; a declined one is not reopened unless the user asks again.
func TestRequestAccessSendsOrExplains(t *testing.T) {
	srv := fakeRuntime(t, nil, map[string]string{
		"mailbox": `{"status":"pending","nonce":"n-123","app_host":"assistant.example"}`,
		"storage": `{"status":"declined"}`,
	})
	legs := withRuntime(t, srv, "sub-A")

	out, isErr := toolText(t, callAccessRPC(t, legs, "tools/call", map[string]any{
		"name": "request_access", "arguments": map[string]any{"resource": "mailbox"},
	}))
	if isErr || out["status"] != "sent" || out["code"] != "n-123" || out["app_host"] != "assistant.example" {
		t.Fatalf("sent: %v (error=%v)", out, isErr)
	}

	out, isErr = toolText(t, callAccessRPC(t, legs, "tools/call", map[string]any{
		"name": "request_access", "arguments": map[string]any{"resource": "storage"},
	}))
	if isErr || !strings.Contains(out["message"].(string), "ask_again") {
		t.Fatalf("declined: %v", out)
	}

	out, isErr = toolText(t, callAccessRPC(t, legs, "tools/call", map[string]any{
		"name": "request_access", "arguments": map[string]any{"resource": "calendar"},
	}))
	if !isErr || !strings.Contains(out["error"].(string), "mailbox, storage") {
		t.Fatalf("unknown resource: %v", out)
	}
}

// Without a signed-in user there is nobody to ask, and nothing is sent.
func TestAccessRefusesWithoutAUser(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	legs := withRuntime(t, srv, "")
	out, isErr := toolText(t, callAccessRPC(t, legs, "tools/call", map[string]any{
		"name": "request_access", "arguments": map[string]any{"resource": "mailbox"},
	}))
	if !isErr || !strings.Contains(out["error"].(string), "no signed-in user") {
		t.Fatalf("want a refusal: %v", out)
	}
	if asked {
		t.Fatal("the runtime must not be asked on nobody's behalf")
	}
}

package youzone

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientListRobotsUsesYOUZONEHeadersAndPath(t *testing.T) {
	var gotPath, gotCookie, gotMachine string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCookie = r.Header.Get("Cookie")
		gotMachine = r.URL.Query().Get("machineCode")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{
				"dataList": []map[string]any{{"id": "robot-1", "name": "cc-connect"}},
			},
		})
	}))
	defer server.Close()

	cfg := testClientConfig(server.URL)
	client := newClient(cfg, server.Client())
	robots, err := client.listRobots(context.Background(), "machine-1")
	if err != nil {
		t.Fatalf("listRobots() error = %v", err)
	}
	if gotPath != "/yonbip-ec-link/robot/web/list" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotMachine != "machine-1" {
		t.Fatalf("machineCode = %q", gotMachine)
	}
	if gotCookie != "yht_access_token=token; tenantid=tenant" {
		t.Fatalf("Cookie = %q", gotCookie)
	}
	if len(robots) != 1 || robots[0].ID != "robot-1" || robots[0].Name != "cc-connect" {
		t.Fatalf("robots = %#v", robots)
	}
}

func TestClientGetWSSParsesNestedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/yonbip-ec-link/claw-robot/client/getWss" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["id"] != "robot-1" || payload["robotId"] != "robot-1" {
			t.Fatalf("payload = %#v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"wssUrl": "wss://example.test/youzone"},
		})
	}))
	defer server.Close()

	client := newClient(testClientConfig(server.URL), server.Client())
	wss, err := client.getWSS(context.Background(), "robot-1")
	if err != nil {
		t.Fatalf("getWSS() error = %v", err)
	}
	if wss != "wss://example.test/youzone" {
		t.Fatalf("wss = %q", wss)
	}
}

func TestClientSendMessageUsesYOUZONEUniversalMessagePayload(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/yonbip-ec-link/claw-robot/client/sendMessage" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"packetId": "packet-1"},
		})
	}))
	defer server.Close()

	client := newClient(testClientConfig(server.URL), server.Client())
	out, err := buildOutboundMessage("hello", replyContext{})
	if err != nil {
		t.Fatalf("buildOutboundMessage() error = %v", err)
	}
	result, err := client.sendMessage(context.Background(), "robot-1", out)
	if err != nil {
		t.Fatalf("sendMessage() error = %v", err)
	}
	if !result.Success || result.PacketID != "packet-1" || result.BusinessCode == nil || *result.BusinessCode != 200 {
		t.Fatalf("result = %#v", result)
	}
	if payload["id"] != "robot-1" || payload["robotId"] != "robot-1" {
		t.Fatalf("payload robot fields = %#v", payload)
	}
	if payload["contentType"].(float64) != float64(youzoneUniversalMessageContentType) {
		t.Fatalf("contentType = %v, want %d", payload["contentType"], youzoneUniversalMessageContentType)
	}
	if payload["content"] != "hello" {
		t.Fatalf("content (digest) = %v", payload["content"])
	}
	extend, ok := payload["extend"].(string)
	if !ok || extend == "" {
		t.Fatalf("extend = %#v, want non-empty JSON string", payload["extend"])
	}
	var parsedExtend youzoneExtend
	if err := json.Unmarshal([]byte(extend), &parsedExtend); err != nil {
		t.Fatalf("extend is not valid JSON: %v", err)
	}
	if parsedExtend.ExtendType != "universalMessage" || parsedExtend.CustomData == "" {
		t.Fatalf("parsed extend = %#v", parsedExtend)
	}
	// The outbound HTTP body must not carry any conversation/recipient target:
	// the robot id alone identifies the conversation (see outbound.go).
	for _, k := range []string{"conversationId", "to", "target", "chatId", "robotUserId"} {
		if _, present := payload[k]; present {
			t.Fatalf("payload unexpectedly contains target field %q: %#v", k, payload)
		}
	}
}

func testClientConfig(baseURL string) config {
	return config{
		baseURL:     baseURL,
		apiPrefix:   "/yonbip-ec-link",
		accessToken: "token",
		tenantID:    "tenant",
		httpTimeout: defaultHTTPTimeout,
	}
}

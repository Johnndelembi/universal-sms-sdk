package sms

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type capturedRequest struct {
	Path, Method string
	Headers      http.Header
	Payload      any
}
type testServer struct {
	URL      string
	mu       sync.Mutex
	requests []capturedRequest
}

func (s *testServer) captured() []capturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedRequest(nil), s.requests...)
}

func server(t *testing.T, response any, code int) *testServer {
	t.Helper()
	capture := &testServer{}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var payload any
		if len(body) > 0 {
			payload, _ = decodeJSON(body)
		}
		capture.mu.Lock()
		capture.requests = append(capture.requests, capturedRequest{req.URL.RequestURI(), req.Method, req.Header.Clone(), payload})
		capture.mu.Unlock()
		if response == "disconnect" {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/redirect-target")
		w.WriteHeader(code)
		if response == "stall" {
			w.(http.Flusher).Flush()
			<-req.Context().Done()
			return
		}
		if text, ok := response.(string); ok {
			io.WriteString(w, text)
		} else {
			json.NewEncoder(w).Encode(response)
		}
	}))
	capture.URL = httpServer.URL
	t.Cleanup(httpServer.Close)
	return capture
}

func client(t *testing.T, provider string, change func(*Config)) *Client {
	t.Helper()
	config := Config{Provider: provider, APIKey: "key", SecretKey: "secret", APIToken: "token", SenderID: "MYAPP"}
	if change != nil {
		change(&config)
	}
	c, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var message = Message{To: "+255712345678", Text: "Hello", Reference: "order-1"}

func TestProviderContracts(t *testing.T) {
	raw, err := os.ReadFile("tests/send-contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range value.([]any) {
		contract := object(item)
		provider := contract["provider"].(string)
		t.Run(provider, func(t *testing.T) {
			http := server(t, contract["response"], 200)
			sms := client(t, provider, func(c *Config) { c.BaseURL = http.URL; c.AppID = "app" })
			result, err := sms.Send(context.Background(), message)
			if err != nil {
				t.Fatal(err)
			}
			if result.Provider != provider || result.MessageID != identifier(contract["messageId"]) || result.CorrelationID != identifier(contract["correlationId"]) || result.Status != contract["status"] {
				t.Fatalf("unexpected result: %+v", result)
			}
			requests := http.captured()
			if len(requests) != 1 {
				t.Fatalf("expected one request, got %d", len(requests))
			}
			request := requests[0]
			if request.Path != contract["path"] || request.Method != "POST" || !reflect.DeepEqual(request.Payload, contract["payload"]) {
				t.Fatalf("unexpected request: %+v", request)
			}
			for key, value := range object(contract["headers"]) {
				if request.Headers.Get(key) != value {
					t.Errorf("header %s: got %q", key, request.Headers.Get(key))
				}
			}
		})
	}
}

func TestInputValidationAndSenderOverride(t *testing.T) {
	http := server(t, map[string]any{"request_id": 1}, 200)
	sms := client(t, Beem, func(c *Config) { c.BaseURL = http.URL })
	for _, invalid := range []Message{{To: "0712345678", Text: "Hello"}, {To: message.To, Text: " "}, {To: message.To, Text: "Hello", SenderID: " "}, {To: message.To, Text: "Hello", Reference: " "}} {
		if _, err := sms.Send(context.Background(), invalid); err == nil {
			t.Errorf("accepted invalid message: %+v", invalid)
		}
	}
	if len(http.captured()) != 0 {
		t.Fatal("invalid input made an HTTP request")
	}
	_, err := sms.Send(context.Background(), Message{To: "255712345678", Text: "Hello", SenderID: "OVERRIDE"})
	if err != nil {
		t.Fatal(err)
	}
	if object(http.captured()[0].Payload)["source_addr"] != "OVERRIDE" {
		t.Fatal("sender override lost")
	}
}

func TestConfigurationValidation(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.Provider = "unknown" }, func(c *Config) { c.APIKey = "" }, func(c *Config) { c.SecretKey = "" }, func(c *Config) { c.Timeout = -time.Second }, func(c *Config) { c.BaseURL = "http://example.com" }, func(c *Config) { c.BaseURL = "https://key:secret@example.com" }, func(c *Config) { c.APIKey = "key\r\nInjected: true" }} {
		config := Config{Provider: Beem, APIKey: "key", SecretKey: "secret"}
		change(&config)
		if _, err := NewClient(config); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
	c := client(t, Beem, nil)
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, c), "secret") {
			t.Fatal("client logging exposed credentials")
		}
	}
	data, err := json.Marshal(c)
	if err != nil || string(data) != "{}" {
		t.Fatal("client serialization exposed configuration")
	}
}

func TestGeneratedReference(t *testing.T) {
	http := server(t, map[string]any{"messages": []any{map[string]any{"messageId": "id"}}}, 200)
	sms := client(t, NextSMS, func(c *Config) { c.BaseURL = http.URL })
	result, err := sms.Send(context.Background(), Message{To: message.To, Text: message.Text})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CorrelationID) != 36 || object(http.captured()[0].Payload)["reference"] != result.CorrelationID {
		t.Fatal("missing generated reference")
	}
}

func TestHTTPFailuresDoNotRetryOrRedirect(t *testing.T) {
	for _, code := range []int{400, 401, 429, 408, 503, 307} {
		http := server(t, map[string]any{"secret": "do-not-log"}, code)
		sms := client(t, Beem, func(c *Config) { c.BaseURL = http.URL })
		_, err := sms.Send(context.Background(), message)
		var failure *Error
		if !errors.As(err, &failure) || failure.StatusCode != code || failure.OutcomeUnknown != (code == 408 || code >= 500 || code < 400) {
			t.Fatalf("HTTP %d: %v", code, err)
		}
		if strings.Contains(err.Error(), "do-not-log") || len(http.captured()) != 1 {
			t.Fatal("response leaked or request retried")
		}
	}
}

func TestAmbiguousResponses(t *testing.T) {
	for _, response := range []any{"invalid", "{}", "[]", "null", `{"request_id":{}}`, `{} {}`, "disconnect", "stall"} {
		http := server(t, response, 200)
		sms := client(t, Beem, func(c *Config) { c.BaseURL = http.URL; c.Timeout = 100 * time.Millisecond })
		_, err := sms.Send(context.Background(), message)
		var failure *Error
		if !errors.As(err, &failure) || !failure.OutcomeUnknown {
			t.Fatalf("response %v: %v", response, err)
		}
		if len(http.captured()) != 1 {
			t.Fatal("send retried")
		}
	}
}

func TestExplicitRejections(t *testing.T) {
	for _, provider := range []string{Beem, Briq} {
		response := map[string]any{"successful": false, "success": false}
		http := server(t, response, 200)
		sms := client(t, provider, func(c *Config) { c.BaseURL = http.URL })
		_, err := sms.Send(context.Background(), message)
		var failure *Error
		if !errors.As(err, &failure) || failure.OutcomeUnknown {
			t.Fatalf("expected known rejection: %v", err)
		}
	}
}

func TestBeemStatus(t *testing.T) {
	http := server(t, []any{map[string]any{"dest_addr": "255700000001", "request_id": "31951", "status": "DELIVERED"}, map[string]any{"dest_addr": "255712345678", "request_id": 31951, "status": "UNDELIVERED"}}, 200)
	sms := client(t, Beem, func(c *Config) { c.DeliveryReportsBaseURL = http.URL })
	if _, err := sms.GetStatus(context.Background(), "31951", ""); err == nil {
		t.Fatal("accepted missing destination")
	}
	result, err := sms.GetStatus(context.Background(), "31951", message.To)
	if err != nil || result.Status != "failed" {
		t.Fatalf("unexpected report: %+v, %v", result, err)
	}
	request := http.captured()[0]
	if request.Path != "/public/v1/delivery-reports?dest_addr=255712345678&request_id=31951" || request.Headers.Get("Authorization") != "Basic a2V5OnNlY3JldA==" {
		t.Fatalf("wrong lookup request: %+v", request)
	}
	empty := server(t, []any{}, 200)
	sms = client(t, Beem, func(c *Config) { c.DeliveryReportsBaseURL = empty.URL })
	if _, err := sms.GetStatus(context.Background(), "id", message.To); err == nil {
		t.Fatal("accepted empty report")
	}
}

func TestNotifyStatus(t *testing.T) {
	for _, response := range []any{map[string]any{"data": map[string]any{"messageId": "id/a b", "status": "DELIVERED"}}, map[string]any{"status": "DELIVERED"}} {
		http := server(t, response, 200)
		sms := client(t, NotifyAfrica, func(c *Config) { c.BaseURL = http.URL })
		result, err := sms.GetStatus(context.Background(), "id/a b", "")
		if err != nil || result.Status != "delivered" {
			t.Fatalf("unexpected report: %+v, %v", result, err)
		}
		if http.captured()[0].Path != "/api/v1/api/messages/status/id%2Fa%20b" {
			t.Fatal("tracking ID not encoded")
		}
	}
	for _, response := range []any{nil, []any{}, map[string]any{}, map[string]any{"messageId": "wrong", "status": "DELIVERED"}} {
		http := server(t, response, 200)
		sms := client(t, NotifyAfrica, func(c *Config) { c.BaseURL = http.URL })
		if _, err := sms.GetStatus(context.Background(), "id", ""); err == nil {
			t.Fatal("accepted invalid report")
		}
	}
}

func sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestBriqWebhook(t *testing.T) {
	sms := client(t, Briq, func(c *Config) { c.WebhookSecret = "webhook-secret" })
	for name, status := range map[string]string{"sms.sent": "sent", "sms.delivered": "delivered", "sms.expired": "expired", "sms.failed": "failed", "future.event": "unknown"} {
		body, _ := json.Marshal(map[string]any{"id": "evt_1", "event": name, "data": map[string]any{"message_id": "msg-1", "job_id": "job-1"}})
		signature := sign(body)
		result, err := sms.ParseWebhook(body, signature)
		if err != nil || result.Status != status || result.EventID != "evt_1" || result.CorrelationID != "job-1" {
			t.Fatalf("unexpected webhook: %+v, %v", result, err)
		}
		if _, err := sms.ParseWebhook(append(append([]byte{}, body...), ' '), signature); err == nil {
			t.Fatal("accepted modified bytes")
		}
		for _, invalid := range []string{"", "sha256=nope", "sha256=" + strings.Repeat("0", 64)} {
			if _, err := sms.ParseWebhook(body, invalid); err == nil {
				t.Fatal("accepted invalid signature")
			}
		}
	}
	for _, body := range []string{"{}", "[]", "null", "invalid"} {
		if _, err := sms.ParseWebhook([]byte(body), sign([]byte(body))); err == nil {
			t.Fatal("accepted invalid webhook")
		}
	}
}

func TestNextSmsWebhook(t *testing.T) {
	sms := client(t, NextSMS, func(c *Config) { c.WebhookToken = "secure-token" })
	body := []byte(`{"token":"secure-token","messageId":"id","status":"EXPIRED"}`)
	result, err := sms.ParseWebhook(body, "")
	if err != nil || result.Status != "expired" {
		t.Fatalf("unexpected webhook: %+v, %v", result, err)
	}
	for _, invalid := range []string{`{"token":"wrong"}`, `{"token":"secure-token"}`, "null", "[]", "invalid"} {
		if _, err := sms.ParseWebhook([]byte(invalid), ""); err == nil {
			t.Fatal("accepted invalid webhook")
		}
	}
	if _, err := client(t, Beem, nil).ParseWebhook(body, ""); err == nil {
		t.Fatal("accepted unsupported webhook provider")
	}
}

func TestStatusNormalization(t *testing.T) {
	for raw, status := range map[string]string{"DELIVERED": "delivered", "ENROUTE (SENT)": "sent", "REJECTED_NETWORK": "failed", "FUTURE_STATUS": "unknown"} {
		http := server(t, map[string]any{"messages": []any{map[string]any{"messageId": "id", "status": map[string]any{"name": raw}}}}, 200)
		sms := client(t, NextSMS, func(c *Config) { c.BaseURL = http.URL })
		result, err := sms.Send(context.Background(), message)
		if err != nil || result.Status != status || result.ProviderStatus != raw || result.MessageID != "id" {
			t.Fatalf("unexpected result: %+v, %v", result, err)
		}
	}
}

func TestLookupFailureDoesNotImplyUnknownSend(t *testing.T) {
	http := server(t, map[string]any{}, 503)
	sms := client(t, NotifyAfrica, func(c *Config) { c.BaseURL = http.URL })
	_, err := sms.GetStatus(context.Background(), "id", "")
	var failure *Error
	if !errors.As(err, &failure) || failure.OutcomeUnknown {
		t.Fatalf("wrong lookup error: %v", err)
	}
	if _, err := client(t, Briq, nil).GetStatus(context.Background(), "id", ""); err == nil {
		t.Fatal("accepted unsupported polling provider")
	}
}

func TestContextCancellation(t *testing.T) {
	http := server(t, "stall", 200)
	sms := client(t, Beem, func(c *Config) { c.BaseURL = http.URL })
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sms.Send(cancelled, message); !errors.Is(err, context.Canceled) {
		t.Fatalf("wrong pre-send cancellation: %v", err)
	}
	if len(http.captured()) != 0 {
		t.Fatal("cancelled context made a request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := sms.Send(ctx, message)
	var failure *Error
	if !errors.As(err, &failure) || !failure.OutcomeUnknown || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("in-flight cancellation lost uncertainty: %v", err)
	}
	if len(http.captured()) != 1 {
		t.Fatal("cancelled send retried")
	}
}

func TestConcurrentClient(t *testing.T) {
	http := server(t, map[string]any{"request_id": 1}, 200)
	sms := client(t, Beem, func(c *Config) { c.BaseURL = http.URL })
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := sms.Send(context.Background(), message); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if len(http.captured()) != 10 {
		t.Fatal("concurrent sends lost or duplicated")
	}
}

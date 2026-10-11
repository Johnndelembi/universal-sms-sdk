// Package sms provides direct, server-side SMS clients for Beem, Briq, NextSMS and Notify Africa.
package sms

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	Beem         = "beem"
	Briq         = "briq"
	NextSMS      = "nextsms"
	NotifyAfrica = "notify-africa"
)

var baseURLs = map[string]string{
	Beem: "https://apisms.beem.africa", Briq: "https://karibu.briq.tz",
	NextSMS: "https://messaging-service.co.tz", NotifyAfrica: "https://api.notify.africa",
}
var phonePattern = regexp.MustCompile(`^\+?[1-9][0-9]{7,14}$`)
var signaturePattern = regexp.MustCompile(`^sha256=[a-f0-9]{64}$`)

// Config contains explicit provider credentials. Never log or serialize it.
type Config struct {
	Provider               string
	APIKey                 string
	SecretKey              string
	APIToken               string
	SenderID               string
	AppID                  string
	WebhookSecret          string
	WebhookToken           string
	BaseURL                string
	DeliveryReportsBaseURL string
	// Timeout is a total request deadline. Zero selects 30 seconds.
	Timeout time.Duration
}

// Message sends to one international destination. SenderID and Reference are optional.
// Reference is a NextSMS correlation value, not a guaranteed idempotency key.
type Message struct {
	To        string
	Text      string
	SenderID  string
	Reference string
}

// Result preserves tracking IDs and the provider's original status.
// Empty IDs mean unavailable; Briq's initial response has only CorrelationID.
type Result struct {
	Provider       string `json:"provider"`
	MessageID      string `json:"messageId,omitempty"`
	CorrelationID  string `json:"correlationId,omitempty"`
	Status         string `json:"status"`
	ProviderStatus string `json:"providerStatus"`
	EventID        string `json:"eventId,omitempty"`
}

// Error describes a provider failure without retaining credentials or response bodies.
// Reconcile before retrying if OutcomeUnknown is true. StatusCode is zero for non-HTTP errors.
type Error struct {
	Provider       string
	StatusCode     int
	OutcomeUnknown bool
	message        string
	cause          error
}

func (e *Error) Error() string { return e.Provider + ": " + e.message }

// Unwrap preserves context cancellation and deadlines without exposing transport details.
func (e *Error) Unwrap() error { return e.cause }

// Client is reusable and safe for concurrent requests. It never follows redirects or retries sends.
type Client struct {
	config  Config
	headers http.Header
	http    *http.Client
}

func NewClient(config Config) (*Client, error) {
	base, ok := baseURLs[config.Provider]
	if !ok {
		return nil, errors.New("provider must be beem, briq, nextsms or notify-africa")
	}
	if config.Timeout < 0 {
		return nil, errors.New("timeout must be positive")
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.BaseURL == "" {
		config.BaseURL = base
	}
	if config.DeliveryReportsBaseURL == "" {
		config.DeliveryReportsBaseURL = "https://dlrapi.beem.africa"
	}
	var err error
	if config.BaseURL, err = validateURL(config.BaseURL); err != nil {
		return nil, err
	}
	if config.DeliveryReportsBaseURL, err = validateURL(config.DeliveryReportsBaseURL); err != nil {
		return nil, err
	}
	credential, name := config.APIKey, "APIKey"
	if config.Provider == NextSMS {
		credential, name = config.APIToken, "APIToken"
	}
	if err = header(credential, name); err != nil {
		return nil, err
	}
	headers := http.Header{"Accept": {"application/json"}, "Content-Type": {"application/json"}}
	switch config.Provider {
	case Beem:
		if strings.Contains(credential, ":") {
			return nil, errors.New("Beem APIKey cannot contain a colon")
		}
		if err = required(config.SecretKey, "SecretKey"); err != nil {
			return nil, err
		}
		headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credential+":"+config.SecretKey)))
	case Briq:
		headers.Set("X-API-Key", credential)
		if config.AppID != "" {
			if err = header(config.AppID, "AppID"); err != nil {
				return nil, err
			}
			headers.Set("X-App-ID", config.AppID)
		}
	default:
		headers.Set("Authorization", "Bearer "+credential)
	}
	return &Client{config: config, headers: headers, http: &http.Client{
		Timeout:       config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Provider() string { return c.config.Provider }

// String omits credentials from common logging formats. Config must still never be logged.
func (c *Client) String() string   { return "sms.Client(provider=" + c.Provider() + ")" }
func (c *Client) GoString() string { return c.String() }

func (c *Client) Send(ctx context.Context, message Message) (Result, error) {
	to, err := phone(message.To)
	if err != nil {
		return Result{}, err
	}
	if err = required(message.Text, "Text"); err != nil {
		return Result{}, err
	}
	sender := message.SenderID
	if sender == "" {
		sender = c.config.SenderID
	}
	if err = required(sender, "SenderID"); err != nil {
		return Result{}, err
	}
	reference := message.Reference
	if reference == "" {
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return Result{}, errors.New("unable to generate reference")
		}
		id[6], id[8] = (id[6]&15)|64, (id[8]&63)|128
		reference = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	}
	if err = required(reference, "Reference"); err != nil {
		return Result{}, err
	}
	path := ""
	var payload map[string]any
	switch c.Provider() {
	case Beem:
		// shortcut: Beem uses encoding 0; add Unicode encoding when expanding beyond the existing Java flow.
		path = "/v1/send"
		payload = map[string]any{"source_addr": sender, "encoding": 0, "schedule_time": "", "message": message.Text,
			"recipients": []map[string]any{{"recipient_id": 1, "dest_addr": to}}}
	case Briq:
		path = "/v1/message/send-instant"
		payload = map[string]any{"content": message.Text, "recipients": []string{to}, "sender_id": sender}
	case NextSMS:
		path = "/api/sms/v2/text/single"
		payload = map[string]any{"from": sender, "to": to, "text": message.Text, "flash": 0, "reference": reference}
	case NotifyAfrica:
		path = "/api/v1/api/messages/send"
		payload = map[string]any{"phone_number": to, "message": message.Text, "sender_id": sender}
	}
	value, err := c.request(ctx, http.MethodPost, c.config.BaseURL, path, payload)
	if err != nil {
		return Result{}, err
	}
	response := object(value)
	var result Result
	switch c.Provider() {
	case Beem:
		if rejected, ok := response["successful"].(bool); ok && !rejected {
			return Result{}, c.failure("provider rejected the SMS", false)
		}
		result = c.result(response["request_id"], response["request_id"], "accepted")
	case Briq:
		if rejected, ok := response["success"].(bool); ok && !rejected {
			return Result{}, c.failure("provider rejected the SMS", false)
		}
		result = c.result(nil, response["job_id"], stringValue(response["status"], "accepted"))
	case NextSMS:
		var item map[string]any
		if messages, ok := response["messages"].([]any); ok && len(messages) > 0 {
			item = object(messages[0])
		}
		result = c.result(item["messageId"], reference, stringValue(object(item["status"])["name"], "UNKNOWN"))
	case NotifyAfrica:
		data := object(response["data"])
		result = c.result(data["messageId"], nil, stringValue(data["status"], "UNKNOWN"))
	}
	if (c.Provider() == Briq && result.CorrelationID == "") || (c.Provider() != Briq && result.MessageID == "") {
		return Result{}, c.failure("send response is missing its tracking ID; reconcile before resending", true)
	}
	return result, nil
}

// GetStatus requires the original destination for Beem. Pass an empty destination for Notify Africa.
func (c *Client) GetStatus(ctx context.Context, messageID, to string) (Result, error) {
	if err := required(messageID, "MessageID"); err != nil {
		return Result{}, err
	}
	switch c.Provider() {
	case Beem:
		destination, err := phone(to)
		if err != nil {
			return Result{}, err
		}
		query := url.Values{"dest_addr": {destination}, "request_id": {messageID}}
		value, err := c.request(ctx, http.MethodGet, c.config.DeliveryReportsBaseURL, "/public/v1/delivery-reports?"+query.Encode(), nil)
		if err != nil {
			return Result{}, err
		}
		if reports, ok := value.([]any); ok {
			for _, item := range reports {
				report := object(item)
				if report["dest_addr"] == destination && identifier(report["request_id"]) == messageID {
					if status, ok := report["status"].(string); ok {
						return c.result(messageID, nil, status), nil
					}
				}
			}
		}
		return Result{}, c.failure("no matching delivery report", false)
	case NotifyAfrica:
		value, err := c.request(ctx, http.MethodGet, c.config.BaseURL, "/api/v1/api/messages/status/"+url.PathEscape(messageID), nil)
		if err != nil {
			return Result{}, err
		}
		data := object(value)
		if nested, exists := data["data"]; exists && nested != nil {
			data = object(nested)
		}
		status, ok := data["status"].(string)
		returnedID, hasID := data["messageId"]
		if !ok || (hasID && identifier(returnedID) != messageID) {
			return Result{}, c.failure("invalid or mismatched delivery report", false)
		}
		return c.result(messageID, nil, status), nil
	default:
		return Result{}, fmt.Errorf("%s uses delivery webhooks; call ParseWebhook", c.Provider())
	}
}

// ParseWebhook authenticates original raw bytes. Signature is the Briq header, or empty for NextSMS.
func (c *Client) ParseWebhook(rawBody []byte, signature string) (Result, error) {
	if c.Provider() != Briq && c.Provider() != NextSMS {
		return Result{}, fmt.Errorf("%s uses status polling; call GetStatus", c.Provider())
	}
	if c.Provider() == Briq {
		if err := required(c.config.WebhookSecret, "WebhookSecret"); err != nil {
			return Result{}, err
		}
		if !signaturePattern.MatchString(signature) {
			return Result{}, errors.New("invalid webhook signature")
		}
		mac := hmac.New(sha256.New, []byte(c.config.WebhookSecret))
		mac.Write(rawBody)
		supplied, _ := hex.DecodeString(signature[7:])
		if !hmac.Equal(mac.Sum(nil), supplied) {
			return Result{}, errors.New("invalid webhook signature")
		}
	}
	value, err := decodeJSON(rawBody)
	if err != nil {
		return Result{}, errors.New("invalid webhook JSON")
	}
	event, ok := value.(map[string]any)
	if !ok {
		return Result{}, errors.New("invalid webhook JSON")
	}
	var delivery Result
	if c.Provider() == Briq {
		name := stringValue(event["event"], "")
		if err = required(name, "event"); err != nil {
			return Result{}, err
		}
		data := object(event["data"])
		delivery = c.result(data["message_id"], data["job_id"], name)
		delivery.EventID = identifier(event["id"])
		switch name {
		case "sms.sent":
			delivery.Status = "sent"
		case "sms.delivered":
			delivery.Status = "delivered"
		case "sms.expired":
			delivery.Status = "expired"
		case "sms.failed":
			delivery.Status = "failed"
		}
	} else {
		if err = required(c.config.WebhookToken, "WebhookToken"); err != nil {
			return Result{}, err
		}
		if subtle.ConstantTimeCompare([]byte(c.config.WebhookToken), []byte(stringValue(event["token"], ""))) != 1 {
			return Result{}, errors.New("invalid webhook token")
		}
		delivery = c.result(event["messageId"], nil, stringValue(event["status"], "UNKNOWN"))
	}
	if delivery.MessageID == "" && delivery.CorrelationID == "" {
		return Result{}, errors.New("webhook is missing its tracking ID")
	}
	return delivery, nil
}

func (c *Client) request(ctx context.Context, method, base, path string, payload map[string]any) (any, error) {
	sending := method == http.MethodPost
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, errors.New("invalid request payload")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid request URL")
	}
	req.Header = c.headers.Clone()
	response, err := c.http.Do(req)
	if err != nil {
		failure := c.failure("network request failed", sending)
		failure.cause = ctx.Err()
		if errors.Is(err, context.DeadlineExceeded) {
			failure.cause = context.DeadlineExceeded
		}
		return nil, failure
	}
	defer response.Body.Close()
	code := response.StatusCode
	if code < 200 || code >= 300 {
		return nil, &Error{Provider: c.Provider(), StatusCode: code, OutcomeUnknown: sending && (code >= 500 || code == 408 || code < 400), message: "HTTP request failed"}
	}
	content, err := io.ReadAll(response.Body)
	if err != nil {
		failure := c.failure("invalid or unreadable JSON response", sending)
		failure.cause = ctx.Err()
		if errors.Is(err, context.DeadlineExceeded) {
			failure.cause = context.DeadlineExceeded
		}
		return nil, failure
	}
	value, err := decodeJSON(content)
	if err != nil {
		return nil, c.failure("invalid or unreadable JSON response", sending)
	}
	return value, nil
}

func (c *Client) failure(message string, unknown bool) *Error {
	return &Error{Provider: c.Provider(), OutcomeUnknown: unknown, message: message}
}

func (c *Client) result(messageID, correlationID any, raw string) Result {
	status := "unknown"
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "ACCEPTED", "QUEUED", "PROCESSING", "PENDING", "PENDING_DISPATCHED", "PENDING_WAITING_DELIVERY":
		status = "accepted"
	case "SENT", "ENROUTE (SENT)":
		status = "sent"
	case "DELIVERED":
		status = "delivered"
	case "EXPIRED":
		status = "expired"
	case "FAILED", "UNDELIVERABLE", "UNDELIVERED", "REJECTED", "DELETED", "SKIPPED", "REJECTED_NETWORK", "REJECTED_DND":
		status = "failed"
	}
	return Result{Provider: c.Provider(), MessageID: identifier(messageID), CorrelationID: identifier(correlationID), Status: status, ProviderStatus: raw}
}

func decodeJSON(content []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value, extra any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing JSON content")
	}
	return value, nil
}

func object(value any) map[string]any { item, _ := value.(map[string]any); return item }
func stringValue(value any, fallback string) string {
	text, ok := value.(string)
	if ok {
		return text
	}
	return fallback
}
func identifier(value any) string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	if number, ok := value.(json.Number); ok {
		if integer, err := strconv.ParseInt(string(number), 10, 64); err == nil && integer >= 0 && integer <= 9007199254740991 {
			return strconv.FormatInt(integer, 10)
		}
	}
	return ""
}
func required(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be a non-empty string", name)
	}
	return nil
}
func header(value, name string) error {
	if err := required(value, name); err != nil {
		return err
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s cannot contain newlines", name)
	}
	return nil
}
func phone(value string) (string, error) {
	if !phonePattern.MatchString(value) {
		return "", errors.New("To must be an international number, e.g. +255712345678")
	}
	return strings.TrimPrefix(value, "+"), nil
}
func validateURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("invalid BaseURL")
	}
	local := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	if parsed.Hostname() == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && local)) || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("BaseURL must use HTTPS (HTTP is allowed only on loopback) without credentials, query or fragment")
	}
	return strings.TrimRight(value, "/"), nil
}

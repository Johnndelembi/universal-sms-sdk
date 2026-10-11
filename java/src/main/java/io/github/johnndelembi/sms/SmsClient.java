package io.github.johnndelembi.sms;

import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.net.URI;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.MessageDigest;
import java.time.Duration;
import java.util.Base64;
import java.util.HexFormat;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;

/** Synchronous, reusable server-side client. Never automatically retries a send. */
public final class SmsClient {
    private static final Map<String, String> BASE_URLS = Map.of(
        "beem", "https://apisms.beem.africa", "briq", "https://karibu.briq.tz",
        "nextsms", "https://messaging-service.co.tz", "notify-africa", "https://api.notify.africa");
    private final ObjectMapper mapper = new ObjectMapper().enable(DeserializationFeature.FAIL_ON_TRAILING_TOKENS);
    private final String provider;
    private final String senderId;
    private final String webhookSecret;
    private final String webhookToken;
    private final String baseUrl;
    private final String deliveryBaseUrl;
    private final Duration timeout;
    private final Map<String, String> headers;
    private final HttpClient http;

    public static Builder builder(String provider) { return new Builder(provider); }

    public static final class Builder {
        private final String provider;
        private String apiKey, secretKey, apiToken, senderId, appId, webhookSecret, webhookToken, baseUrl, deliveryReportsBaseUrl;
        private Duration timeout = Duration.ofSeconds(30);
        private Builder(String provider) { this.provider = provider; }
        public Builder apiKey(String value) { apiKey = value; return this; }
        public Builder secretKey(String value) { secretKey = value; return this; }
        public Builder apiToken(String value) { apiToken = value; return this; }
        public Builder senderId(String value) { senderId = value; return this; }
        public Builder appId(String value) { appId = value; return this; }
        public Builder webhookSecret(String value) { webhookSecret = value; return this; }
        public Builder webhookToken(String value) { webhookToken = value; return this; }
        public Builder baseUrl(String value) { baseUrl = value; return this; }
        public Builder deliveryReportsBaseUrl(String value) { deliveryReportsBaseUrl = value; return this; }
        public Builder timeout(Duration value) { timeout = value; return this; }
        public SmsClient build() { return new SmsClient(this); }
    }

    private SmsClient(Builder config) {
        if (config.provider == null || !BASE_URLS.containsKey(config.provider)) throw new IllegalArgumentException("provider must be beem, briq, nextsms or notify-africa");
        provider = config.provider;
        senderId = config.senderId;
        webhookSecret = config.webhookSecret;
        webhookToken = config.webhookToken;
        if (config.timeout == null || config.timeout.isZero() || config.timeout.isNegative()) throw new IllegalArgumentException("timeout must be positive");
        timeout = config.timeout;
        try { timeout.toNanos(); }
        catch (ArithmeticException error) { throw new IllegalArgumentException("timeout is too large"); }
        baseUrl = validateUrl(config.baseUrl == null ? BASE_URLS.get(provider) : config.baseUrl);
        deliveryBaseUrl = validateUrl(config.deliveryReportsBaseUrl == null ? "https://dlrapi.beem.africa" : config.deliveryReportsBaseUrl);
        var auth = new java.util.HashMap<String, String>();
        auth.put("Accept", "application/json");
        auth.put("Content-Type", "application/json");
        String credential = header(provider.equals("nextsms") ? config.apiToken : config.apiKey, provider.equals("nextsms") ? "apiToken" : "apiKey");
        if (provider.equals("beem")) {
            if (credential.contains(":")) throw new IllegalArgumentException("Beem apiKey cannot contain a colon");
            auth.put("Authorization", "Basic " + Base64.getEncoder().encodeToString((credential + ":" + required(config.secretKey, "secretKey")).getBytes(StandardCharsets.UTF_8)));
        } else if (provider.equals("briq")) {
            auth.put("X-API-Key", credential);
            if (config.appId != null) auth.put("X-App-ID", header(config.appId, "appId"));
        } else auth.put("Authorization", "Bearer " + credential);
        headers = Map.copyOf(auth);
        http = HttpClient.newBuilder().connectTimeout(timeout).followRedirects(HttpClient.Redirect.NEVER).build();
    }

    public String provider() { return provider; }
    public SmsResult send(String to, String text) { return send(new SmsMessage(to, text)); }

    public SmsResult send(SmsMessage message) {
        if (message == null) throw new IllegalArgumentException("message is required");
        String to = phone(message.to());
        String text = required(message.text(), "text");
        String sender = required(message.senderId() == null ? senderId : message.senderId(), "senderId");
        String reference = message.reference() == null ? UUID.randomUUID().toString() : required(message.reference(), "reference");
        SmsResult submission;
        switch (provider) {
            case "beem" -> {
                // shortcut: Beem uses encoding 0; add Unicode encoding when expanding beyond the existing Java flow.
                JsonNode response = request("POST", "/v1/send", Map.of("source_addr", sender, "encoding", 0, "schedule_time", "", "message", text,
                    "recipients", List.of(Map.of("recipient_id", 1, "dest_addr", to))), baseUrl);
                if (response.path("successful").isBoolean() && !response.path("successful").booleanValue()) throw failure("provider rejected the SMS", false);
                submission = result(response.path("request_id"), response.path("request_id"), "accepted", null);
            }
            case "briq" -> {
                JsonNode response = request("POST", "/v1/message/send-instant", Map.of("content", text, "recipients", List.of(to), "sender_id", sender), baseUrl);
                if (response.path("success").isBoolean() && !response.path("success").booleanValue()) throw failure("provider rejected the SMS", false);
                submission = result(null, response.path("job_id"), string(response.path("status"), "accepted"), null);
            }
            case "nextsms" -> {
                JsonNode response = request("POST", "/api/sms/v2/text/single", Map.of("from", sender, "to", to, "text", text, "flash", 0, "reference", reference), baseUrl);
                JsonNode item = response.path("messages").isArray() ? response.path("messages").path(0) : mapper.missingNode();
                submission = result(item.path("messageId"), mapper.getNodeFactory().textNode(reference), string(item.path("status").path("name"), "UNKNOWN"), null);
            }
            default -> {
                JsonNode data = request("POST", "/api/v1/api/messages/send", Map.of("phone_number", to, "message", text, "sender_id", sender), baseUrl).path("data");
                submission = result(data.path("messageId"), null, string(data.path("status"), "UNKNOWN"), null);
            }
        }
        if (provider.equals("briq") ? submission.correlationId() == null : submission.messageId() == null) throw failure("send response is missing its tracking ID; reconcile before resending", true);
        return submission;
    }

    public SmsResult getStatus(String messageId) { return getStatus(messageId, null); }

    public SmsResult getStatus(String messageId, String to) {
        required(messageId, "messageId");
        if (provider.equals("beem")) {
            String destination = phone(to);
            JsonNode reports = request("GET", "/public/v1/delivery-reports?dest_addr=" + encode(destination) + "&request_id=" + encode(messageId), null, deliveryBaseUrl);
            if (reports.isArray()) for (JsonNode report : reports) {
                if (destination.equals(string(report.path("dest_addr"), null)) && messageId.equals(identifier(report.path("request_id"))) && report.path("status").isTextual()) {
                    return result(mapper.getNodeFactory().textNode(messageId), null, report.path("status").textValue(), null);
                }
            }
            throw failure("no matching delivery report", false);
        }
        if (provider.equals("notify-africa")) {
            JsonNode response = request("GET", "/api/v1/api/messages/status/" + encode(messageId), null, baseUrl);
            JsonNode data = response.hasNonNull("data") ? response.path("data") : response;
            if (!data.isObject() || !data.path("status").isTextual() || (data.has("messageId") && !messageId.equals(identifier(data.path("messageId"))))) throw failure("invalid or mismatched delivery report", false);
            return result(mapper.getNodeFactory().textNode(messageId), null, data.path("status").textValue(), null);
        }
        throw new IllegalArgumentException(provider + " uses delivery webhooks; call parseWebhook");
    }

    public SmsResult parseWebhook(byte[] rawBody) { return parseWebhook(rawBody, null); }

    public SmsResult parseWebhook(byte[] rawBody, String signature) {
        if (!provider.equals("briq") && !provider.equals("nextsms")) throw new IllegalArgumentException(provider + " uses status polling; call getStatus");
        if (rawBody == null) throw new IllegalArgumentException("rawBody must be the original request bytes");
        if (provider.equals("briq")) {
            String secret = required(webhookSecret, "webhookSecret");
            if (signature == null || !signature.matches("sha256=[a-f0-9]{64}")) throw new IllegalArgumentException("invalid webhook signature");
            try {
                Mac mac = Mac.getInstance("HmacSHA256");
                mac.init(new SecretKeySpec(secret.getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
                if (!MessageDigest.isEqual(mac.doFinal(rawBody), HexFormat.of().parseHex(signature.substring(7)))) throw new IllegalArgumentException("invalid webhook signature");
            } catch (GeneralSecurityException error) { throw new IllegalStateException("unable to verify webhook signature"); }
        }
        JsonNode event;
        try { event = mapper.readTree(rawBody); }
        catch (IOException error) { throw new IllegalArgumentException("invalid webhook JSON"); }
        if (event == null || !event.isObject()) throw new IllegalArgumentException("invalid webhook JSON");
        SmsResult delivery;
        if (provider.equals("briq")) {
            String name = required(string(event.path("event"), null), "event");
            String status = switch (name) {
                case "sms.sent" -> "sent"; case "sms.delivered" -> "delivered";
                case "sms.expired" -> "expired"; case "sms.failed" -> "failed"; default -> "unknown";
            };
            delivery = new SmsResult(provider, identifier(event.path("data").path("message_id")), identifier(event.path("data").path("job_id")), status, name, identifier(event.path("id")));
        } else {
            String token = required(webhookToken, "webhookToken");
            if (!MessageDigest.isEqual(token.getBytes(StandardCharsets.UTF_8), string(event.path("token"), "").getBytes(StandardCharsets.UTF_8))) throw new IllegalArgumentException("invalid webhook token");
            delivery = result(event.path("messageId"), null, string(event.path("status"), "UNKNOWN"), null);
        }
        if (delivery.messageId() == null && delivery.correlationId() == null) throw new IllegalArgumentException("webhook is missing its tracking ID");
        return delivery;
    }

    private JsonNode request(String method, String path, Map<String, ?> payload, String base) {
        boolean sending = method.equals("POST");
        CompletableFuture<HttpResponse<byte[]>> pending = null;
        try {
            var builder = HttpRequest.newBuilder(URI.create(base + path)).timeout(timeout);
            headers.forEach(builder::header);
            builder.method(method, payload == null ? HttpRequest.BodyPublishers.noBody() : HttpRequest.BodyPublishers.ofByteArray(mapper.writeValueAsBytes(payload)));
            pending = http.sendAsync(builder.build(), HttpResponse.BodyHandlers.ofByteArray());
            HttpResponse<byte[]> response = pending.get(timeout.toNanos(), TimeUnit.NANOSECONDS);
            int code = response.statusCode();
            if (code < 200 || code >= 300) throw new SmsException(provider, "HTTP request failed", code, sending && (code >= 500 || code == 408 || code < 400));
            JsonNode data = mapper.readTree(response.body());
            if (data == null) throw failure("invalid or unreadable JSON response", sending);
            return data;
        } catch (InterruptedException error) {
            if (pending != null) pending.cancel(true);
            Thread.currentThread().interrupt();
            throw failure("request interrupted", sending);
        } catch (TimeoutException error) {
            if (pending != null) pending.cancel(true);
            throw failure("request timed out", sending);
        } catch (IOException | ExecutionException error) { throw failure("network request failed or invalid JSON response", sending); }
    }

    private SmsException failure(String message, boolean unknown) { return new SmsException(provider, message, null, unknown); }
    private SmsResult result(JsonNode messageId, JsonNode correlationId, String raw, String eventId) {
        String status = switch (raw.trim().toUpperCase(Locale.ROOT)) {
            case "ACCEPTED", "QUEUED", "PROCESSING", "PENDING", "PENDING_DISPATCHED", "PENDING_WAITING_DELIVERY" -> "accepted";
            case "SENT", "ENROUTE (SENT)" -> "sent"; case "DELIVERED" -> "delivered"; case "EXPIRED" -> "expired";
            case "FAILED", "UNDELIVERABLE", "UNDELIVERED", "REJECTED", "DELETED", "SKIPPED", "REJECTED_NETWORK", "REJECTED_DND" -> "failed";
            default -> "unknown";
        };
        return new SmsResult(provider, identifier(messageId), identifier(correlationId), status, raw, eventId);
    }
    private static String identifier(JsonNode value) {
        if (value == null) return null;
        if (value.isTextual() && !value.textValue().isBlank()) return value.textValue();
        if (value.isIntegralNumber() && value.canConvertToLong() && value.longValue() >= 0 && value.longValue() <= 9007199254740991L) return value.asText();
        return null;
    }
    private static String string(JsonNode value, String fallback) { return value != null && value.isTextual() ? value.textValue() : fallback; }
    private static String required(String value, String name) {
        if (value == null || value.isBlank()) throw new IllegalArgumentException(name + " must be a non-empty string");
        return value;
    }
    private static String header(String value, String name) {
        required(value, name);
        if (value.contains("\r") || value.contains("\n")) throw new IllegalArgumentException(name + " cannot contain newlines");
        return value;
    }
    private static String phone(String value) {
        required(value, "to");
        if (!value.matches("\\+?[1-9][0-9]{7,14}")) throw new IllegalArgumentException("to must be an international number, e.g. +255712345678");
        return value.startsWith("+") ? value.substring(1) : value;
    }
    private static String encode(String value) { return URLEncoder.encode(value, StandardCharsets.UTF_8).replace("+", "%20"); }
    private static String validateUrl(String value) {
        URI url;
        try { url = URI.create(required(value, "baseUrl")); }
        catch (IllegalArgumentException error) { throw new IllegalArgumentException("invalid baseUrl"); }
        boolean local = List.of("localhost", "127.0.0.1", "[::1]").contains(url.getHost() == null ? "" : url.getHost());
        if (url.getHost() == null || (!"https".equals(url.getScheme()) && !("http".equals(url.getScheme()) && local)) || url.getUserInfo() != null || url.getRawQuery() != null || url.getRawFragment() != null) throw new IllegalArgumentException("baseUrl must use HTTPS (HTTP is allowed only on loopback) without credentials, query or fragment");
        return value.replaceAll("/+$", "");
    }
}

package io.github.johnndelembi.sms;

import static org.junit.jupiter.api.Assertions.*;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpServer;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicReference;
import java.util.stream.Stream;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestFactory;

class SmsClientTest {
    private final ObjectMapper mapper = new ObjectMapper();
    private final List<HttpServer> servers = new CopyOnWriteArrayList<>();
    private final List<java.util.concurrent.ExecutorService> executors = new CopyOnWriteArrayList<>();
    private final List<JsonNode> requests = new CopyOnWriteArrayList<>();
    private final SmsMessage message = new SmsMessage("+255712345678", "Hello", null, "order-1");

    @AfterEach void cleanup() {
        servers.forEach(server -> server.stop(0));
        executors.forEach(java.util.concurrent.ExecutorService::shutdownNow);
    }

    private SmsClient.Builder client(String provider) {
        return SmsClient.builder(provider).apiKey("key").secretKey("secret").apiToken("token").senderId("MYAPP");
    }

    private String server(Object response, int status) throws Exception {
        var http = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        var executor = Executors.newCachedThreadPool();
        executors.add(executor);
        http.setExecutor(executor);
        http.createContext("/", exchange -> {
            byte[] body = exchange.getRequestBody().readAllBytes();
            var request = mapper.createObjectNode();
            request.put("path", exchange.getRequestURI().toString());
            request.put("method", exchange.getRequestMethod());
            var headers = request.putObject("headers");
            exchange.getRequestHeaders().forEach((key, values) -> headers.put(key.toLowerCase(), values.get(0)));
            request.set("payload", body.length == 0 ? mapper.nullNode() : mapper.readTree(body));
            requests.add(request);
            if ("disconnect".equals(response)) { exchange.close(); return; }
            exchange.getResponseHeaders().add("Content-Type", "application/json");
            exchange.getResponseHeaders().add("Location", "/redirect-target");
            byte[] data = response instanceof String text ? text.getBytes(StandardCharsets.UTF_8) : mapper.writeValueAsBytes(response);
            exchange.sendResponseHeaders(status, "stall".equals(response) ? 0 : data.length);
            if ("stall".equals(response)) {
                exchange.getResponseBody().flush();
                try { Thread.sleep(1000); } catch (InterruptedException ignored) { Thread.currentThread().interrupt(); }
                exchange.close();
                return;
            }
            exchange.getResponseBody().write(data);
            exchange.close();
        });
        http.start();
        servers.add(http);
        return "http://127.0.0.1:" + http.getAddress().getPort();
    }

    @TestFactory Stream<DynamicTest> providerContracts() throws Exception {
        JsonNode contracts = mapper.readTree(getClass().getResourceAsStream("/send-contracts.json"));
        return java.util.stream.StreamSupport.stream(contracts.spliterator(), false).map(contract -> DynamicTest.dynamicTest(contract.path("provider").asText(), () -> {
            requests.clear();
            String provider = contract.path("provider").asText();
            var sms = client(provider).baseUrl(server(contract.path("response"), 200)).appId("app").build();
            SmsResult result = sms.send(message);
            assertEquals(provider, result.provider());
            assertEquals(contract.path("messageId").isNull() ? null : contract.path("messageId").asText(), result.messageId());
            assertEquals(contract.path("correlationId").isNull() ? null : contract.path("correlationId").asText(), result.correlationId());
            assertEquals(contract.path("status").asText(), result.status());
            assertEquals(1, requests.size());
            JsonNode request = requests.get(0);
            assertEquals("POST", request.path("method").asText());
            assertEquals(contract.path("path"), request.path("path"));
            assertEquals(contract.path("payload"), request.path("payload"));
            contract.path("headers").fields().forEachRemaining(entry -> assertEquals(entry.getValue(), request.path("headers").path(entry.getKey())));
        }));
    }

    @Test void validatesInputsAndAllowsSenderOverride() throws Exception {
        var sms = client("beem").baseUrl(server(Map.of("request_id", 1), 200)).build();
        for (SmsMessage invalid : List.of(new SmsMessage("0712345678", "Hello"), new SmsMessage(message.to(), " "), new SmsMessage(message.to(), "Hello", "", null), new SmsMessage(message.to(), "Hello", null, ""))) {
            assertThrows(IllegalArgumentException.class, () -> sms.send(invalid));
        }
        assertTrue(requests.isEmpty());
        sms.send(new SmsMessage("255712345678", "Hello", "OVERRIDE", null));
        assertEquals("OVERRIDE", requests.get(0).path("payload").path("source_addr").asText());
    }

    @Test void validatesConfiguration() {
        assertThrows(IllegalArgumentException.class, () -> client("unknown").build());
        assertThrows(IllegalArgumentException.class, () -> client("beem").apiKey("").build());
        assertThrows(IllegalArgumentException.class, () -> client("beem").secretKey("").build());
        assertThrows(IllegalArgumentException.class, () -> client("beem").timeout(Duration.ZERO).build());
        assertThrows(IllegalArgumentException.class, () -> client("beem").timeout(Duration.ofSeconds(Long.MAX_VALUE)).build());
        for (String url : List.of("http://example.com", "https://key:secret@example.com", "https://example.com?q=1", "https://example.com#fragment")) {
            assertThrows(IllegalArgumentException.class, () -> client("beem").baseUrl(url).build());
        }
        assertThrows(IllegalArgumentException.class, () -> client("briq").apiKey("key\r\nInjected: true").build());
        assertFalse(client("beem").build().toString().contains("secret"));
    }

    @Test void generatesNextSmsReference() throws Exception {
        var sms = client("nextsms").baseUrl(server(Map.of("messages", List.of(Map.of("messageId", "id"))), 200)).build();
        var result = sms.send(message.to(), message.text());
        assertTrue(result.correlationId().matches("[a-f0-9-]{36}"));
        assertEquals(result.correlationId(), requests.get(0).path("payload").path("reference").asText());
    }

    @Test void doesNotRetryOrFollowRedirects() throws Exception {
        for (int code : List.of(400, 401, 429, 408, 503, 307)) {
            requests.clear();
            var sms = client("beem").baseUrl(server(Map.of("secret", "do-not-log"), code)).build();
            var error = assertThrows(SmsException.class, () -> sms.send(message));
            assertEquals(code, error.statusCode());
            assertEquals(code == 408 || code >= 500 || code < 400, error.outcomeUnknown());
            assertFalse(error.toString().contains("do-not-log"));
            assertEquals(1, requests.size());
        }
    }

    @Test void ambiguousSendNeedsReconciliation() throws Exception {
        for (String response : List.of("invalid", "{}", "[]", "null", "{\"request_id\":{}}", "{} {}", "disconnect", "stall")) {
            requests.clear();
            var sms = client("beem").baseUrl(server(response, 200)).timeout(Duration.ofMillis(300)).build();
            var error = assertThrows(SmsException.class, () -> sms.send(message));
            assertTrue(error.outcomeUnknown());
            assertEquals(1, requests.size());
        }
    }

    @Test void rejectsExplicitFailures() throws Exception {
        for (String provider : List.of("beem", "briq")) {
            var sms = client(provider).baseUrl(server(Map.of(provider.equals("beem") ? "successful" : "success", false), 200)).build();
            assertFalse(assertThrows(SmsException.class, () -> sms.send(message)).outcomeUnknown());
        }
    }

    @Test void beemLookupMatchesDestinationAndRequest() throws Exception {
        String url = server(List.of(Map.of("dest_addr", "255700000001", "request_id", "31951", "status", "DELIVERED"),
            Map.of("dest_addr", "255712345678", "request_id", 31951, "status", "UNDELIVERED")), 200);
        var sms = client("beem").deliveryReportsBaseUrl(url).build();
        assertThrows(IllegalArgumentException.class, () -> sms.getStatus("31951"));
        assertEquals("failed", sms.getStatus("31951", message.to()).status());
        assertEquals("/public/v1/delivery-reports?dest_addr=255712345678&request_id=31951", requests.get(0).path("path").asText());
        assertEquals("Basic a2V5OnNlY3JldA==", requests.get(0).path("headers").path("authorization").asText());
        var empty = client("beem").deliveryReportsBaseUrl(server(List.of(), 200)).build();
        assertFalse(assertThrows(SmsException.class, () -> empty.getStatus("id", message.to())).outcomeUnknown());
    }

    @Test void notifyLookupValidatesAndEncodesTrackingId() throws Exception {
        for (Object response : List.of(Map.of("data", Map.of("messageId", "id/a b", "status", "DELIVERED")), Map.of("status", "DELIVERED"))) {
            requests.clear();
            var sms = client("notify-africa").baseUrl(server(response, 200)).build();
            assertEquals("delivered", sms.getStatus("id/a b").status());
            assertEquals("/api/v1/api/messages/status/id%2Fa%20b", requests.get(0).path("path").asText());
        }
        for (Object response : List.of(Map.of(), List.of(), Map.of("status", "DELIVERED", "messageId", "wrong"))) {
            var sms = client("notify-africa").baseUrl(server(response, 200)).build();
            assertThrows(SmsException.class, () -> sms.getStatus("id"));
        }
    }

    private String signature(byte[] body) throws Exception {
        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec("webhook-secret".getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
        return "sha256=" + HexFormat.of().formatHex(mac.doFinal(body));
    }

    @Test void briqAuthenticatesOriginalWebhookBytes() throws Exception {
        var sms = client("briq").webhookSecret("webhook-secret").build();
        for (var entry : Map.of("sms.sent", "sent", "sms.delivered", "delivered", "sms.expired", "expired", "sms.failed", "failed", "future.event", "unknown").entrySet()) {
            byte[] body = mapper.writeValueAsBytes(Map.of("id", "evt_1", "event", entry.getKey(), "data", Map.of("message_id", "msg-1", "job_id", "job-1")));
            String signature = signature(body);
            var event = sms.parseWebhook(body, signature);
            assertEquals(entry.getValue(), event.status());
            assertEquals("evt_1", event.eventId());
            assertEquals("job-1", event.correlationId());
            byte[] changed = (new String(body, StandardCharsets.UTF_8) + " ").getBytes(StandardCharsets.UTF_8);
            assertThrows(IllegalArgumentException.class, () -> sms.parseWebhook(changed, signature));
            for (String invalid : List.of("", "sha256=nope", "sha256=" + "0".repeat(64))) assertThrows(IllegalArgumentException.class, () -> sms.parseWebhook(body, invalid));
        }
        for (String invalid : List.of("{}", "[]", "null", "invalid")) {
            byte[] body = invalid.getBytes(StandardCharsets.UTF_8);
            assertThrows(IllegalArgumentException.class, () -> sms.parseWebhook(body, signature(body)));
        }
    }

    @Test void nextSmsAuthenticatesToken() throws Exception {
        var sms = client("nextsms").webhookToken("secure-token").build();
        byte[] body = mapper.writeValueAsBytes(Map.of("token", "secure-token", "messageId", "id", "status", "EXPIRED"));
        assertEquals("expired", sms.parseWebhook(body).status());
        for (String invalid : List.of("{\"token\":\"wrong\"}", "{\"token\":\"secure-token\"}", "[]", "null", "invalid")) assertThrows(IllegalArgumentException.class, () -> sms.parseWebhook(invalid.getBytes(StandardCharsets.UTF_8)));
        assertThrows(IllegalArgumentException.class, () -> client("beem").build().parseWebhook(body));
    }

    @Test void normalizesStatusesAndRetainsIds() throws Exception {
        for (var entry : Map.of("DELIVERED", "delivered", "ENROUTE (SENT)", "sent", "REJECTED_NETWORK", "failed", "FUTURE_STATUS", "unknown").entrySet()) {
            var sms = client("nextsms").baseUrl(server(Map.of("messages", List.of(Map.of("messageId", "id", "status", Map.of("name", entry.getKey())))), 200)).build();
            var result = sms.send(message);
            assertEquals(entry.getValue(), result.status());
            assertEquals(entry.getKey(), result.providerStatus());
            assertEquals("id", result.messageId());
        }
    }

    @Test void lookupFailureIsNotAnUnknownSend() throws Exception {
        var sms = client("notify-africa").baseUrl(server(Map.of(), 503)).build();
        assertFalse(assertThrows(SmsException.class, () -> sms.getStatus("id")).outcomeUnknown());
        assertThrows(IllegalArgumentException.class, () -> client("briq").build().getStatus("id"));
    }

    @Test void interruptionCancelsAndPreservesInterruptFlag() throws Exception {
        var sms = client("beem").baseUrl(server("stall", 200)).build();
        var started = new CountDownLatch(1);
        var failure = new AtomicReference<Throwable>();
        var thread = new Thread(() -> {
            started.countDown();
            try { sms.send(message); failure.set(new AssertionError("send should be interrupted")); }
            catch (SmsException error) {
                if (!error.outcomeUnknown() || !Thread.currentThread().isInterrupted()) failure.set(new AssertionError("interrupt state lost"));
            }
        });
        thread.start();
        started.await();
        thread.interrupt();
        thread.join(3000);
        assertFalse(thread.isAlive());
        assertNull(failure.get());
    }
}

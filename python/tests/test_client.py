import hashlib
import hmac
import json
import socket
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from universal_sms import SmsClient, SmsError

CONTRACTS = json.loads((Path(__file__).parents[2] / "tests/send-contracts.json").read_text())
MESSAGE = {"to": "+255712345678", "text": "Hello", "reference": "order-1"}


class ClientTests(unittest.TestCase):
    def server(self, response, status=200):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                self.respond()

            def do_GET(self):
                self.respond()

            def respond(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                requests.append({"path": self.path, "headers": dict((key.lower(), value) for key, value in self.headers.items()),
                                 "method": self.command, "payload": json.loads(body) if body else None})
                if response == "disconnect":
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                    return
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Location", "/redirect-target")
                self.end_headers()
                if response == "stall":
                    threading.Event().wait(0.15)
                    return
                content = response if isinstance(response, str) else json.dumps(response)
                self.wfile.write(content.encode())

            def log_message(self, *args):
                pass

        http = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=http.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        thread.start()

        def close():
            http.shutdown()
            http.server_close()
            thread.join()

        self.addCleanup(close)
        return f"http://127.0.0.1:{http.server_port}", requests

    def client(self, provider="beem", **kwargs):
        options = {"api_key": "key", "secret_key": "secret", "api_token": "token", "sender_id": "MYAPP"}
        options.update(kwargs)
        return SmsClient(provider, **options)

    def test_shared_provider_contracts(self):
        for contract in CONTRACTS:
            with self.subTest(provider=contract["provider"]):
                base_url, requests = self.server(contract["response"])
                result = self.client(contract["provider"], base_url=base_url, app_id="app").send(**MESSAGE)
                self.assertEqual(result.provider, contract["provider"])
                self.assertEqual(result.message_id, contract["messageId"])
                self.assertEqual(result.correlation_id, contract["correlationId"])
                self.assertEqual(result.status, contract["status"])
                self.assertEqual(len(requests), 1)
                self.assertEqual(requests[0]["method"], "POST")
                self.assertEqual(requests[0]["path"], contract["path"])
                self.assertEqual(requests[0]["payload"], contract["payload"])
                for key, value in contract["headers"].items():
                    self.assertEqual(requests[0]["headers"][key], value)

    def test_validates_before_sending_and_overrides_sender(self):
        base_url, requests = self.server(CONTRACTS[0]["response"])
        client = self.client(base_url=base_url)
        for invalid in [{"to": "0712345678"}, {"to": 255712345678}, {"text": " "}, {"sender_id": ""}, {"reference": ""}]:
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                client.send(**{**MESSAGE, **invalid})
        self.assertEqual(requests, [])
        client.send(**{**MESSAGE, "to": "255712345678", "sender_id": "OVERRIDE"})
        self.assertEqual(requests[0]["payload"]["source_addr"], "OVERRIDE")

    def test_configuration(self):
        for options in [{"provider": "unknown"}, {"api_key": ""}, {"secret_key": ""}, {"timeout": 0}, {"timeout": float("inf")},
                        {"base_url": "http://example.com"}, {"base_url": "https://key:secret@example.com"}, {"api_key": "key\r\nInjected: true"}]:
            with self.subTest(options=options), self.assertRaises(ValueError):
                self.client(**options)
        self.assertNotIn("secret", repr(self.client()))

    def test_generated_reference(self):
        base_url, requests = self.server(CONTRACTS[2]["response"])
        result = self.client("nextsms", base_url=base_url).send(to=MESSAGE["to"], text=MESSAGE["text"])
        self.assertRegex(result.correlation_id, r"^[a-f0-9-]{36}$")
        self.assertEqual(result.correlation_id, requests[0]["payload"]["reference"])

    def test_http_errors_without_retry_or_redirect(self):
        for code, unknown in [(400, False), (401, False), (429, False), (408, True), (503, True), (307, True)]:
            with self.subTest(code=code):
                base_url, requests = self.server({"secret": "do-not-log"}, code)
                with self.assertRaises(SmsError) as caught:
                    self.client(base_url=base_url).send(**MESSAGE)
                self.assertEqual(caught.exception.status_code, code)
                self.assertEqual(caught.exception.outcome_unknown, unknown)
                self.assertNotIn("do-not-log", str(caught.exception))
                self.assertEqual(len(requests), 1)

    def test_ambiguous_responses(self):
        for response in ["not-json", "disconnect", "stall", {}, [], None, {"request_id": {}}]:
            with self.subTest(response=response):
                base_url, requests = self.server(response)
                with self.assertRaises(SmsError) as caught:
                    self.client(base_url=base_url, timeout=0.05).send(**MESSAGE)
                self.assertTrue(caught.exception.outcome_unknown)
                self.assertEqual(len(requests), 1)

    def test_explicit_provider_rejection(self):
        for provider, response in [("beem", {"successful": False, "request_id": 1}), ("briq", {"success": False, "job_id": "1"})]:
            base_url, _ = self.server(response)
            with self.assertRaises(SmsError) as caught:
                self.client(provider, base_url=base_url).send(**MESSAGE)
            self.assertFalse(caught.exception.outcome_unknown)

    def test_beem_delivery_lookup(self):
        base_url, requests = self.server([
            {"dest_addr": "255700000001", "request_id": "31951", "status": "DELIVERED"},
            {"dest_addr": "255712345678", "request_id": 31951, "status": "UNDELIVERED"},
        ])
        client = self.client(delivery_reports_base_url=base_url)
        with self.assertRaises(ValueError):
            client.get_status("31951")
        self.assertEqual(client.get_status("31951", to=MESSAGE["to"]).status, "failed")
        self.assertEqual(requests[0]["path"], "/public/v1/delivery-reports?dest_addr=255712345678&request_id=31951")
        self.assertEqual(requests[0]["headers"]["authorization"], "Basic a2V5OnNlY3JldA==")

    def test_unmatched_beem_report(self):
        base_url, _ = self.server([])
        with self.assertRaises(SmsError) as caught:
            self.client(delivery_reports_base_url=base_url).get_status("31951", to=MESSAGE["to"])
        self.assertFalse(caught.exception.outcome_unknown)

    def test_notify_lookup(self):
        for response in [{"data": {"messageId": "id/a", "status": "DELIVERED"}}, {"status": "DELIVERED"}]:
            base_url, requests = self.server(response)
            self.assertEqual(self.client("notify-africa", base_url=base_url).get_status("id/a").status, "delivered")
            self.assertEqual(requests[0]["path"], "/api/v1/api/messages/status/id%2Fa")
        for response in [None, [], {}, {"status": "DELIVERED", "messageId": "wrong"}]:
            base_url, _ = self.server(response)
            with self.assertRaises(SmsError):
                self.client("notify-africa", base_url=base_url).get_status("id")

    def test_briq_webhook_verifies_original_bytes(self):
        client = self.client("briq", webhook_secret="webhook-secret")
        for event, status in [("sms.sent", "sent"), ("sms.delivered", "delivered"), ("sms.expired", "expired"), ("sms.failed", "failed"), ("future.event", "unknown")]:
            body = json.dumps({"id": "evt_1", "event": event, "data": {"message_id": "msg-1", "job_id": "job-1"}}).encode()
            signature = "sha256=" + hmac.new(b"webhook-secret", body, hashlib.sha256).hexdigest()
            result = client.parse_webhook(body, signature)
            self.assertEqual(result.status, status)
            self.assertEqual(result.event_id, "evt_1")
            for invalid_body, invalid_signature in [(body + b" ", signature), (body, None), (body, ""), (body, "sha256=nope"), (body, "sha256=" + "0" * 64)]:
                with self.assertRaises(ValueError):
                    client.parse_webhook(invalid_body, invalid_signature)
        with self.assertRaises(ValueError):
            client.parse_webhook("{}")
        with self.assertRaises(ValueError):
            self.client("briq").parse_webhook(b"{}", "")

    def test_nextsms_webhook(self):
        client = self.client("nextsms", webhook_token="secure-token")
        body = json.dumps({"token": "secure-token", "messageId": "next-1", "status": "EXPIRED"}).encode()
        self.assertEqual(client.parse_webhook(body).status, "expired")
        for event in [{"token": "wrong"}, {"token": "secure-token"}, None, []]:
            with self.assertRaises(ValueError):
                client.parse_webhook(json.dumps(event).encode())
        with self.assertRaises(ValueError):
            client.parse_webhook(b"invalid")
        with self.assertRaises(ValueError):
            self.client().parse_webhook(body)

    def test_normalizes_status_without_losing_provider_value(self):
        for raw, status in [("DELIVERED", "delivered"), ("ENROUTE (SENT)", "sent"), ("REJECTED_NETWORK", "failed"), ("FUTURE_STATUS", "unknown")]:
            base_url, _ = self.server({"messages": [{"messageId": "id", "status": {"name": raw}}]})
            result = self.client("nextsms", base_url=base_url).send(**MESSAGE)
            self.assertEqual(result.status, status)
            self.assertEqual(result.provider_status, raw)
            self.assertEqual(result.message_id, "id")

    def test_lookup_failure_is_not_an_unknown_send(self):
        base_url, _ = self.server({}, 503)
        with self.assertRaises(SmsError) as caught:
            self.client("notify-africa", base_url=base_url).get_status("id")
        self.assertFalse(caught.exception.outcome_unknown)
        with self.assertRaises(ValueError):
            self.client("briq").get_status("id")


if __name__ == "__main__":
    unittest.main()

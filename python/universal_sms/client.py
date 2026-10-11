from __future__ import annotations

import base64
import hashlib
import hmac
import http.client
import json
import math
import re
from dataclasses import dataclass
from typing import Literal
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener
from uuid import uuid4

Provider = Literal["beem", "briq", "nextsms", "notify-africa"]
SmsStatus = Literal["accepted", "sent", "delivered", "expired", "failed", "unknown"]

_BASE_URLS = {
    "beem": "https://apisms.beem.africa",
    "briq": "https://karibu.briq.tz",
    "nextsms": "https://messaging-service.co.tz",
    "notify-africa": "https://api.notify.africa",
}
_STATUS_GROUPS: dict[SmsStatus, tuple[str, ...]] = {
    "accepted": ("ACCEPTED", "QUEUED", "PROCESSING", "PENDING", "PENDING_DISPATCHED", "PENDING_WAITING_DELIVERY"),
    "sent": ("SENT", "ENROUTE (SENT)"),
    "delivered": ("DELIVERED",),
    "expired": ("EXPIRED",),
    "failed": ("FAILED", "UNDELIVERABLE", "UNDELIVERED", "REJECTED", "DELETED", "SKIPPED", "REJECTED_NETWORK", "REJECTED_DND"),
}


def _required(value: str | None, name: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{name} must be a non-empty string")
    return value


def _phone(value: str | None) -> str:
    number = _required(value, "to")
    if not re.fullmatch(r"\+?[1-9][0-9]{7,14}", number):
        raise ValueError("to must be an international number, e.g. +255712345678")
    return number.removeprefix("+")


def _base_url(value: str) -> str:
    _required(value, "base_url")
    url = urlsplit(value)
    local = url.hostname in ("localhost", "127.0.0.1", "::1")
    if (not url.hostname or (url.scheme != "https" and not (url.scheme == "http" and local))
            or url.username or url.password or url.query or url.fragment
            or any(ord(char) <= 32 for char in value)):
        raise ValueError("base_url must use HTTPS (HTTP is allowed only on loopback) without credentials, query or fragment")
    return value.rstrip("/")


def _identifier(value: object) -> str | None:
    if isinstance(value, str) and value.strip():
        return value
    if type(value) is int and 0 <= value <= 9007199254740991:
        return str(value)
    return None


def _object(value: object) -> dict:
    return value if isinstance(value, dict) else {}


@dataclass(frozen=True)
class SmsResult:
    provider: Provider
    message_id: str | None
    correlation_id: str | None
    status: SmsStatus
    provider_status: str
    event_id: str | None = None


def _result(provider: Provider, message_id: object, correlation_id: object, raw_status: object) -> SmsResult:
    provider_status = raw_status if isinstance(raw_status, str) else "UNKNOWN"
    normalized = provider_status.strip().upper()
    status = next((name for name, values in _STATUS_GROUPS.items() if normalized in values), "unknown")
    return SmsResult(provider, _identifier(message_id), _identifier(correlation_id), status, provider_status)


class SmsError(Exception):
    """A provider failure. Reconcile before retrying if outcome_unknown is True."""

    def __init__(self, provider: Provider, message: str, *, status_code: int | None = None,
                 outcome_unknown: bool = False) -> None:
        super().__init__(f"{provider}: {message}")
        self.provider = provider
        self.status_code = status_code
        self.outcome_unknown = outcome_unknown


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Never replay a paid send or forward provider credentials to a redirect target.
        return None


class SmsClient:
    def __init__(self, provider: Provider, *, api_key: str | None = None,
                 secret_key: str | None = None, api_token: str | None = None,
                 sender_id: str | None = None, app_id: str | None = None,
                 webhook_secret: str | None = None, webhook_token: str | None = None,
                 base_url: str | None = None, delivery_reports_base_url: str | None = None,
                 timeout: float = 30.0) -> None:
        if provider not in _BASE_URLS:
            raise ValueError("provider must be beem, briq, nextsms or notify-africa")
        if isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0:
            raise ValueError("timeout must be a positive finite number of seconds")
        self._provider = provider
        self._sender_id = sender_id
        self._webhook_secret = webhook_secret
        self._webhook_token = webhook_token
        self._timeout = timeout
        self._base_url = _base_url(base_url if base_url is not None else _BASE_URLS[provider])
        self._delivery_base_url = _base_url(delivery_reports_base_url if delivery_reports_base_url is not None else "https://dlrapi.beem.africa")
        name = "api_token" if provider == "nextsms" else "api_key"
        credential = _required(api_token if provider == "nextsms" else api_key, name)
        if re.search(r"[\r\n]", credential):
            raise ValueError(f"{name} cannot contain newlines")
        self._headers = {"Accept": "application/json", "Content-Type": "application/json"}
        if provider == "beem":
            if ":" in credential:
                raise ValueError("Beem api_key cannot contain a colon")
            encoded = base64.b64encode(f"{credential}:{_required(secret_key, 'secret_key')}".encode()).decode()
            self._headers["Authorization"] = f"Basic {encoded}"
        elif provider == "briq":
            self._headers["X-API-Key"] = credential
            if app_id is not None:
                _required(app_id, "app_id")
                if re.search(r"[\r\n]", app_id):
                    raise ValueError("app_id cannot contain newlines")
                self._headers["X-App-ID"] = app_id
        else:
            self._headers["Authorization"] = f"Bearer {credential}"
        self._opener = build_opener(_NoRedirect())

    @property
    def provider(self) -> Provider:
        return self._provider

    def _request(self, method: str, path: str, payload: dict | None = None,
                 base_url: str | None = None) -> object:
        sending = method == "POST"
        body = json.dumps(payload).encode() if payload is not None else None
        request = Request(f"{base_url or self._base_url}{path}", data=body, headers=self._headers, method=method)
        try:
            with self._opener.open(request, timeout=self._timeout) as response:
                return json.load(response)
        except HTTPError as error:
            status_code = error.code
            error.close()
            raise SmsError(self.provider, "HTTP request failed", status_code=status_code,
                           outcome_unknown=sending and (status_code >= 500 or status_code == 408 or status_code < 400)) from None
        except (URLError, OSError, http.client.HTTPException):
            raise SmsError(self.provider, "network request failed", outcome_unknown=sending) from None
        except (ValueError, UnicodeError):
            raise SmsError(self.provider, "invalid or unreadable JSON response", outcome_unknown=sending) from None

    def send(self, *, to: str, text: str, sender_id: str | None = None,
             reference: str | None = None) -> SmsResult:
        destination = _phone(to)
        _required(text, "text")
        sender = _required(sender_id if sender_id is not None else self._sender_id, "sender_id")
        reference = str(uuid4()) if reference is None else _required(reference, "reference")
        if self.provider == "beem":
            # shortcut: Beem uses encoding 0; add Unicode encoding when expanding beyond the existing Java flow.
            response = _object(self._request("POST", "/v1/send", {
                "source_addr": sender, "encoding": 0, "schedule_time": "", "message": text,
                "recipients": [{"recipient_id": 1, "dest_addr": destination}],
            }))
            if response.get("successful") is False:
                raise SmsError(self.provider, "provider rejected the SMS")
            submission = _result(self.provider, response.get("request_id"), response.get("request_id"), "accepted")
        elif self.provider == "briq":
            response = _object(self._request("POST", "/v1/message/send-instant", {
                "content": text, "recipients": [destination], "sender_id": sender,
            }))
            if response.get("success") is False:
                raise SmsError(self.provider, "provider rejected the SMS")
            submission = _result(self.provider, None, response.get("job_id"), response.get("status", "accepted"))
        elif self.provider == "nextsms":
            response = _object(self._request("POST", "/api/sms/v2/text/single", {
                "from": sender, "to": destination, "text": text, "flash": 0, "reference": reference,
            }))
            messages = response.get("messages")
            message = _object(messages[0]) if isinstance(messages, list) and messages else {}
            submission = _result(self.provider, message.get("messageId"), reference, _object(message.get("status")).get("name"))
        else:
            response = _object(self._request("POST", "/api/v1/api/messages/send", {
                "phone_number": destination, "message": text, "sender_id": sender,
            }))
            data = _object(response.get("data"))
            submission = _result(self.provider, data.get("messageId"), None, data.get("status"))
        tracking_id = submission.correlation_id if self.provider == "briq" else submission.message_id
        if not tracking_id:
            raise SmsError(self.provider, "send response is missing its tracking ID; reconcile before resending", outcome_unknown=True)
        return submission

    def get_status(self, message_id: str, *, to: str | None = None) -> SmsResult:
        _required(message_id, "message_id")
        if self.provider == "beem":
            destination = _phone(to)
            query = urlencode({"dest_addr": destination, "request_id": message_id})
            reports = self._request("GET", f"/public/v1/delivery-reports?{query}", base_url=self._delivery_base_url)
            report = next((item for item in reports if isinstance(item, dict)
                           and item.get("dest_addr") == destination and _identifier(item.get("request_id")) == message_id), None) if isinstance(reports, list) else None
            if report is None or not isinstance(report.get("status"), str):
                raise SmsError(self.provider, "no matching delivery report")
            return _result(self.provider, message_id, None, report.get("status"))
        if self.provider == "notify-africa":
            response = self._request("GET", f"/api/v1/api/messages/status/{quote(message_id, safe='')}")
            data = _object(response).get("data", response)
            if (not isinstance(data, dict) or not isinstance(data.get("status"), str)
                    or ("messageId" in data and _identifier(data["messageId"]) != message_id)):
                raise SmsError(self.provider, "invalid or mismatched delivery report")
            return _result(self.provider, message_id, None, data["status"])
        raise ValueError(f"{self.provider} uses delivery webhooks; call parse_webhook")

    def parse_webhook(self, raw_body: bytes, signature: str | None = None) -> SmsResult:
        if self.provider not in ("briq", "nextsms"):
            raise ValueError(f"{self.provider} uses status polling; call get_status")
        if not isinstance(raw_body, bytes):
            raise ValueError("raw_body must be the original request bytes")
        if self.provider == "briq":
            secret = _required(self._webhook_secret, "webhook_secret")
            if not isinstance(signature, str) or not re.fullmatch(r"sha256=[a-f0-9]{64}", signature):
                raise ValueError("invalid webhook signature")
            expected = hmac.new(secret.encode(), raw_body, hashlib.sha256).hexdigest()
            if not hmac.compare_digest(expected, signature[7:]):
                raise ValueError("invalid webhook signature")
        try:
            event = json.loads(raw_body)
        except (ValueError, UnicodeError):
            raise ValueError("invalid webhook JSON") from None
        if not isinstance(event, dict):
            raise ValueError("invalid webhook JSON")
        if self.provider == "briq":
            raw_status = _required(event.get("event"), "event")
            statuses: dict[str, SmsStatus] = {"sms.sent": "sent", "sms.delivered": "delivered", "sms.expired": "expired", "sms.failed": "failed"}
            data = _object(event.get("data"))
            delivery = SmsResult(self.provider, _identifier(data.get("message_id")), _identifier(data.get("job_id")),
                                 statuses.get(raw_status, "unknown"), raw_status, _identifier(event.get("id")))
        else:
            token = _required(self._webhook_token, "webhook_token")
            supplied = event.get("token") if isinstance(event.get("token"), str) else ""
            if not hmac.compare_digest(token.encode(), supplied.encode()):
                raise ValueError("invalid webhook token")
            delivery = _result(self.provider, event.get("messageId"), None, event.get("status"))
        if not delivery.message_id and not delivery.correlation_id:
            raise ValueError("webhook is missing its tracking ID")
        return delivery

# Universal SMS SDK

One small API for **Beem, Briq, NextSMS V2 and Notify Africa**, in Node.js and Python. Use your existing provider account and credentials. No gateway deployment, database or runtime dependencies are required. These are independent community SDKs, not official provider SDKs.

## Install from GitHub

Node.js 20+ and Python 3.10+ are supported. Both installs require Git. Pin a release tag or commit for reproducible deployments, for example `#sdk-v0.1.0` in npm or `@sdk-v0.1.0` before the Python URL fragment once that tag exists.

```bash
npm install github:Johnndelembi/universal-sms-sdk
python -m pip install 'universal-sms-sdk @ git+https://github.com/Johnndelembi/universal-sms-sdk.git#subdirectory=python'
```

The repository's root `package.json` exposes the Node SDK. Python's `pyproject.toml` is in `python`. See [npm Git installs](https://docs.npmjs.com/cli/v11/commands/npm-install/) and [pip subdirectory installs](https://pip.pypa.io/en/stable/topics/vcs-support/).

## Send an SMS

Configure an approved sender ID and keep credentials in environment variables on your server.

```js
import { SmsClient, SmsError } from 'universal-sms-sdk';

const sms = new SmsClient({
  provider: 'beem',
  apiKey: process.env.BEEM_API_KEY,
  secretKey: process.env.BEEM_SECRET_KEY,
  senderId: process.env.SMS_SENDER_ID,
});

const message = await sms.send({ to: '+255712345678', text: 'Your order is ready.' });
console.log(message.messageId, message.status);
```

CommonJS also works: `const { SmsClient, SmsError } = require('universal-sms-sdk')`. TypeScript declarations ship with the package; credentials must be non-empty strings.

```python
import os
from universal_sms import SmsClient, SmsError

sms = SmsClient(
    "beem",
    api_key=os.environ["BEEM_API_KEY"],
    secret_key=os.environ["BEEM_SECRET_KEY"],
    sender_id=os.environ["SMS_SENDER_ID"],
)

message = sms.send(to="+255712345678", text="Your order is ready.")
print(message.message_id, message.status)
```

Node uses async methods. Python is synchronous and uses only the standard library; in an asyncio application call it with `await asyncio.to_thread(sms.send, to=..., text=...)`.

## Switch providers

Change the client configuration; keep your send call. Credentials are passed explicitly rather than loaded from guessed environment variable names.

| Provider | Node configuration | Python configuration | Delivery tracking |
|---|---|---|---|
| `beem` | `apiKey`, `secretKey` | `api_key`, `secret_key` | Status lookup requires the original destination |
| `briq` | `apiKey`, optional `appId` | `api_key`, optional `app_id` | Signed delivery webhooks |
| `nextsms` | `apiToken` | `api_token` | Token-verified delivery webhooks |
| `notify-africa` | `apiKey` | `api_key` | Status lookup |

Set `senderId` / `sender_id` on the client or on each send. International phone numbers may have a leading `+`; the SDK sends provider payloads with digits only. Local numbers, empty messages and missing credentials fail before an HTTP request.

```js
const sms = new SmsClient({ provider: 'briq', apiKey: process.env.BRIQ_API_KEY, senderId: 'MYAPP' });
const message = await sms.send({ to: '+255712345678', text: 'Hello' });
// Briq gives a job ID immediately; store message.correlationId.
```

```python
sms = SmsClient("nextsms", api_token=os.environ["NEXTSMS_API_TOKEN"], sender_id="MYAPP")
message = sms.send(to="+255712345678", text="Hello", reference="order-123")
```

## Results and errors

Node returns a plain object; Python returns an immutable typed `SmsResult`.

| Node | Python | Meaning |
|---|---|---|
| `provider` | `provider` | Selected provider |
| `messageId` | `message_id` | Provider message ID, or `null` / `None` for Briq's initial job response |
| `correlationId` | `correlation_id` | Briq job ID, Beem request ID or NextSMS reference; otherwise absent |
| `status` | `status` | `accepted`, `sent`, `delivered`, `expired`, `failed`, or `unknown` |
| `providerStatus` | `provider_status` | Original status string (Beem submission is normalized to `accepted`) |
| `eventId` | `event_id` | Briq webhook event ID, when supplied |

`accepted` and `sent` do not confirm handset delivery. A valid response carrying a rejected/failed delivery status returns `status: 'failed'` with its ID; inspect status as well as catching exceptions. Unknown provider status values remain `unknown`.

HTTP, network and malformed-response errors raise `SmsError`. Inspect `provider`, `statusCode` / `status_code` and `outcomeUnknown` / `outcome_unknown`. Input and webhook validation raises `TypeError` in Node and `ValueError` in Python. Error messages exclude credentials and provider response bodies.

```js
try {
  await sms.send({ to: '+255712345678', text: 'Hello' });
} catch (error) {
  if (!(error instanceof SmsError)) throw error;
  if (error.outcomeUnknown) {
    // Reconcile in provider logs or delivery tracking before sending again.
  } else {
    // Inspect error.statusCode and correct the request/account configuration.
  }
}
```

```python
try:
    sms.send(to="+255712345678", text="Hello")
except SmsError as error:
    if error.outcome_unknown:
        # Reconcile in provider logs or delivery tracking before sending again.
        pass
    else:
        # Inspect error.status_code and correct the request/account configuration.
        pass
```

There are **no automatic send retries, redirects or provider failover**. Timeout, disconnected connection, HTTP 408/5xx and malformed successful send responses may mean the provider already accepted and billed the message. NextSMS's optional `reference` is a correlation value, not a guaranteed idempotency key; a UUID is generated when omitted. Other providers do not receive that reference.

## Delivery lookup

Beem: wait at least five minutes before querying; retain both the returned ID and the destination.

```js
const delivery = await sms.getStatus(message.messageId, { to: '+255712345678' });
```

```python
delivery = sms.get_status(message.message_id, to="+255712345678")
```

Notify Africa: call `sms.getStatus(message.messageId)` / `sms.get_status(message.message_id)` without the destination. Briq and NextSMS status lookup is not implemented; use the webhook methods below.

## Delivery webhooks

Configure the webhook URL with the provider. Your application owns HTTP routing, persistence, deduplication and acknowledgement. Pass the **original raw request bytes**, not reserialized JSON.

```js
const sms = new SmsClient({
  provider: 'briq', apiKey: process.env.BRIQ_API_KEY,
  webhookSecret: process.env.BRIQ_WEBHOOK_SECRET,
});
// rawBody is your server framework's Buffer, before JSON parsing.
const event = sms.parseWebhook(rawBody, request.headers['x-briq-signature']);
// Deduplicate event.eventId and correlate using event.correlationId / event.messageId.
```

```python
sms = SmsClient("briq", api_key=os.environ["BRIQ_API_KEY"],
                webhook_secret=os.environ["BRIQ_WEBHOOK_SECRET"])
event = sms.parse_webhook(raw_body, signature_header)
```

Briq requires a configured signing secret and an HMAC-SHA256 signature. Unsigned webhooks are rejected. NextSMS uses `webhookToken` / `webhook_token` from the client configuration and verifies the JSON `token` field with a constant-time comparison. Call `parseWebhook(rawBody)` / `parse_webhook(raw_body)` for NextSMS. Verification authenticates a webhook; store event IDs or your message/status key to handle replayed delivery events.

## Configuration and scope

`baseUrl` / `base_url` overrides the sending API host. Beem's separate DLR host uses `deliveryReportsBaseUrl` / `delivery_reports_base_url`. URLs must use HTTPS, except loopback HTTP for local tests; URL credentials, queries and fragments are rejected. Do not use arbitrary user input for these settings.

Timeout defaults to 30 seconds: Node's `timeoutMs` is a request deadline in milliseconds; Python's `timeout` is a socket-operation timeout in seconds, including response reads, not a total elapsed deadline. TLS certificate verification remains enabled.

Version 0.1 covers the single-recipient flows already established by the Java adapters, with the same status vocabulary. Beem uses the existing `encoding: 0` flow; Unicode-specific encoding, provider-native batches, scheduling, balance queries, campaigns, OTP workflows and Python async transport are deferred. Provider/account restrictions on countries, sender IDs, content length and encoding still apply. Do not implement a bulk helper by silently looping and retrying paid sends.

Official references: [Beem SMS](https://docs.beem.africa/guides/sms/index), [Beem delivery reports](https://docs.beem.africa/guides/sms/delivery-reports), [Briq messaging](https://docs.briq.tz/guides/messaging), [Briq webhooks](https://docs.briq.tz/guides/webhooks), [NextSMS V2](https://documenter.getpostman.com/view/1679195/2sAYkDP1XN), [Notify Africa](https://docs.notify.africa/docs/api/sms/getting-started). Tests assert the repo's provider contracts; no live paid sends have been verified.

## Implementation plan and maintenance

1. **Done:** package two direct clients in an independent SDK repository, based on the existing messaging-api provider adapters.
2. **Done:** mirror all four send payloads/authentication schemes, normalize results, validate inputs and preserve ambiguous outcomes.
3. **Done:** implement Beem/Notify Africa status lookups and verified Briq/NextSMS webhook parsing.
4. **Done:** exercise both languages against the same provider fixtures and real local HTTP servers; package and smoke-test installed artifacts.
5. **Ready to run on GitHub:** SDK CI and tagged release packaging; follow [RELEASING.md](RELEASING.md).
6. **Before production release:** verify each adapter with an authorized test account, confirm approved senders/encoding, and compare real responses to fixtures. This remains a live-account check.

When provider contracts change, update both clients and the shared request fixtures together. Check the original Java adapters for drift too.

Run from this repository:

```bash
npm test
cd python
python -m unittest discover -s tests -v
```

MIT licensed; see [LICENSE](LICENSE).

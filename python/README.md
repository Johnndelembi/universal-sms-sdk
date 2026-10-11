# Universal SMS SDK for Python

One API for Beem, Briq, NextSMS V2 and Notify Africa. Python 3.10+, no runtime dependencies. Community SDK, MIT licensed.

Install from the GitHub repository (omit the tag until it exists):

```bash
python -m pip install 'universal-sms-sdk @ git+https://github.com/Johnndelembi/universal-sms-sdk.git@sdk-v0.1.0#subdirectory=python'
```

```python
import os
from universal_sms import SmsClient, SmsError

sms = SmsClient("beem", api_key=os.environ["BEEM_API_KEY"],
                secret_key=os.environ["BEEM_SECRET_KEY"],
                sender_id=os.environ["SMS_SENDER_ID"])
message = sms.send(to="+255712345678", text="Hello from Python")
print(message.message_id, message.status)
```

Choose `briq` with `api_key`, `nextsms` with `api_token`, or `notify-africa` with `api_key` to switch providers. Keep the same send call.

Beem and Notify Africa support `get_status(message_id)`; Beem also requires `to` and a wait of at least five minutes. Briq and NextSMS support `parse_webhook(raw_body, signature=None)` with configured `webhook_secret` (Briq) or `webhook_token` (NextSMS). Preserve original bytes for signature verification.

Methods are synchronous; use `asyncio.to_thread` inside an async application. Approved sender IDs and provider credentials are required. Beem currently uses encoding 0, as in the Java adapter; provider-specific Unicode encoding is deferred.

No automatic retries or failover. On `SmsError`, inspect `status_code` and `outcome_unknown`; reconcile ambiguous sends before resending to avoid duplicate messages and charges. References are correlation values, not guaranteed idempotency keys. `accepted`/`sent` does not confirm delivery.

The [repository README](https://github.com/Johnndelembi/universal-sms-sdk#readme) documents all provider options, results, tracking, configuration and limitations. [RELEASING.md](https://github.com/Johnndelembi/universal-sms-sdk/blob/main/RELEASING.md) documents GitHub releases and optional registry publication. The package name is not yet claimed or published to PyPI.

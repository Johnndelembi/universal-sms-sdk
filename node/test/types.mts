import { SmsClient, SmsError, type SmsResult } from 'universal-sms-sdk';

const beem = new SmsClient({ provider: 'beem', apiKey: 'key', secretKey: 'secret', senderId: 'MYAPP' });
const briq = new SmsClient({ provider: 'briq', apiKey: 'key', webhookSecret: 'secret' });
const next = new SmsClient({ provider: 'nextsms', apiToken: 'token' });
const notify = new SmsClient({ provider: 'notify-africa', apiKey: 'key' });
const sent: Promise<SmsResult> = beem.send({ to: '+255712345678', text: 'Hello' });
const delivery: Promise<SmsResult> = notify.getStatus('id');
const event: SmsResult = briq.parseWebhook(new Uint8Array(), 'signature');
const error: boolean = new SmsError('nextsms', 'failure').outcomeUnknown;
void [next, sent, delivery, event, error];

// @ts-expect-error Beem needs the secret key.
new SmsClient({ provider: 'beem', apiKey: 'key' });
// @ts-expect-error NextSMS V2 needs apiToken rather than apiKey.
new SmsClient({ provider: 'nextsms', apiKey: 'key' });
// @ts-expect-error Unsupported providers are rejected.
new SmsClient({ provider: 'other', apiKey: 'key' });

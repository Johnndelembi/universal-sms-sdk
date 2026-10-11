const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { once } = require('node:events');
const { createHmac } = require('node:crypto');
const { SmsClient, SmsError } = require('../index.cjs');
const contracts = require('../../tests/send-contracts.json');

async function server(t, response, status = 200) {
  const requests = [];
  const http = createServer(async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    requests.push({ path: req.url, headers: req.headers, method: req.method, payload: body ? JSON.parse(body) : null });
    res.writeHead(status, { 'Content-Type': 'application/json', Location: '/redirect-target' });
    if (response === 'disconnect') return res.destroy();
    if (response === 'stall') return; // Exercise the real request deadline.
    res.end(typeof response === 'string' ? response : JSON.stringify(response));
  });
  http.listen(0, '127.0.0.1');
  await once(http, 'listening');
  t.after(() => { http.closeAllConnections(); http.close(); });
  return { requests, baseUrl: `http://127.0.0.1:${http.address().port}` };
}

function client(provider, options = {}) {
  return new SmsClient({ provider, apiKey: 'key', secretKey: 'secret', apiToken: 'token', senderId: 'MYAPP', ...options });
}
const message = { to: '+255712345678', text: 'Hello', reference: 'order-1' };

for (const contract of contracts) {
  test(`${contract.provider}: request and normalized response match the shared contract`, async t => {
    const http = await server(t, contract.response);
    const result = await client(contract.provider, { baseUrl: http.baseUrl, appId: 'app' }).send(message);
    assert.equal(result.provider, contract.provider);
    for (const key of ['messageId', 'correlationId', 'status']) assert.equal(result[key], contract[key]);
    assert.equal(http.requests.length, 1);
    const request = http.requests[0];
    assert.equal(request.path, contract.path);
    assert.equal(request.method, 'POST');
    assert.deepEqual(request.payload, contract.payload);
    for (const [key, value] of Object.entries(contract.headers)) assert.equal(request.headers[key], value);
  });
}

test('validates inputs before sending and supports sender override', async t => {
  const http = await server(t, contracts[0].response);
  const sms = client('beem', { baseUrl: http.baseUrl });
  for (const invalid of [{ to: '0712345678' }, { to: 255712345678 }, { text: ' ' }, { senderId: '' }, { reference: '' }]) {
    await assert.rejects(sms.send({ ...message, ...invalid }), TypeError);
  }
  assert.equal(http.requests.length, 0);
  await sms.send({ ...message, to: '255712345678', senderId: 'OVERRIDE' });
  assert.equal(http.requests[0].payload.source_addr, 'OVERRIDE');
});

test('validates configuration and keeps credentials out of serialization', () => {
  for (const options of [{ provider: 'unknown' }, { apiKey: '' }, { secretKey: '' }, { timeoutMs: 0 }, { timeoutMs: Infinity }, { baseUrl: 'http://example.com' }, { baseUrl: 'https://key:secret@example.com' }, { apiKey: 'key\r\nInjected: true' }]) {
    assert.throws(() => client('beem', options), TypeError);
  }
  assert.equal(JSON.stringify(client('beem')), '{}');
});

test('NextSMS generates a reference when omitted', async t => {
  const http = await server(t, contracts[2].response);
  const result = await client('nextsms', { baseUrl: http.baseUrl }).send({ to: message.to, text: message.text });
  assert.match(result.correlationId, /^[a-f0-9-]{36}$/);
  assert.equal(result.correlationId, http.requests[0].payload.reference);
});

for (const [code, unknown] of [[400, false], [401, false], [429, false], [408, true], [503, true], [307, true]]) {
  test(`HTTP ${code}: preserves outcome uncertainty and never retries or follows redirects`, async t => {
    const http = await server(t, { secret: 'do-not-log' }, code);
    await assert.rejects(client('beem', { baseUrl: http.baseUrl }).send(message), error => {
      assert.ok(error instanceof SmsError);
      assert.equal(error.statusCode, code);
      assert.equal(error.outcomeUnknown, unknown);
      assert.doesNotMatch(String(error), /do-not-log/);
      return true;
    });
    assert.equal(http.requests.length, 1);
  });
}

for (const response of ['not-json', 'disconnect', 'stall', {}, [], null, { request_id: {} }]) {
  test(`ambiguous send response ${JSON.stringify(response)} requires reconciliation`, async t => {
    const http = await server(t, response);
    await assert.rejects(client('beem', { baseUrl: http.baseUrl, timeoutMs: 100 }).send(message), error => error instanceof SmsError && error.outcomeUnknown);
    assert.equal(http.requests.length, 1);
  });
}

test('explicit provider rejection has a known outcome', async t => {
  for (const [provider, response] of [['beem', { successful: false, request_id: 1 }], ['briq', { success: false, job_id: '1' }]]) {
    const http = await server(t, response);
    await assert.rejects(client(provider, { baseUrl: http.baseUrl }).send(message), error => error instanceof SmsError && !error.outcomeUnknown);
  }
});

test('Beem lookup uses the DLR host, destination and request ID and selects the matching report', async t => {
  const http = await server(t, [{ dest_addr: '255700000001', request_id: '31951', status: 'DELIVERED' }, { dest_addr: '255712345678', request_id: 31951, status: 'UNDELIVERED' }]);
  const sms = client('beem', { deliveryReportsBaseUrl: http.baseUrl });
  await assert.rejects(sms.getStatus('31951'), TypeError);
  const result = await sms.getStatus('31951', { to: message.to });
  assert.equal(result.status, 'failed');
  assert.equal(http.requests[0].path, '/public/v1/delivery-reports?dest_addr=255712345678&request_id=31951');
  assert.equal(http.requests[0].headers.authorization, 'Basic a2V5OnNlY3JldA==');
});

test('unmatched Beem report is a lookup failure', async t => {
  const http = await server(t, []);
  await assert.rejects(client('beem', { deliveryReportsBaseUrl: http.baseUrl }).getStatus('31951', { to: message.to }), error => error instanceof SmsError && !error.outcomeUnknown);
});

test('Notify Africa lookup encodes identifiers and validates the response', async t => {
  for (const response of [{ data: { messageId: 'id/a', status: 'DELIVERED' } }, { status: 'DELIVERED' }]) {
    const http = await server(t, response);
    assert.equal((await client('notify-africa', { baseUrl: http.baseUrl }).getStatus('id/a')).status, 'delivered');
    assert.equal(http.requests[0].path, '/api/v1/api/messages/status/id%2Fa');
  }
  for (const response of [null, [], {}, { status: 'DELIVERED', messageId: 'wrong' }]) {
    const http = await server(t, response);
    await assert.rejects(client('notify-africa', { baseUrl: http.baseUrl }).getStatus('id'), SmsError);
  }
});

test('Briq verifies original bytes and retains the event ID for deduplication', () => {
  const sms = client('briq', { webhookSecret: 'webhook-secret' });
  for (const [event, status] of [['sms.sent', 'sent'], ['sms.delivered', 'delivered'], ['sms.expired', 'expired'], ['sms.failed', 'failed'], ['future.event', 'unknown']]) {
    const body = Buffer.from(JSON.stringify({ id: 'evt_1', event, data: { message_id: 'msg-1', job_id: 'job-1' } }));
    const signature = `sha256=${createHmac('sha256', 'webhook-secret').update(body).digest('hex')}`;
    assert.equal(sms.parseWebhook(body, signature).status, status);
    assert.equal(sms.parseWebhook(body, signature).eventId, 'evt_1');
    assert.throws(() => sms.parseWebhook(Buffer.concat([body, Buffer.from(' ')]), signature), /signature/);
    for (const invalid of [undefined, '', 'sha256=nope', `sha256=${'0'.repeat(64)}`]) assert.throws(() => sms.parseWebhook(body, invalid), /signature/);
  }
  assert.throws(() => sms.parseWebhook('{}'), /bytes/);
  assert.throws(() => client('briq').parseWebhook(Buffer.from('{}'), ''), /webhookSecret/);
});

test('NextSMS verifies its webhook token before returning delivery state', () => {
  const sms = client('nextsms', { webhookToken: 'secure-token' });
  const body = Buffer.from(JSON.stringify({ token: 'secure-token', messageId: 'next-1', status: 'EXPIRED' }));
  assert.equal(sms.parseWebhook(body).status, 'expired');
  for (const event of [{ token: 'wrong' }, { token: 'secure-token' }, null, []]) assert.throws(() => sms.parseWebhook(Buffer.from(JSON.stringify(event))), TypeError);
  assert.throws(() => sms.parseWebhook(Buffer.from('invalid')), /JSON/);
  assert.throws(() => client('beem').parseWebhook(body), /polling/);
});

test('unknown provider statuses stay unknown and failed sends preserve their tracking ID', async t => {
  for (const [raw, status] of [['DELIVERED', 'delivered'], ['ENROUTE (SENT)', 'sent'], ['REJECTED_NETWORK', 'failed'], ['FUTURE_STATUS', 'unknown']]) {
    const http = await server(t, { messages: [{ messageId: 'id', status: { name: raw } }] });
    const result = await client('nextsms', { baseUrl: http.baseUrl }).send(message);
    assert.equal(result.status, status);
    assert.equal(result.providerStatus, raw);
    assert.equal(result.messageId, 'id');
  }
});

test('status lookup failure cannot imply a new send', async t => {
  const http = await server(t, {}, 503);
  await assert.rejects(client('notify-africa', { baseUrl: http.baseUrl }).getStatus('id'), error => error instanceof SmsError && !error.outcomeUnknown);
  await assert.rejects(client('briq').getStatus('id'), /webhooks/);
});

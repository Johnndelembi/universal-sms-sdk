'use strict';

const { randomUUID, createHmac, timingSafeEqual } = require('node:crypto');

const BASE_URLS = Object.freeze({
  beem: 'https://apisms.beem.africa',
  briq: 'https://karibu.briq.tz',
  nextsms: 'https://messaging-service.co.tz',
  'notify-africa': 'https://api.notify.africa',
});
const STATUS_GROUPS = {
  accepted: ['ACCEPTED', 'QUEUED', 'PROCESSING', 'PENDING', 'PENDING_DISPATCHED', 'PENDING_WAITING_DELIVERY'],
  sent: ['SENT', 'ENROUTE (SENT)'],
  delivered: ['DELIVERED'],
  expired: ['EXPIRED'],
  failed: ['FAILED', 'UNDELIVERABLE', 'UNDELIVERED', 'REJECTED', 'DELETED', 'SKIPPED', 'REJECTED_NETWORK', 'REJECTED_DND'],
};

function required(value, name) {
  if (typeof value !== 'string' || !value.trim()) throw new TypeError(`${name} must be a non-empty string`);
  return value;
}

function phone(value) {
  required(value, 'to');
  if (!/^\+?[1-9][0-9]{7,14}$/.test(value)) throw new TypeError('to must be an international number, e.g. +255712345678');
  return value.replace(/^\+/, '');
}

function baseUrl(value) {
  required(value, 'baseUrl');
  const url = new URL(value);
  const local = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  if ((url.protocol !== 'https:' && !(url.protocol === 'http:' && local)) || url.username || url.password || url.search || url.hash) {
    throw new TypeError('baseUrl must use HTTPS (HTTP is allowed only on loopback) without credentials, query or fragment');
  }
  return url.href.replace(/\/$/, '');
}

function identifier(value) {
  if (typeof value === 'string' && value.trim()) return value;
  if (Number.isSafeInteger(value) && value >= 0) return String(value);
  return null;
}

function result(provider, messageId, correlationId, rawStatus) {
  const providerStatus = typeof rawStatus === 'string' ? rawStatus : 'UNKNOWN';
  const normalized = providerStatus.trim().toUpperCase();
  const status = Object.entries(STATUS_GROUPS).find(([, values]) => values.includes(normalized))?.[0] || 'unknown';
  return { provider, messageId: identifier(messageId), correlationId: identifier(correlationId), status, providerStatus };
}

class SmsError extends Error {
  constructor(provider, message, { statusCode = null, outcomeUnknown = false } = {}) {
    super(`${provider}: ${message}`);
    this.name = 'SmsError';
    this.provider = provider;
    this.statusCode = statusCode;
    this.outcomeUnknown = outcomeUnknown;
  }
}

class SmsClient {
  #options;
  #provider;
  #baseUrl;
  #deliveryBaseUrl;
  #headers;

  constructor(options) {
    if (!options || !Object.hasOwn(BASE_URLS, options.provider)) throw new TypeError('provider must be beem, briq, nextsms or notify-africa');
    this.#options = { timeoutMs: 30000, ...options };
    this.#provider = options.provider;
    const config = this.#options;
    if (!Number.isInteger(config.timeoutMs) || config.timeoutMs <= 0 || config.timeoutMs > 2147483647) throw new TypeError('timeoutMs must be a positive integer up to 2147483647');
    this.#baseUrl = baseUrl(config.baseUrl ?? BASE_URLS[this.#provider]);
    this.#deliveryBaseUrl = baseUrl(config.deliveryReportsBaseUrl ?? 'https://dlrapi.beem.africa');
    const credentialName = this.#provider === 'nextsms' ? 'apiToken' : 'apiKey';
    const credential = required(config[credentialName], credentialName);
    if (/[\r\n]/.test(credential)) throw new TypeError(`${credentialName} cannot contain newlines`);
    this.#headers = { Accept: 'application/json', 'Content-Type': 'application/json' };
    if (this.#provider === 'beem') {
      if (credential.includes(':')) throw new TypeError('Beem apiKey cannot contain a colon');
      this.#headers.Authorization = `Basic ${Buffer.from(`${credential}:${required(config.secretKey, 'secretKey')}`).toString('base64')}`;
    } else if (this.#provider === 'briq') {
      this.#headers['X-API-Key'] = credential;
      if (config.appId !== undefined) {
        required(config.appId, 'appId');
        if (/[\r\n]/.test(config.appId)) throw new TypeError('appId cannot contain newlines');
        this.#headers['X-App-ID'] = config.appId;
      }
    } else {
      this.#headers.Authorization = `Bearer ${credential}`;
    }
  }

  get provider() { return this.#provider; }

  async #request(method, path, payload, base = this.#baseUrl) {
    const sending = method === 'POST';
    let response;
    try {
      response = await fetch(`${base}${path}`, {
        method, headers: this.#headers,
        body: payload === undefined ? undefined : JSON.stringify(payload),
        signal: AbortSignal.timeout(this.#options.timeoutMs), redirect: 'manual',
      });
      if (!response.ok) {
        await response.body?.cancel();
        throw new SmsError(this.#provider, 'HTTP request failed', {
          statusCode: response.status,
          outcomeUnknown: sending && (response.status >= 500 || response.status === 408 || response.status < 400),
        });
      }
      return await response.json();
    } catch (error) {
      if (error instanceof SmsError) throw error;
      throw new SmsError(this.#provider, response ? 'invalid or unreadable JSON response' : 'network request failed', { outcomeUnknown: sending });
    }
  }

  async send({ to, text, senderId, reference = randomUUID() }) {
    const destination = phone(to);
    required(text, 'text');
    const sender = required(senderId ?? this.#options.senderId, 'senderId');
    required(reference, 'reference');
    let response;
    let submission;
    switch (this.#provider) {
      case 'beem':
        // shortcut: Beem uses encoding 0; add Unicode encoding when expanding beyond the existing Java flow.
        response = await this.#request('POST', '/v1/send', {
          source_addr: sender, encoding: 0, schedule_time: '', message: text,
          recipients: [{ recipient_id: 1, dest_addr: destination }],
        });
        if (response?.successful === false) throw new SmsError(this.#provider, 'provider rejected the SMS');
        submission = result(this.#provider, response?.request_id, response?.request_id, 'accepted');
        break;
      case 'briq':
        response = await this.#request('POST', '/v1/message/send-instant', { content: text, recipients: [destination], sender_id: sender });
        if (response?.success === false) throw new SmsError(this.#provider, 'provider rejected the SMS');
        submission = result(this.#provider, null, response?.job_id, response?.status ?? 'accepted');
        break;
      case 'nextsms': {
        response = await this.#request('POST', '/api/sms/v2/text/single', { from: sender, to: destination, text, flash: 0, reference });
        const message = Array.isArray(response?.messages) ? response.messages[0] : undefined;
        submission = result(this.#provider, message?.messageId, reference, message?.status?.name);
        break;
      }
      case 'notify-africa':
        response = await this.#request('POST', '/api/v1/api/messages/send', { phone_number: destination, message: text, sender_id: sender });
        submission = result(this.#provider, response?.data?.messageId, null, response?.data?.status);
        break;
    }
    if (this.#provider === 'briq' ? !submission.correlationId : !submission.messageId) {
      throw new SmsError(this.#provider, 'send response is missing its tracking ID; reconcile before resending', { outcomeUnknown: true });
    }
    return submission;
  }

  async getStatus(messageId, { to } = {}) {
    required(messageId, 'messageId');
    if (this.#provider === 'beem') {
      const destination = phone(to);
      const query = new URLSearchParams({ dest_addr: destination, request_id: messageId });
      const reports = await this.#request('GET', `/public/v1/delivery-reports?${query}`, undefined, this.#deliveryBaseUrl);
      const report = Array.isArray(reports) && reports.find(item => item?.dest_addr === destination && identifier(item?.request_id) === messageId);
      if (!report || typeof report.status !== 'string') throw new SmsError(this.#provider, 'no matching delivery report');
      return result(this.#provider, messageId, null, report.status);
    }
    if (this.#provider === 'notify-africa') {
      const response = await this.#request('GET', `/api/v1/api/messages/status/${encodeURIComponent(messageId)}`);
      const data = response?.data ?? response;
      if (!data || typeof data !== 'object' || Array.isArray(data) || typeof data.status !== 'string' || (data.messageId !== undefined && identifier(data.messageId) !== messageId)) {
        throw new SmsError(this.#provider, 'invalid or mismatched delivery report');
      }
      return result(this.#provider, messageId, null, data.status);
    }
    throw new TypeError(`${this.#provider} uses delivery webhooks; call parseWebhook`);
  }

  parseWebhook(rawBody, signature) {
    if (!['briq', 'nextsms'].includes(this.#provider)) throw new TypeError(`${this.#provider} uses status polling; call getStatus`);
    if (!(rawBody instanceof Uint8Array)) throw new TypeError('rawBody must be the original request bytes');
    if (this.#provider === 'briq') {
      const secret = required(this.#options.webhookSecret, 'webhookSecret');
      if (typeof signature !== 'string' || !/^sha256=[a-f0-9]{64}$/.test(signature)) throw new TypeError('invalid webhook signature');
      const expected = createHmac('sha256', secret).update(rawBody).digest();
      if (!timingSafeEqual(expected, Buffer.from(signature.slice(7), 'hex'))) throw new TypeError('invalid webhook signature');
    }
    let event;
    try { event = JSON.parse(Buffer.from(rawBody).toString('utf8')); }
    catch { throw new TypeError('invalid webhook JSON'); }
    if (!event || typeof event !== 'object' || Array.isArray(event)) throw new TypeError('invalid webhook JSON');
    let delivery;
    if (this.#provider === 'briq') {
      const rawStatus = required(event.event, 'event');
      const status = { 'sms.sent': 'sent', 'sms.delivered': 'delivered', 'sms.expired': 'expired', 'sms.failed': 'failed' }[rawStatus] ?? 'unknown';
      delivery = { ...result(this.#provider, event.data?.message_id, event.data?.job_id, rawStatus), status, eventId: identifier(event.id) };
    } else {
      const token = required(this.#options.webhookToken, 'webhookToken');
      const supplied = typeof event.token === 'string' ? event.token : '';
      const expectedBytes = Buffer.from(token);
      const suppliedBytes = Buffer.from(supplied);
      if (expectedBytes.length !== suppliedBytes.length || !timingSafeEqual(expectedBytes, suppliedBytes)) throw new TypeError('invalid webhook token');
      delivery = result(this.#provider, event.messageId, null, event.status);
    }
    if (!delivery.messageId && !delivery.correlationId) throw new TypeError('webhook is missing its tracking ID');
    return delivery;
  }
}

exports.SmsClient = SmsClient;
exports.SmsError = SmsError;

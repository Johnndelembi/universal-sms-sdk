export type Provider = 'beem' | 'briq' | 'nextsms' | 'notify-africa';
export type SmsStatus = 'accepted' | 'sent' | 'delivered' | 'expired' | 'failed' | 'unknown';

interface CommonOptions {
  senderId?: string;
  baseUrl?: string;
  /** Request deadline in milliseconds, default 30000. */
  timeoutMs?: number;
}
export type SmsClientOptions = CommonOptions & (
  | { provider: 'beem'; apiKey: string; secretKey: string; deliveryReportsBaseUrl?: string }
  | { provider: 'briq'; apiKey: string; appId?: string; webhookSecret?: string }
  | { provider: 'nextsms'; apiToken: string; webhookToken?: string }
  | { provider: 'notify-africa'; apiKey: string }
);
export interface SmsResult {
  provider: Provider;
  /** Briq returns a job correlationId at submission; messageId is null. */
  messageId: string | null;
  correlationId: string | null;
  status: SmsStatus;
  providerStatus: string;
  /** Briq event ID, available on webhooks for application deduplication. */
  eventId?: string | null;
}
export class SmsError extends Error {
  constructor(provider: Provider, message: string, options?: { statusCode?: number | null; outcomeUnknown?: boolean });
  readonly provider: Provider;
  readonly statusCode: number | null;
  /** Reconcile the submission before retrying when true. */
  readonly outcomeUnknown: boolean;
}
export class SmsClient {
  constructor(options: SmsClientOptions);
  readonly provider: Provider;
  send(message: { to: string; text: string; senderId?: string; reference?: string }): Promise<SmsResult>;
  getStatus(messageId: string, options?: { to?: string }): Promise<SmsResult>;
  parseWebhook(rawBody: Uint8Array, signature?: string): SmsResult;
}

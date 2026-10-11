package io.github.johnndelembi.sms;

/** Delivery tracking IDs and status. Briq's initial response has only a correlation ID. */
public record SmsResult(String provider, String messageId, String correlationId,
                        String status, String providerStatus, String eventId) {}

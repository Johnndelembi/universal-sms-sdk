package io.github.johnndelembi.sms;

/** Reconcile before retrying when outcomeUnknown() is true. Provider bodies are not retained. */
public final class SmsException extends RuntimeException {
    private final String provider;
    private final Integer statusCode;
    private final boolean outcomeUnknown;

    public SmsException(String provider, String message, Integer statusCode, boolean outcomeUnknown) {
        super(provider + ": " + message);
        this.provider = provider;
        this.statusCode = statusCode;
        this.outcomeUnknown = outcomeUnknown;
    }

    public String provider() { return provider; }
    public Integer statusCode() { return statusCode; }
    public boolean outcomeUnknown() { return outcomeUnknown; }
}

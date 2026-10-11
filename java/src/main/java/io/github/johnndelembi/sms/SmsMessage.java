package io.github.johnndelembi.sms;

/** Optional sender override and NextSMS correlation reference. */
public record SmsMessage(String to, String text, String senderId, String reference) {
    public SmsMessage(String to, String text) { this(to, text, null, null); }
}

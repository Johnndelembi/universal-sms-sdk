# Java SDK

Requires Java 17+. This is a synchronous client built on JDK HttpClient and Jackson; no Spring dependency. See the [shared quickstart](../README.md) for all four providers, validation and delivery tracking.

## Install from GitHub with Maven

Add these blocks to your application's `pom.xml`:

```xml
<repositories>
  <repository><id>jitpack.io</id><url>https://jitpack.io</url></repository>
</repositories>
<dependencies>
  <dependency>
    <groupId>com.github.Johnndelembi.universal-sms-sdk</groupId>
    <artifactId>universal-sms-sdk-java</artifactId>
    <version>main-SNAPSHOT</version>
  </dependency>
</dependencies>
```

For Gradle (Kotlin DSL):

```kotlin
repositories {
    mavenCentral()
    maven { url = uri("https://jitpack.io") }
}
dependencies {
    implementation("com.github.Johnndelembi.universal-sms-sdk:universal-sms-sdk-java:main-SNAPSHOT")
}
```

[JitPack](https://docs.jitpack.io/) builds versions on demand. Pin a commit hash or release tag in place of `main-SNAPSHOT` for reproducible builds. Maven/Gradle resolve Jackson transitively; the downloaded SDK JAR alone is not a standalone executable.

## Use

```java
import io.github.johnndelembi.sms.SmsClient;
import io.github.johnndelembi.sms.SmsException;
import io.github.johnndelembi.sms.SmsMessage;

var sms = SmsClient.builder("nextsms")
    .apiToken(System.getenv("NEXTSMS_API_TOKEN"))
    .senderId("MYAPP")
    .build();

try {
    var result = sms.send(new SmsMessage("+255712345678", "Hello", null, "order-123"));
    System.out.println(result.messageId() + " " + result.status());
} catch (SmsException error) {
    if (error.outcomeUnknown()) {
        // Reconcile with provider logs or delivery tracking before resending.
    } else {
        // Inspect error.statusCode() and correct request/account configuration.
    }
}
```

`SmsMessage(to, text)` uses the client's sender ID and an automatically generated NextSMS reference. The four-argument record allows a per-message sender and reference; `null` selects defaults. The reference is not a guaranteed idempotency key. Invalid configuration, message inputs or webhook signatures throw `IllegalArgumentException` before an HTTP request. `accepted` does not confirm handset delivery.

For Beem: `.apiKey(...).secretKey(...)`; Briq: `.apiKey(...)` and optional `.appId(...)`; Notify Africa: `.apiKey(...)`. Use `.webhookSecret(...)` for Briq and `.webhookToken(...)` for NextSMS. Pass original raw bytes to webhook parsers.

## Local build alternative

```bash
git clone https://github.com/Johnndelembi/universal-sms-sdk.git
cd universal-sms-sdk
mvn -B install
mvn -B -f java/smoke/pom.xml test
```

After installation, depend on `io.github.johnndelembi:universal-sms-sdk-java:0.1.0` in projects on that machine. These coordinates are installed locally; they have not been published to Maven Central.

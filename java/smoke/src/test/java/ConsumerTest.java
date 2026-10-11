import io.github.johnndelembi.sms.SmsClient;
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.assertEquals;

class ConsumerTest {
    @Test void installedJarResolvesItsJsonDependency() {
        var client = SmsClient.builder("beem").apiKey("key").secretKey("secret").senderId("MYAPP").build();
        assertEquals("beem", client.provider());
    }
}

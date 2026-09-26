package io.overcast.compat.scenario;

import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import software.amazon.awssdk.auth.credentials.AwsBasicCredentials;
import software.amazon.awssdk.auth.credentials.StaticCredentialsProvider;
import software.amazon.awssdk.http.apache.ApacheHttpClient;
import software.amazon.awssdk.regions.Region;
import software.amazon.awssdk.services.sqs.SqsClient;
import software.amazon.awssdk.services.sqs.model.QueueAttributeName;

import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The AWS SDK for Java v2 facts {@code cmd/compatgen}'s Java emitter derives
 * from the pinned model rather than from the SDK, measured on the wire.
 *
 * <p>{@code docs/plans/compat-coverage-modelgen.md} §3.2 lets a typed backend
 * skip the SDK lookup the Go emitter needs "wherever the SDK's nullability is
 * not derivable from the model", and says it is not derivable for Java. This
 * test is what makes that a measurement instead of a claim, and it is the reason
 * {@code emit_java_spell.go} carries no counterpart to the Go emitter's
 * zero-value refusal. If a future SDK ever changed either answer, the emitter
 * would start writing requests that quietly omit a member — a silent wrong
 * result in every generated group — and this fails first.
 *
 * <p>It talks to a loopback HTTP server from the JDK rather than to Overcast: the
 * point is what the SDK <em>serializes</em>, so the emulator would only add a
 * dependency and a port.
 */
class JavaSdkWireFactsTest {

    private HttpServer server;
    private SqsClient sqs;
    private final AtomicReference<String> body = new AtomicReference<>("");

    @BeforeEach
    void start() throws Exception {
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            body.set(new String(exchange.getRequestBody().readAllBytes(), StandardCharsets.UTF_8));
            byte[] out = "{}".getBytes(StandardCharsets.UTF_8);
            exchange.getResponseHeaders().add("Content-Type", "application/x-amz-json-1.0");
            exchange.sendResponseHeaders(200, out.length);
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(out);
            }
        });
        server.start();
        sqs = SqsClient.builder()
                .endpointOverride(URI.create("http://127.0.0.1:" + server.getAddress().getPort()))
                .region(Region.US_EAST_1)
                .credentialsProvider(StaticCredentialsProvider.create(AwsBasicCredentials.create("t", "t")))
                .httpClient(ApacheHttpClient.create())
                .build();
    }

    @AfterEach
    void stop() {
        if (sqs != null) {
            sqs.close();
        }
        if (server != null) {
            server.stop(0);
        }
    }

    /**
     * A builder setter takes the value whatever the member's optionality, and a
     * boxed zero really is serialized — which is why the Java emitter needs no
     * SDK lookup and refuses no zero.
     *
     * <p>The Go emitter has to refuse the same scenario: smithy-go gives
     * {@code VisibilityTimeout} a plain {@code int32} and serializes it only
     * when it differs from the zero value, so a scenario asking for 0 there
     * silently asks for the queue's own timeout ({@code compat/model/README.md}
     * § Values). The two backends genuinely differ, and this is the measurement
     * that says so.
     */
    @Test
    void aBoxedZeroIsSent() {
        sqs.receiveMessage(r -> r.queueUrl("http://q/x")
                .visibilityTimeout(0)
                .waitTimeSeconds(0)
                .maxNumberOfMessages(1));
        assertTrue(body.get().contains("\"VisibilityTimeout\":0"), body.get());
        assertTrue(body.get().contains("\"WaitTimeSeconds\":0"), body.get());

        // And an unset member is still omitted, which is what makes null the
        // SDK's spelling of "unset" and 0 an ordinary value.
        body.set("");
        sqs.receiveMessage(r -> r.queueUrl("http://q/x").maxNumberOfMessages(1));
        assertTrue(body.get().contains("\"MaxNumberOfMessages\":1"), body.get());
        assertEquals(-1, body.get().indexOf("VisibilityTimeout"), body.get());
    }

    /**
     * The String form of an enum member sends the value it was given, unchanged
     * — for a list of enums and an enum-keyed map alike. Those, plus the
     * same-named overload a scalar enum has, are the three shapes the emitter
     * spells; {@code javaSpeller.setterFor} chooses between them.
     */
    @Test
    void enumsAreSentAsTheirWireValues() {
        sqs.getQueueAttributes(r -> r.queueUrl("http://q/x")
                .attributeNamesWithStrings(List.of("QueueArn")));
        assertTrue(body.get().contains("\"AttributeNames\":[\"QueueArn\"]"), body.get());

        body.set("");
        sqs.setQueueAttributes(r -> r.queueUrl("http://q/x")
                .attributesWithStrings(Map.of("VisibilityTimeout", "30")));
        assertTrue(body.get().contains("\"VisibilityTimeout\":\"30\""), body.get());
    }

    /**
     * Why the emitter spells an enum as its wire value rather than through
     * {@code Enum.fromValue}: a value the <em>pinned</em> SDK does not know
     * becomes {@code UNKNOWN_TO_SDK_VERSION}, whose {@code toString} is the
     * four-character string {@code "null"} — and that is what reaches the wire.
     * It is not a compile error, so nothing but this measurement stands between
     * a shape snapshot newer than the pin and a request carrying "null" where
     * the scenario asked for a value.
     *
     * <p>The String form has no such failure mode, which the second half
     * asserts against the same unknown value: the SDK is not consulted about
     * it at all.
     */
    @Test
    void fromValueLosesAnEnumValueThePinnedSdkDoesNotKnow() {
        assertEquals(QueueAttributeName.UNKNOWN_TO_SDK_VERSION,
                QueueAttributeName.fromValue("NoSuchAttributeInThisSdk"));
        assertEquals("null", QueueAttributeName.UNKNOWN_TO_SDK_VERSION.toString(),
                "the unknown constant stringifies to the four characters \"null\", not to a Java null");

        sqs.getQueueAttributes(r -> r.queueUrl("http://q/x")
                .attributeNames(List.of(QueueAttributeName.fromValue("NoSuchAttributeInThisSdk"))));
        assertTrue(body.get().contains("\"AttributeNames\":[\"null\"]"),
                "through fromValue an unknown enum value reaches the wire as \"null\": " + body.get());

        // The spelling the emitter actually writes, given the same value.
        body.set("");
        sqs.getQueueAttributes(r -> r.queueUrl("http://q/x")
                .attributeNamesWithStrings(List.of("NoSuchAttributeInThisSdk")));
        assertTrue(body.get().contains("\"AttributeNames\":[\"NoSuchAttributeInThisSdk\"]"),
                "the String form must pass the modeled value straight through: " + body.get());
    }
}

package io.overcast.compat.harness;

import org.junit.jupiter.api.Test;

import software.amazon.awssdk.awscore.exception.AwsErrorDetails;
import software.amazon.awssdk.awscore.exception.AwsServiceException;
import software.amazon.awssdk.core.exception.SdkClientException;
import software.amazon.awssdk.http.SdkHttpFullResponse;
import software.amazon.awssdk.http.SdkHttpResponse;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * {@link Runner#isUnimplemented} classifies from the response, not the prose
 * (#1924).
 *
 * <p>The rule this replaced matched a bare {@code "501"} anywhere in the
 * exception's text, so a request id, an ARN, a resource name or a port was
 * enough to report a 400 as {@code unimplemented}. That is how the sibling
 * go-sdk suite flipped a gated baseline row on CI run 34064243252 and failed an
 * unrelated pull request.
 */
class UnimplementedClassificationTest {

    /**
     * The exception shape the AWS SDK v2 throws: the modeled error's code and
     * message in {@link AwsErrorDetails}, alongside the raw response the
     * exchange produced. Nothing here is a stand-in — this is what the Runner
     * reads on a real run.
     */
    private static AwsServiceException serviceException(
            String code, String message, int status, boolean unsupportedHeader) {
        SdkHttpFullResponse.Builder response = SdkHttpFullResponse.builder().statusCode(status);
        if (unsupportedHeader) {
            response.putHeader("x-emulator-unsupported", "true");
        }
        SdkHttpResponse http = response.build();
        return AwsServiceException.builder()
                .message(message)
                .statusCode(status)
                .requestId("5f2c9501-0f3a-4c7d-9a11-6b1d0c2e4a77")
                .awsErrorDetails(AwsErrorDetails.builder()
                        .errorCode(code)
                        .errorMessage(message)
                        .sdkHttpResponse(http)
                        .build())
                .build();
    }

    @Test
    void a400IsAFailureHoweverItsProseReads() {
        AwsServiceException e = serviceException("InvalidRequestException",
                "No Lambda rotation function ARN is associated with this secret.", 400, false);
        assertTrue(e.toString().contains("501"), "the fixture must carry the digits that caused the bug");
        assertFalse(Runner.isUnimplemented(e), "a 400 is a failure whatever its text contains");
    }

    @Test
    void a400WhoseResourceNameContains501IsAFailure() {
        AwsServiceException e = serviceException("ResourceNotFoundException",
                "Secrets Manager can't find the specified secret: oc-501abcde-rotate", 400, false);
        assertFalse(Runner.isUnimplemented(e));
    }

    @Test
    void aReal501IsUnimplemented() {
        AwsServiceException e = serviceException("NotImplemented",
                "This operation is not implemented by the emulator", 501, true);
        assertTrue(Runner.isUnimplemented(e));
    }

    @Test
    void a501NamedOnlyByItsHeaderIsUnimplemented() {
        // A body the SDK could not model into a status still arrives with the
        // header Overcast sets alongside every 501.
        AwsServiceException e = serviceException("", "", 200, true);
        assertTrue(Runner.isUnimplemented(e));
    }

    @Test
    void anUnknownOperationIsUnimplementedAt400() {
        AwsServiceException e = serviceException("UnknownOperationException",
                "Unknown operation: Frobnicate", 400, false);
        assertTrue(Runner.isUnimplemented(e));
    }

    @Test
    void anExceptionCarryingNoResponseFallsBackToTheText() {
        // Nothing to read but the message: the heuristic is all there is.
        assertTrue(Runner.isUnimplemented(
                SdkClientException.create("Unable to execute HTTP request: 501 Not Implemented")));
        assertFalse(Runner.isUnimplemented(
                SdkClientException.create("Unable to execute HTTP request: connection refused")));
    }
}

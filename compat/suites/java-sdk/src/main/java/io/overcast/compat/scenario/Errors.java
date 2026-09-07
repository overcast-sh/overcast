package io.overcast.compat.scenario;

import io.overcast.compat.harness.Runner;
import software.amazon.awssdk.awscore.exception.AwsServiceException;

import java.util.ArrayList;
import java.util.IdentityHashMap;
import java.util.List;
import java.util.Map;

/**
 * Error matching ({@code compat/model/README.md} § Errors).
 *
 * <p>A clause carries both the modeled shape and the wire code, because SDKs
 * disagree about which of the two they surface — for SQS's not-found,
 * {@code QueueDoesNotExist} and {@code AWS.SimpleQueueService.NonExistentQueue}
 * — so either is accepted, but by <b>equality</b> against a code parsed out of a
 * surface this SDK actually has, never by containment. Containment cannot tell a
 * code from a code that ends with it: a clause naming
 * {@code NotFoundException} would be satisfied by a
 * {@code ResourceNotFoundException}, which is a different error from a different
 * branch of the service, and by the word appearing anywhere in the SDK's prose.
 *
 * <p>The surfaces the AWS SDK for Java v2 gives us:
 *
 * <table>
 *   <caption>Error surfaces</caption>
 *   <tr><td>{@code AwsServiceException.awsErrorDetails().errorCode()}</td>
 *       <td>the code the protocol unmarshaller resolved — the AWS JSON
 *           protocols' {@code __type}, the REST JSON body's {@code code}, and
 *           the {@code Code} inside an XML error node: the Query protocol's
 *           {@code <ErrorResponse><Error><Code>} and REST XML's bare
 *           {@code <Error><Code>}. The {@code bodyType} and {@code bodyCode}
 *           carriers, which is why nothing in this class reads a body — the
 *           unmarshaller has already found the code at whichever depth the
 *           protocol states it.</td></tr>
 *   <tr><td>the exception's class simple name</td>
 *       <td>the class the SDK minted for a modeled error shape. The
 *           {@code exceptionName} carrier.</td></tr>
 *   <tr><td>{@code x-amzn-query-error}</td>
 *       <td>the header an {@code awsQueryCompatible} service sends, as
 *           {@code <code>;<Sender|Receiver>}, reached through the error details'
 *           own HTTP response. The {@code queryErrorHeader} carrier.</td></tr>
 * </table>
 *
 * <p><b>The Java exception name is the shape name plus {@code Exception}.</b>
 * SQS's {@code QueueDoesNotExist} is
 * {@code software.amazon.awssdk.services.sqs.model.QueueDoesNotExistException},
 * so this surface states the shape's own spelling only where the shape already
 * ends that way ({@code PolicyNotFoundException} does). Stripping a trailing
 * {@code Exception} to recover the other case is <b>not</b> done, and must not
 * be: it would make {@code ResourceNotFoundException} state
 * {@code ResourceNotFound} as well, which is exactly the near miss
 * {@code compat/model/testdata/errors/near-miss-longer-code.json} pins as a
 * non-match. The modeled shape reaches the matcher through {@code errorCode()}
 * instead, which is the surface that carries it.
 *
 * <p>When no surface states a code the clause does <b>not</b> match. There is no
 * containment fallback, and the absence of one is the rule rather than an
 * omission: an error with no code surface is no evidence that the service raised
 * the named error.
 */
final class Errors {

    private Errors() {}

    /** The header an {@code awsQueryCompatible} service returns beside the body. */
    private static final String QUERY_ERROR_HEADER = "x-amzn-query-error";

    /** The package prefix a class must sit under to count as a modeled exception. */
    private static final String MODEL_PACKAGE_PREFIX = "software.amazon.awssdk.services.";

    /** Reports whether a failed call carries the error a clause names. */
    static boolean matches(Throwable err, ErrorSpec want) {
        if (err == null || want == null) {
            return false;
        }
        for (String got : codes(err)) {
            if ((!want.shape().isEmpty() && got.equals(want.shape()))
                    || (!want.code().isEmpty() && got.equals(want.code()))) {
                return true;
            }
        }
        return false;
    }

    /**
     * Returns every code the error states, in every spelling a clause may name
     * it by, or an empty list when it states none.
     */
    static List<String> codes(Throwable err) {
        List<String> out = new ArrayList<>();
        for (Throwable e : chain(err)) {
            if (e instanceof AwsServiceException aws && aws.awsErrorDetails() != null) {
                add(out, aws.awsErrorDetails().errorCode());
                if (aws.awsErrorDetails().sdkHttpResponse() != null) {
                    add(out, aws.awsErrorDetails().sdkHttpResponse()
                            .firstMatchingHeader(QUERY_ERROR_HEADER).orElse(null));
                }
            }
            add(out, modeledExceptionName(e));
        }
        return out;
    }

    /**
     * One observed code in every spelling a clause may name it by, which is the
     * list {@code compat/model/README.md} § Errors fixes: the value itself, what
     * follows the last {@code #} of a Smithy id
     * ({@code com.amazonaws.sqs#QueueDoesNotExist} states the same code as
     * {@code QueueDoesNotExist}), and what precedes the first {@code ;} of the
     * {@code <code>;<fault>} form the {@code x-amzn-query-error} header uses.
     *
     * <p>Splitting at those separators and nowhere else is what keeps the match
     * an equality: no spelling of {@code ResourceNotFoundException} is
     * {@code NotFoundException}.
     */
    private static void add(List<String> out, String code) {
        if (code == null || code.isEmpty()) {
            return;
        }
        out.add(code);
        int hash = code.lastIndexOf('#');
        if (hash >= 0) {
            out.add(code.substring(hash + 1));
        }
        int semi = code.indexOf(';');
        if (semi >= 0) {
            out.add(code.substring(0, semi));
        }
    }

    /**
     * The {@code exceptionName} surface: the simple name of the class the SDK
     * generated for a modeled error shape.
     *
     * <p>Only a class from a generated service model package counts.
     * {@code AwsServiceException} and {@code SdkServiceException} are named after
     * what they are rather than after a modeled shape, and a clause naming one of
     * them must never be satisfied — which the package test excludes by
     * construction, since both live outside {@code services.*}.
     */
    static String modeledExceptionName(Throwable e) {
        Class<?> type = e.getClass();
        String pkg = type.getPackageName();
        if (!pkg.startsWith(MODEL_PACKAGE_PREFIX) || !pkg.endsWith(".model")) {
            return null;
        }
        return type.getSimpleName();
    }

    /**
     * Reports whether the emulator answered 501.
     *
     * <p>One rule serves both paths: {@link Runner#classifyResponse} reads the
     * response the SDK's exception carries, which is exact, and only an
     * exception carrying none falls back to the substring heuristic, which is
     * what that heuristic is for. The chain is walked here as well as there
     * because the SDK wraps a modeled exception in a {@code CompletionException}
     * in some paths, whose {@code getCause} chain the Runner already follows.
     */
    static boolean isUnimplemented(Throwable err) {
        for (Throwable e : chain(err)) {
            Boolean fromResponse = Runner.classifyResponse(e);
            if (fromResponse != null) return fromResponse;
        }
        return Runner.looksUnimplementedWithoutResponse(err.getMessage());
    }

    /**
     * Flattens an exception's cause chain. The AWS SDK wraps a modeled exception
     * in a {@code CompletionException} or an {@code SdkClientException} in some
     * paths, so the surfaces a clause reads can be a link or two down.
     */
    private static List<Throwable> chain(Throwable err) {
        List<Throwable> out = new ArrayList<>();
        Map<Throwable, Boolean> seen = new IdentityHashMap<>();
        for (Throwable e = err; e != null && seen.put(e, Boolean.TRUE) == null; e = e.getCause()) {
            out.add(e);
        }
        return out;
    }
}

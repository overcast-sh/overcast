package io.overcast.compat.scenario;

import software.amazon.awssdk.core.SdkBytes;
import software.amazon.awssdk.core.SdkField;
import software.amazon.awssdk.core.SdkPojo;
import software.amazon.awssdk.core.document.Document;
import software.amazon.awssdk.core.util.SdkAutoConstructList;
import software.amazon.awssdk.core.util.SdkAutoConstructMap;

import java.time.Instant;
import java.time.format.DateTimeFormatter;
import java.util.ArrayList;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * An AWS SDK response, as one of the IR's documents.
 *
 * <p>This is the one direction that still needs a conversion. A response is an
 * arbitrary SDK object and the assertions walk it by path, so nothing is known
 * about its shape until it arrives. The other direction — a value into an input
 * member — needs no conversion: {@code cmd/compatgen} resolves each member's
 * type from the model and writes the spelling into the emitted source, so only a
 * deferred expression reaches run time, through {@link Binder}.
 *
 * <p>It reflects on nothing. Every AWS SDK for Java v2 model class implements
 * {@link SdkPojo}, which lists the response's own {@link SdkField}s with the
 * <b>modeled member name</b> on each — so the document's keys are the names a
 * scenario path spells, taken from the SDK's own description of the shape rather
 * than from a Java field name this package would have to un-mangle.
 *
 * <p>Three choices are load-bearing:
 *
 * <ul>
 *   <li><b>null is absence, not null.</b> The Java SDK deserializes an omitted
 *       member and a JSON null to the same {@code null}, and
 *       {@code compat/model/README.md} § Paths settles which of the two that is:
 *       "{@code undefined} in an SDK's object model is absence, not a value". So
 *       an unset member is left out of the document rather than written as null,
 *       and {@code missing} holds for it while {@code nonEmpty} fails.</li>
 *   <li><b>An auto-construct list or map is absence too.</b> The SDK hands back
 *       {@link SdkAutoConstructList} for a list member the service omitted,
 *       which is an empty list that was never sent. Reporting it as {@code []}
 *       would make {@code missing} fail on an omitted page — the answer several
 *       AWS services legitimately give.</li>
 *   <li><b>Every number becomes a double.</b> That is what the interpreters'
 *       JSON parsers produce, so an {@code equals} on an integer member compares
 *       the same way here as it does there; {@link Json} renders an integral
 *       double without a fractional part so the two also print alike.</li>
 * </ul>
 */
final class Doc {

    private Doc() {}

    /** The sentinel for "this value is not present at all". */
    private static final Object ABSENT = new Object();

    /**
     * Converts an SDK value to the IR's document form: {@code Map<String,
     * Object>}, {@code List<Object>}, {@link String}, {@link Double},
     * {@link Boolean} or {@link Json#NULL}.
     *
     * @return the document, or {@code null} when the value is absent
     */
    static Object of(Object v) {
        Object doc = convert(v);
        return doc == ABSENT ? null : doc;
    }

    private static Object convert(Object v) {
        if (v == null) {
            return ABSENT;
        }
        if (v instanceof String s) {
            return s;
        }
        if (v instanceof Boolean b) {
            return b;
        }
        if (v instanceof Number n) {
            return n.doubleValue();
        }
        if (v instanceof Instant t) {
            // A timestamp is never compared by the IR, but it can sit on a
            // response a path walks past, so it is rendered rather than dropped.
            return DateTimeFormatter.ISO_INSTANT.format(t);
        }
        if (v instanceof SdkBytes bytes) {
            // A blob's document form in every backend is its standard base64
            // text (compat/model/README.md § Values): what an `equals` against
            // a `$base64` compares, and what an export of a blob puts in the
            // context bag for a later `$base64` around a $ref to decode.
            return Base64.getEncoder().encodeToString(bytes.asByteArray());
        }
        if (v instanceof Enum<?>) {
            // An enum member is stored as a string in every generated model
            // class, so this is a belt to that braces. The value is whatever
            // the constant stringifies to, including the literal four
            // characters "null" that UNKNOWN_TO_SDK_VERSION gives — which is a
            // value the service sent and this suite could not name, not an
            // absent member, and reporting it as absence would make a wrong
            // response satisfy `missing`.
            return v.toString();
        }
        if (v instanceof SdkAutoConstructList<?> || v instanceof SdkAutoConstructMap<?, ?>) {
            return ABSENT;
        }
        if (v instanceof Document document) {
            return fromDocument(document);
        }
        if (v instanceof List<?> list) {
            List<Object> out = new ArrayList<>(list.size());
            for (Object item : list) {
                Object doc = convert(item);
                // A null element of a list is a null the service sent, not an
                // absent member: dropping it would renumber every index after
                // it, which a path can address.
                out.add(doc == ABSENT ? Json.NULL : doc);
            }
            return out;
        }
        if (v instanceof Map<?, ?> map) {
            Map<String, Object> out = new LinkedHashMap<>();
            for (Map.Entry<?, ?> entry : map.entrySet()) {
                Object key = entry.getKey();
                String name = key instanceof Enum<?> ? key.toString() : String.valueOf(key);
                if (name == null) {
                    continue;
                }
                Object doc = convert(entry.getValue());
                if (doc != ABSENT) {
                    out.put(name, doc);
                }
            }
            return out;
        }
        if (v instanceof SdkPojo pojo) {
            Map<String, Object> out = new LinkedHashMap<>();
            for (SdkField<?> field : pojo.sdkFields()) {
                Object doc = convert(field.getValueOrDefault(pojo));
                if (doc != ABSENT) {
                    out.put(field.memberName(), doc);
                }
            }
            return out;
        }
        return String.valueOf(v);
    }

    /** An {@code smithy.api#Document} member, which carries its own JSON. */
    private static Object fromDocument(Document d) {
        if (d.isNull()) {
            return Json.NULL;
        }
        if (d.isString()) {
            return d.asString();
        }
        if (d.isBoolean()) {
            return d.asBoolean();
        }
        if (d.isNumber()) {
            return d.asNumber().doubleValue();
        }
        if (d.isList()) {
            List<Object> out = new ArrayList<>();
            for (Document item : d.asList()) {
                out.add(fromDocument(item));
            }
            return out;
        }
        if (d.isMap()) {
            Map<String, Object> out = new LinkedHashMap<>();
            d.asMap().forEach((k, value) -> out.put(k, fromDocument(value)));
            return out;
        }
        return Json.NULL;
    }
}

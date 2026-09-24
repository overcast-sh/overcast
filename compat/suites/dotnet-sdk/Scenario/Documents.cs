using System.Collections;
using System.Globalization;
using System.Reflection;
using System.Text.Encodings.Web;
using System.Text.Json;
using Amazon.Runtime;
using Amazon.Runtime.Documents;

namespace OvercastCompat.Scenario;

/// <summary>
/// An AWS SDK response, as one of the IR's documents.
/// </summary>
/// <remarks>
/// This is the one direction that still needs a conversion, and the only place
/// in this namespace that reflects. It has to: a response is an arbitrary SDK
/// response object and the assertions walk it by path, so nothing is known
/// about its shape until it arrives. The other direction — a value into a
/// request property — needs no conversion: cmd/compatgen resolves each member's
/// modeled kind and writes the spelling into the emitted source, so only a
/// deferred expression reaches run time, through <see cref="Binder.Bind{T}"/>.
/// <para>
/// Every rule the IR states about a response — a path resolves or it does not,
/// an absent list reads like an empty one, <c>equals</c> compares in the JSON
/// type system — is stated over JSON. The three interpreters get that for free:
/// they hold the parsed response. A typed SDK hands us an object of nullables,
/// ConstantClass enums and List&lt;T&gt;s instead, so one conversion stands
/// between the two, and its choices are what make this suite agree with the
/// others. Three are load-bearing:
/// </para>
/// <list type="bullet">
/// <item><description><b>null is absence, not null.</b> compat/model/README.md
/// § Paths settles it: "undefined in an SDK's object model is absence, not a
/// value". So a null property is left out of the document rather than written
/// as null. Serializing the response object with System.Text.Json instead would
/// write <c>"NextToken": null</c> for every unset member, which resolves — and
/// would fail <c>missing</c> on an absent token and <c>isList</c> on an omitted
/// page, both of which are correct AWS answers.</description></item>
/// <item><description><b>every number becomes a double.</b> That is what a JSON
/// parser produces on the interpreters' side, so an <c>equals</c> on an int
/// member compares the same way here as it does there.</description></item>
/// <item><description><b>a ConstantClass is its string.</b> AWSSDK models an
/// enum as a class wrapping the wire value; a path names the modeled member and
/// expects the string the service sent, not an object with a Value
/// property.</description></item>
/// <item><description><b>a DateTime the model calls a long is its epoch
/// milliseconds.</b> AWSSDK types some modeled longs as DateTime —
/// CloudWatch Logs' <c>LogStream.CreationTime</c> among them — and every other
/// backend holds the number the service sent. The generated groups register
/// each such property through <see cref="EpochMilliseconds"/>, so an
/// <c>equals</c>, a <c>where</c> and an exported <c>$ref</c> see that number
/// here as they do there. A DateTime the model calls a timestamp stays ISO 8601
/// text: the IR never compares or binds a timestamp, because the backends
/// cannot agree on one (compat/model/README.md § Values).</description></item>
/// </list>
/// </remarks>
internal static class Documents
{
    /// <summary>
    /// The properties registered through <see cref="EpochMilliseconds"/>, by
    /// declaring type and name.
    /// </summary>
    /// <remarks>
    /// Keyed by the declaring type rather than by PropertyInfo: a PropertyInfo
    /// read off a derived type carries that type as its ReflectedType and is not
    /// equal to one read off the declaring type, so the same property would be
    /// two keys.
    /// </remarks>
    private static readonly System.Collections.Concurrent.ConcurrentDictionary<(Type, string), bool> EpochMillisecondProperties = new();

    /// <summary>
    /// Registers DateTime properties whose modeled type is a long of epoch
    /// milliseconds, so their document form is that number rather than ISO
    /// text.
    /// </summary>
    /// <remarks>
    /// Called from a generated group's constructor, for the properties
    /// cmd/compatgen found AWSSDK types as DateTime where the model says long —
    /// on the requests the group sends as well as the responses it reads,
    /// because failure-message field 3 renders the request through this same
    /// conversion. Registering one twice is harmless.
    /// </remarks>
    public static void EpochMilliseconds(Type type, params string[] properties)
    {
        foreach (var name in properties)
        {
            var property = type.GetProperty(name, BindingFlags.Public | BindingFlags.Instance)
                ?? throw new ArgumentException($"{type.FullName} declares no property {name}");
            if ((Nullable.GetUnderlyingType(property.PropertyType) ?? property.PropertyType) != typeof(DateTime))
            {
                throw new ArgumentException(
                    $"{type.FullName}.{name} is a {property.PropertyType.Name}, not a DateTime; only a DateTime has an epoch-milliseconds form");
            }
            EpochMillisecondProperties[(property.DeclaringType!, property.Name)] = true;
        }
    }

    /// <summary>
    /// A DateTime as the epoch milliseconds the service sent: the number every
    /// other backend reads for a modeled long.
    /// </summary>
    /// <remarks>
    /// An Unspecified kind is read as UTC rather than local time, which is what
    /// the SDK means by one and what keeps the answer the same on a machine
    /// whose clock is not in UTC.
    /// </remarks>
    public static double ToEpochMilliseconds(DateTime value)
    {
        var utc = value.Kind == DateTimeKind.Unspecified
            ? DateTime.SpecifyKind(value, DateTimeKind.Utc)
            : value.ToUniversalTime();
        return new DateTimeOffset(utc).ToUnixTimeMilliseconds();
    }

    private static readonly JsonSerializerOptions CanonicalOptions = new()
    {
        // The IR's values are compared and printed as JSON, and an escaped
        // "&" in place of "&" would make an ARN unrecognisable in a
        // failure message. Sorted keys come from the SortedDictionary the
        // conversion builds, not from the serializer.
        Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping,
        WriteIndented = false,
    };

    /// <summary>
    /// Converts an SDK value to the IR's document form: SortedDictionary,
    /// List&lt;object?&gt;, string, double, bool or null.
    /// </summary>
    /// <returns>
    /// False when the value is absent — a null reference or a null Nullable —
    /// and the caller then omits the member entirely.
    /// </returns>
    public static bool TryConvert(object? value, out object? document)
    {
        document = null;
        if (value is null)
        {
            return false;
        }

        switch (value)
        {
            case string text:
                document = text;
                return true;
            case bool flag:
                document = flag;
                return true;
            // A ConstantClass is AWSSDK's enum: the wire value, wrapped.
            case ConstantClass constant:
                document = constant.Value;
                return true;
            case sbyte or byte or short or ushort or int or uint or long or ulong or float or double or decimal:
                document = System.Convert.ToDouble(value, CultureInfo.InvariantCulture);
                return true;
            // A timestamp is never compared by the IR (compat/model/README.md
            // § Assertions), but it can sit on a response a path walks past, so
            // it is rendered rather than dropped. A DateTime the model calls a
            // long never reaches here: FromObject renders a registered one as
            // its epoch milliseconds (EpochMilliseconds).
            case DateTime timestamp:
                document = timestamp.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.FFFFFFFZ", CultureInfo.InvariantCulture);
                return true;
            case DateTimeOffset timestamp:
                document = timestamp.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.FFFFFFFZ", CultureInfo.InvariantCulture);
                return true;
            // A blob is a MemoryStream or a byte[], which JSON carries base64.
            // Blobs are refused by the generator, so nothing asserts on one;
            // rendering it the way the wire does is still better than a list of
            // 8-bit numbers in a failure message.
            case byte[] bytes:
                document = System.Convert.ToBase64String(bytes);
                return true;
            case MemoryStream stream:
                document = System.Convert.ToBase64String(stream.ToArray());
                return true;
            // AWSSDK's own JSON value, for a modeled `document` member. It is a
            // struct wrapping a tagged union, so the reflection fallback below
            // would produce a `{"Type": ...}` object rather than the JSON the
            // service sent, and no path over it would resolve.
            case Document json:
                document = FromDocument(json);
                return true;
            case IDictionary map:
            {
                var members = new SortedDictionary<string, object?>(StringComparer.Ordinal);
                foreach (DictionaryEntry entry in map)
                {
                    // SDK map keys are strings or ConstantClass enums; anything
                    // else has no place in a document and is dropped rather
                    // than stringified into a member name a path could
                    // accidentally address.
                    var key = entry.Key switch
                    {
                        string text => text,
                        ConstantClass constant => constant.Value,
                        _ => null,
                    };
                    if (key is null)
                    {
                        continue;
                    }
                    if (TryConvert(entry.Value, out var converted))
                    {
                        members[key] = converted;
                    }
                }
                document = members;
                return true;
            }
            case IEnumerable items:
            {
                var list = new List<object?>();
                foreach (var item in items)
                {
                    // A null element of a list is a null the service sent, not
                    // an absent member: dropping it would renumber every index
                    // after it, which a path can address.
                    list.Add(TryConvert(item, out var converted) ? converted : null);
                }
                document = list;
                return true;
            }
        }

        document = FromObject(value);
        return true;
    }

    /// <summary>
    /// Converts AWSSDK's own JSON value into the IR's document form.
    /// </summary>
    /// <remarks>
    /// Every numeric arm becomes a double, as every other number does: an
    /// <c>equals</c> compares in the JSON type system, where 1 and 1.0 are one
    /// value. A null document is the JSON null the service sent rather than an
    /// absent member — this is inside a value, not a property of one.
    /// </remarks>
    private static object? FromDocument(Document value)
    {
        switch (value.Type)
        {
            case DocumentType.Bool:
                return value.AsBool();
            case DocumentType.String:
                return value.AsString();
            case DocumentType.Int:
                return (double)value.AsInt();
            case DocumentType.Long:
                return (double)value.AsLong();
            case DocumentType.Double:
                return value.AsDouble();
            case DocumentType.List:
                return value.AsList().Select(FromDocument).ToList();
            case DocumentType.Dictionary:
            {
                var members = new SortedDictionary<string, object?>(StringComparer.Ordinal);
                foreach (var entry in value.AsDictionary())
                {
                    members[entry.Key] = FromDocument(entry.Value);
                }
                return members;
            }
            default:
                return null;
        }
    }

    /// <summary>
    /// Converts an SDK response or model object by reading its public
    /// properties.
    /// </summary>
    /// <remarks>
    /// The members AWSSDK puts on every response — ResponseMetadata,
    /// ContentLength, HttpStatusCode — are the SDK's own bookkeeping rather
    /// than part of the modeled response, and a scenario path can only ever
    /// mean a modeled member, so they are dropped rather than surfaced. They
    /// are recognised by where they are declared, which is the one rule that
    /// stays right as AWSSDK adds to that base class.
    /// <para>The same skip is applied on the request path. A request reaches
    /// here too — <see cref="Execution"/> renders the request it sent into
    /// failure-message field 3 — and <c>AmazonWebServiceRequest</c> declares no
    /// public property today, so the skip changes nothing yet. That is exactly
    /// when to write it: the day the SDK adds one, the two paths would
    /// otherwise start disagreeing about what a modeled member is, and the
    /// failure message would gain a field no scenario can name.</para>
    /// </remarks>
    private static SortedDictionary<string, object?> FromObject(object value)
    {
        var members = new SortedDictionary<string, object?>(StringComparer.Ordinal);
        foreach (var property in value.GetType().GetProperties(BindingFlags.Public | BindingFlags.Instance))
        {
            if (property.GetIndexParameters().Length > 0 || property.GetMethod is null)
            {
                continue;
            }
            if (property.DeclaringType == typeof(AmazonWebServiceResponse)
                || property.DeclaringType == typeof(AmazonWebServiceRequest))
            {
                continue;
            }
            object? raw;
            try
            {
                raw = property.GetValue(value);
            }
            catch (TargetInvocationException)
            {
                // A property that throws on get says nothing about the
                // response; treating it as absent beats failing the step.
                continue;
            }
            if (raw is DateTime timestamp && EpochMillisecondProperties.ContainsKey((property.DeclaringType!, property.Name)))
            {
                members[property.Name] = ToEpochMilliseconds(timestamp);
                continue;
            }
            if (TryConvert(raw, out var converted))
            {
                members[property.Name] = converted;
            }
        }
        return members;
    }

    /// <summary>
    /// Renders a document in a stable form: object keys sorted (the conversion
    /// builds a SortedDictionary), no HTML escaping, no trailing newline.
    /// </summary>
    /// <remarks>
    /// It is both how values are compared and how they are printed in a failure
    /// message, so "expected X, actual Y" reads in the same notation the
    /// scenario file is written in.
    /// </remarks>
    public static string Canonical(object? document) =>
        JsonSerializer.Serialize(document, CanonicalOptions);

    /// <summary>
    /// The IR's "equal, as JSON" (compat/model/README.md § Assertions).
    /// </summary>
    /// <remarks>
    /// Both sides are documents by the time they get here: a response through
    /// TryConvert, an expected value through the evaluator, which normalises
    /// literals the same way. So every JSON number is a double on both sides,
    /// every string a string, every structure a SortedDictionary, and comparing
    /// their canonical encodings is JSON equality with no coercion: "30" never
    /// equals 30, and true never equals 1.
    /// </remarks>
    public static bool JsonEqual(object? left, object? right) =>
        string.Equals(Canonical(left), Canonical(right), StringComparison.Ordinal);

    /// <summary>Prints a value for a failure message.</summary>
    public static string Render(object? value) => Canonical(value);

    /// <summary>What a failure message prints where a path did not resolve.</summary>
    public const string MissingValue = "<missing>";

    /// <summary>Prints a resolved-or-not value for a failure message.</summary>
    public static string RenderResolved(object? value, bool resolved) =>
        resolved ? Render(value) : MissingValue;

    /// <summary>
    /// The IR's emptiness: null, "", [] or {}. Numbers and booleans are never
    /// empty, which is what stops nonEmpty failing on a legitimate 0 or false.
    /// </summary>
    public static bool IsEmpty(object? value) => value switch
    {
        null => true,
        string text => text.Length == 0,
        List<object?> list => list.Count == 0,
        SortedDictionary<string, object?> members => members.Count == 0,
        _ => false,
    };
}

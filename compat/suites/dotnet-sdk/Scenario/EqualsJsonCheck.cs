using System.Globalization;
using System.Text;
using System.Text.Json;

namespace OvercastCompat.Scenario;

/// <summary>
/// The <c>equalsJSON</c> check: a member holding a JSON document, compared by
/// value rather than by text (compat/model/README.md § Assertions).
/// </summary>
/// <remarks>
/// It exists because the SDKs disagree about what such a member is. IAM sends a
/// policy document as percent-encoded JSON text; botocore decodes it for the
/// two Python backends, and AWSSDK hands over the string as sent. So a string is
/// percent-decoded once and parsed here, while an object or a list is the
/// document already. compat/model/testdata/equalsjson pins every rule for every
/// backend at once.
/// <para>
/// Both sides end up in <see cref="Documents"/>' form — SortedDictionary,
/// List&lt;object?&gt;, string, double, bool or null — and are compared by
/// <see cref="Equal"/> rather than by <see cref="Documents.JsonEqual"/>:
/// System.Text.Json writes the double -0 as <c>-0</c>, so comparing canonical
/// text would tell -0 from 0, where the IR compares numbers as doubles.
/// </para>
/// </remarks>
internal static class EqualsJsonCheck
{
    /// <summary>
    /// Parses the operand the emitter wrote as compact JSON text.
    /// </summary>
    /// <remarks>
    /// The operand is a literal object or array and is never evaluated, so a
    /// <c>$</c>-prefixed key anywhere inside it — which the IR would otherwise
    /// read as an expression — is refused rather than compared either way.
    /// </remarks>
    /// <exception cref="ScenarioValueException">
    /// The text is not one JSON object or array, or holds a <c>$</c> key.
    /// </exception>
    public static object Operand(string text)
    {
        if (!TryParse(text, out var document))
        {
            throw new ScenarioValueException($"equalsJSON operand is not JSON text: {Documents.Render(text)}");
        }
        if (document is not SortedDictionary<string, object?> and not List<object?>)
        {
            throw new ScenarioValueException(
                $"equalsJSON operand must be a JSON object or array, not {Documents.Render(document)}");
        }
        if (DollarKey(document) is { } key)
        {
            throw new ScenarioValueException(
                $"equalsJSON operand has the key {Documents.Render(key)}; it is a literal document, and a $ key would read as an expression");
        }
        return document;
    }

    /// <summary>
    /// The document an actual value holds: a string percent-decoded once and
    /// parsed as one JSON text, an object or list as it is.
    /// </summary>
    /// <returns>False when the value is not one — a number, a boolean, null, or text that does not parse.</returns>
    public static bool TryDocument(object? actual, out object? document)
    {
        switch (actual)
        {
            case string text:
                return TryParse(PercentDecode(text), out document);
            case SortedDictionary<string, object?> or List<object?>:
                document = actual;
                return true;
            default:
                document = null;
                return false;
        }
    }

    /// <summary>Evaluates the check against what the path resolved to.</summary>
    /// <returns>Null when it holds; otherwise the failure message's actual field.</returns>
    public static string? Mismatch(object operand, object? got, bool resolved)
    {
        if (!resolved)
        {
            return Documents.MissingValue;
        }
        if (!TryDocument(got, out var document))
        {
            return "not a JSON document: " + Documents.Render(got);
        }
        return Equal(document, operand) ? null : "document " + Documents.Render(document);
    }

    /// <summary>The failure message's expected field.</summary>
    public static string Expected(object operand) => "equalsJSON " + Documents.Render(operand);

    /// <summary>
    /// Two documents are the same JSON value: members by name whatever their
    /// order, arrays element by element, numbers as doubles, and no coercion
    /// between types.
    /// </summary>
    public static bool Equal(object? left, object? right) => (left, right) switch
    {
        (null, null) => true,
        (string a, string b) => string.Equals(a, b, StringComparison.Ordinal),
        (bool a, bool b) => a == b,
        (double a, double b) => a == b,
        (List<object?> a, List<object?> b) => a.Count == b.Count && a.Zip(b).All(pair => Equal(pair.First, pair.Second)),
        (SortedDictionary<string, object?> a, SortedDictionary<string, object?> b) =>
            a.Count == b.Count && a.All(member => b.TryGetValue(member.Key, out var other) && Equal(member.Value, other)),
        _ => false,
    };

    /// <summary>
    /// Python's <c>urllib.parse.unquote</c>, which botocore applies to the
    /// member unconditionally.
    /// </summary>
    /// <remarks>
    /// <c>%XX</c> becomes that byte and everything else — <c>+</c> included, and
    /// a <c>%</c> not followed by two hex digits — is kept as its own UTF-8
    /// bytes, then the bytes are read as UTF-8. Hand-rolled because
    /// <c>WebUtility.UrlDecode</c> and <c>HttpUtility.UrlDecode</c> turn
    /// <c>+</c> into a space, which would part from the other backends.
    /// </remarks>
    public static string PercentDecode(string text)
    {
        var bytes = new List<byte>(text.Length);
        var i = 0;
        while (i < text.Length)
        {
            if (text[i] == '%' && i + 2 < text.Length && Hex(text[i + 1]) >= 0 && Hex(text[i + 2]) >= 0)
            {
                bytes.Add((byte)(Hex(text[i + 1]) << 4 | Hex(text[i + 2])));
                i += 3;
                continue;
            }
            // A surrogate pair is one character, encoded together rather than
            // as two lone halves.
            var length = char.IsSurrogatePair(text, i) ? 2 : 1;
            bytes.AddRange(Encoding.UTF8.GetBytes(text.Substring(i, length)));
            i += length;
        }
        return Encoding.UTF8.GetString(bytes.ToArray());
    }

    /// <summary>An ASCII hex digit's value, or -1.</summary>
    private static int Hex(char c) => c switch
    {
        >= '0' and <= '9' => c - '0',
        >= 'a' and <= 'f' => c - 'a' + 10,
        >= 'A' and <= 'F' => c - 'A' + 10,
        _ => -1,
    };

    /// <summary>
    /// Parses exactly one JSON text, whitespace around it allowed. JsonDocument
    /// refuses anything after the value, comments and trailing commas by
    /// default, which is the strictness the fixture asks for.
    /// </summary>
    private static bool TryParse(string text, out object? document)
    {
        try
        {
            using var parsed = JsonDocument.Parse(text);
            document = FromElement(parsed.RootElement);
            return true;
        }
        catch (Exception ex) when (ex is JsonException or InvalidOperationException)
        {
            // InvalidOperationException: a string holding an escaped lone
            // surrogate parses but cannot be read back as a .NET string.
            document = null;
            return false;
        }
    }

    /// <summary>A parsed JSON value in <see cref="Documents"/>' form.</summary>
    public static object? FromElement(JsonElement element)
    {
        switch (element.ValueKind)
        {
            case JsonValueKind.Object:
            {
                var members = new SortedDictionary<string, object?>(StringComparer.Ordinal);
                foreach (var property in element.EnumerateObject())
                {
                    members[property.Name] = FromElement(property.Value);
                }
                return members;
            }
            case JsonValueKind.Array:
                return element.EnumerateArray().Select(FromElement).ToList();
            case JsonValueKind.String:
                return element.GetString();
            case JsonValueKind.Number:
                // Parsed from the raw text rather than through GetDouble, which
                // refuses a literal beyond double's range instead of rounding
                // it to infinity the way every other backend's parser does.
                return double.Parse(element.GetRawText(), NumberStyles.Float, CultureInfo.InvariantCulture);
            case JsonValueKind.True:
                return true;
            case JsonValueKind.False:
                return false;
            default:
                return null;
        }
    }

    /// <summary>The first <c>$</c>-prefixed object key at any depth, or null.</summary>
    private static string? DollarKey(object? document)
    {
        switch (document)
        {
            case SortedDictionary<string, object?> members:
                foreach (var member in members)
                {
                    if (member.Key.StartsWith('$'))
                    {
                        return member.Key;
                    }
                    if (DollarKey(member.Value) is { } inner)
                    {
                        return inner;
                    }
                }
                return null;
            case List<object?> items:
                foreach (var item in items)
                {
                    if (DollarKey(item) is { } inner)
                    {
                        return inner;
                    }
                }
                return null;
            default:
                return null;
        }
    }
}

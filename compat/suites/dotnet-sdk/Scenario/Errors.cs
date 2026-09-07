using System.Net;
using Amazon.Runtime;

namespace OvercastCompat.Scenario;

/// <summary>
/// Error matching (compat/model/README.md § Errors).
/// </summary>
/// <remarks>
/// A clause carries both the modeled shape and the wire code, because SDKs
/// disagree about which of the two they surface — for SQS's not-found,
/// QueueDoesNotExist and AWS.SimpleQueueService.NonExistentQueue — so either is
/// accepted, but by <b>equality</b> against a code parsed out of a surface this
/// SDK actually has, never by containment over the whole message. Containment
/// cannot tell a code from a code that ends with it: a clause naming
/// NotFoundException would be satisfied by a ResourceNotFoundException, which
/// is a different error from a different branch of the service, and by the word
/// appearing anywhere in the SDK's prose.
/// <para>The surfaces the AWS SDK for .NET gives us, and where each comes
/// from:</para>
/// <list type="table">
/// <item><term>AmazonServiceException.ErrorCode</term><description>the code the
/// unmarshaller resolved — the AWS JSON protocols' <c>__type</c>, the REST JSON
/// body's <c>code</c> member, and, for an awsQueryCompatible service, the
/// <c>x-amzn-query-error</c> header, which the SDK prefers over the body
/// exactly as botocore does. That is the bodyType, bodyCode and
/// queryErrorHeader carriers, all three observed through one
/// property.</description></item>
/// <item><term>the exception's own type name</term><description>the class
/// AWSSDK generated for a modeled error, read off the exception rather than
/// named in a cast per shape, so no generated file has to reference a service's
/// model namespace to match one. This is the exceptionName carrier. Note that
/// AWSSDK appends "Exception" to a shape that does not already end in it —
/// SQS's QueueDoesNotExist becomes QueueDoesNotExistException — so this surface
/// states a code only for the shapes AWS already spells that way. The suffix is
/// deliberately not stripped: doing so would make a clause naming
/// ResourceNotFound match a ResourceNotFoundException, which the shared
/// near-miss fixtures forbid.</description></item>
/// </list>
/// <para>When no surface states a code the clause does <b>not</b> match. There
/// is no containment fallback, and the absence of one is the rule rather than
/// an omission: an error with no code surface is no evidence that the service
/// raised the named error.</para>
/// </remarks>
internal static class Errors
{
    /// <summary>Reports whether a failed call carries the error a clause names.</summary>
    public static bool Matches(Exception? error, ErrorSpec? want)
    {
        if (error is null || want is null)
        {
            return false;
        }
        foreach (var code in Codes(error))
        {
            if ((want.Shape.Length > 0 && string.Equals(code, want.Shape, StringComparison.Ordinal))
                || (want.Code.Length > 0 && string.Equals(code, want.Code, StringComparison.Ordinal)))
            {
                return true;
            }
        }
        return false;
    }

    /// <summary>
    /// Every code the error states, in every spelling a clause may name it by,
    /// or nothing when it states none.
    /// </summary>
    public static IReadOnlyList<string> Codes(Exception error)
    {
        var codes = new List<string>();
        foreach (var link in Chain(error))
        {
            if (link is AmazonServiceException service && !string.IsNullOrEmpty(service.ErrorCode))
            {
                codes.AddRange(Spellings(service.ErrorCode));
            }
            var modeled = ModeledTypeName(link);
            if (modeled is not null)
            {
                codes.AddRange(Spellings(modeled));
            }
        }
        return codes;
    }

    /// <summary>
    /// One observed code in every spelling a clause may name it by, which is
    /// the list compat/model/README.md § Errors fixes: the value itself, what
    /// follows the last "#" of a Smithy id
    /// (<c>com.amazonaws.sqs#QueueDoesNotExist</c> states the same code as
    /// <c>QueueDoesNotExist</c>), and what precedes the first ";" of the
    /// <c>&lt;code&gt;;&lt;fault&gt;</c> form the x-amzn-query-error header
    /// uses.
    /// </summary>
    /// <remarks>
    /// Splitting at those separators and nowhere else is what keeps the match
    /// an equality: no spelling of ResourceNotFoundException is
    /// NotFoundException.
    /// </remarks>
    public static IReadOnlyList<string> Spellings(string code)
    {
        var spellings = new List<string> { code };
        var hash = code.LastIndexOf('#');
        if (hash >= 0)
        {
            spellings.Add(code[(hash + 1)..]);
        }
        var semicolon = code.IndexOf(';', StringComparison.Ordinal);
        if (semicolon >= 0)
        {
            spellings.Add(code[..semicolon]);
        }
        return spellings;
    }

    /// <summary>
    /// The exceptionName surface: the name of the class AWSSDK generated for a
    /// modeled error shape, or null for an error it did not model.
    /// </summary>
    /// <remarks>
    /// A generated exception lives in the service's <c>Model</c> namespace and
    /// derives from AmazonServiceException; the per-service catch-all
    /// (<c>AmazonSQSException</c>) and the runtime's own exceptions sit outside
    /// it. Naming the namespace rather than listing the catch-alls is what
    /// keeps this right as services are added.
    /// </remarks>
    public static string? ModeledTypeName(Exception error)
    {
        if (error is not AmazonServiceException)
        {
            return null;
        }
        var type = error.GetType();
        var space = type.Namespace;
        return space is not null && space.EndsWith(".Model", StringComparison.Ordinal) ? type.Name : null;
    }

    /// <summary>
    /// Reports whether the emulator answered 501.
    /// </summary>
    /// <remarks>
    /// One rule serves both paths: Runner.ClassifyResponse reads the response
    /// the SDK parsed, which is exact, and only an exception carrying none
    /// falls back to the substring heuristic, which is what that heuristic is
    /// for. The chain is walked here as well as in the Runner because the SDK
    /// wraps a modeled exception in an AggregateException in some paths, whose
    /// children the Runner's InnerException walk does not reach.
    /// </remarks>
    public static bool IsUnimplementedResponse(Exception error)
    {
        foreach (var link in Chain(error))
        {
            if (link is AmazonServiceException service
                && Harness.Runner.ClassifyResponse(service) is bool decided)
            {
                return decided;
            }
        }
        return Harness.Runner.LooksUnimplementedWithoutResponse(error.ToString());
    }

    /// <summary>
    /// Flattens an exception chain, following inner exceptions and the children
    /// of an AggregateException.
    /// </summary>
    /// <remarks>
    /// The AWS SDK wraps the unmarshalled error in a transport exception, so
    /// the surfaces a clause reads are spread across more than one link.
    /// </remarks>
    public static IReadOnlyList<Exception> Chain(Exception error)
    {
        var chain = new List<Exception>();
        Walk(error);
        return chain;

        void Walk(Exception? link)
        {
            while (link is not null)
            {
                chain.Add(link);
                if (link is AggregateException aggregate)
                {
                    foreach (var child in aggregate.InnerExceptions)
                    {
                        Walk(child);
                    }
                    return;
                }
                link = link.InnerException;
            }
        }
    }
}

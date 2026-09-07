using System.Net;
using Amazon.Runtime;
using OvercastCompat.Harness;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// <see cref="Runner.IsUnimplemented"/> classifies from the response, not the
/// prose (#1924).
/// </summary>
/// <remarks>
/// The rule this replaced matched a bare "501" anywhere in the exception's
/// text, so a request id, an ARN, a resource name or a port was enough to
/// report a 400 as <c>unimplemented</c>. That is how the sibling go-sdk suite
/// flipped a gated baseline row on CI run 34064243252 and failed an unrelated
/// pull request.
/// <para>Nothing here talks to Overcast: an AmazonServiceException is the
/// object the AWS SDK for .NET throws, and these are built the way the SDK
/// builds them.</para>
/// </remarks>
public class UnimplementedClassificationTests
{
    private static AmazonServiceException ServiceException(string code, string message, HttpStatusCode status) =>
        new(message, null, ErrorType.Sender, code, "5f2c9501-0f3a-4c7d-9a11-6b1d0c2e4a77", status);

    [Fact]
    public void A400IsAFailureHoweverItsProseReads()
    {
        // A rotation Lambda's own answer, echoed back by the 400 the test
        // expects, puts both of the heuristic's markers in the message.
        var error = ServiceException("InvalidRequestException",
            "Lambda arn:aws:lambda:us-east-1:000000000000:function:oc-501-rot answered \"Not Implemented\"",
            HttpStatusCode.BadRequest);
        Assert.Contains("501", error.ToString());
        Assert.Contains("Not Implemented", error.ToString());
        Assert.False(Runner.IsUnimplemented(error), "a 400 is a failure whatever its text contains");
    }

    [Fact]
    public void A400WhoseResourceNameContains501IsAFailure()
    {
        var error = ServiceException("ResourceNotFoundException",
            "Secrets Manager can't find the specified secret: oc-501abcde-rotate", HttpStatusCode.BadRequest);
        Assert.False(Runner.IsUnimplemented(error));
    }

    [Fact]
    public void AReal501IsUnimplemented()
    {
        var error = ServiceException("NotImplemented",
            "This operation is not implemented by the emulator", HttpStatusCode.NotImplemented);
        Assert.True(Runner.IsUnimplemented(error));
    }

    [Fact]
    public void AnUnknownOperationIsUnimplementedAt400()
    {
        var error = ServiceException("UnknownOperationException",
            "Unknown operation: Frobnicate", HttpStatusCode.BadRequest);
        Assert.True(Runner.IsUnimplemented(error));
    }

    [Fact]
    public void AnExceptionCarryingNoResponseFallsBackToTheText()
    {
        // Nothing to read but the message: the heuristic is all there is.
        Assert.True(Runner.IsUnimplemented(new HttpRequestException("501 Not Implemented")));
        Assert.False(Runner.IsUnimplemented(new HttpRequestException("connection refused")));
    }
}

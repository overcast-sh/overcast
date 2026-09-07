namespace OvercastCompat.Harness;

public delegate Task TestFn(TestContext context);
public delegate Task SetupFn(TestContext context);

/// <summary>
/// An exception whose message was assembled out of scenario data rather than
/// produced by the AWS SDK - the params JSON that was sent, expected and actual
/// values, the SDK's own text quoted inside it.
/// </summary>
/// <remarks>
/// <see cref="Runner.LooksUnimplementedWithoutResponse"/> must never be
/// applied to such a message. It matches a bare "501", and a run id or a port like 4501 in the
/// params is enough to put one there, which would report every failure of that
/// test as unimplemented. A composed failure states the 501 itself instead, in
/// <see cref="Unimplemented"/>, decided where the raw SDK error was still in
/// hand.
/// </remarks>
public interface IComposedFailure
{
    /// <summary>Whether the emulator answered 501.</summary>
    bool Unimplemented { get; }
}

public sealed record TestCase(
    string Name,
    TestFn Fn,
    string? Op = null,
    string? Skip = null,
    IReadOnlyList<string>? Depends = null)
{
    public IReadOnlyList<string> Depends { get; init; } = Depends ?? Array.Empty<string>();
}

public sealed record TestGroup(
    string Suite,
    string Service,
    string Name,
    IReadOnlyList<TestCase> Tests,
    SetupFn? Setup = null,
    SetupFn? Teardown = null,
    bool Parallel = false);

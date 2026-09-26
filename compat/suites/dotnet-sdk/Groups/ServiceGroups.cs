using OvercastCompat.Clients;

namespace OvercastCompat.Groups;

/// <summary>
/// Every service group in the suite, in registration order.
/// </summary>
/// <remarks>
/// Separate from Program.cs's entry point so tests can resolve the suite's
/// real impl keys against the real registry.json without starting a run -
/// mirrors go-sdk's <c>groups.All()</c> and java-sdk's
/// <c>Main.serviceGroups()</c>.
/// </remarks>
public static class ServiceGroups
{
    public static IServiceGroup[] All(AwsClients clients) =>
    [
        new S3Group(clients),
        new SqsGroup(clients),
        new DynamoDbGroup(clients),
        new SnsGroup(clients),
        new LambdaGroup(clients),
        new StsGroup(clients),
        new KmsGroup(clients),
        new SecretsManagerGroup(clients),
        new SsmGroup(clients),
        new EventBridgeGroup(clients),
    ];
}

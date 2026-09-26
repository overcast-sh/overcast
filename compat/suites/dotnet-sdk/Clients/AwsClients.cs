using Amazon;
using Amazon.DynamoDBv2;
using Amazon.EventBridge;
using Amazon.KeyManagementService;
using Amazon.Lambda;
using Amazon.Runtime;
using Amazon.S3;
using Amazon.SecretsManager;
using Amazon.SecurityToken;
using Amazon.SimpleNotificationService;
using Amazon.SimpleSystemsManagement;
using Amazon.SQS;

namespace OvercastCompat.Clients;

public sealed class AwsClients
{
    private readonly string _endpoint;
    private readonly RegionEndpoint _region;
    private readonly AWSCredentials _credentials = new BasicAWSCredentials("test", "test");

    private AmazonDynamoDBClient? _dynamodb;
    private AmazonEventBridgeClient? _eventbridge;
    private AmazonKeyManagementServiceClient? _kms;
    private AmazonLambdaClient? _lambda;
    private AmazonS3Client? _s3;
    private AmazonSecretsManagerClient? _secretsmanager;
    private AmazonSecurityTokenServiceClient? _sts;
    private AmazonSimpleNotificationServiceClient? _sns;
    private AmazonSimpleSystemsManagementClient? _ssm;
    private AmazonSQSClient? _sqs;

    public AwsClients(string endpoint, string region)
    {
        _endpoint = endpoint;
        _region = RegionEndpoint.GetBySystemName(region);
    }

    /// <summary>
    /// Builds one service client against the endpoint, region and placeholder
    /// credentials every group shares.
    /// </summary>
    /// <remarks>
    /// Internal rather than private because the generated scenario groups build
    /// their own clients through it: adding a named accessor above for every
    /// service cmd/compatgen learns to cover would be a line of this file per
    /// AWS service, and nothing else about those clients differs.
    /// </remarks>
    internal T CreateClient<T>(Func<AWSCredentials, ClientConfig, T> factory, ClientConfig config) where T : AmazonServiceClient
    {
        config.ServiceURL = _endpoint;
        config.UseHttp = _endpoint.StartsWith("http://", StringComparison.OrdinalIgnoreCase);
        config.AuthenticationRegion = _region.SystemName;
        return factory(_credentials, config);
    }

    public AmazonDynamoDBClient DynamoDB()
    {
        return _dynamodb ??= CreateClient((creds, cfg) => new AmazonDynamoDBClient(creds, (AmazonDynamoDBConfig)cfg), new AmazonDynamoDBConfig());
    }

    public AmazonEventBridgeClient EventBridge()
    {
        return _eventbridge ??= CreateClient((creds, cfg) => new AmazonEventBridgeClient(creds, (AmazonEventBridgeConfig)cfg), new AmazonEventBridgeConfig());
    }

    public AmazonKeyManagementServiceClient KMS()
    {
        return _kms ??= CreateClient((creds, cfg) => new AmazonKeyManagementServiceClient(creds, (AmazonKeyManagementServiceConfig)cfg), new AmazonKeyManagementServiceConfig());
    }

    public AmazonLambdaClient Lambda()
    {
        return _lambda ??= CreateClient((creds, cfg) => new AmazonLambdaClient(creds, (AmazonLambdaConfig)cfg), new AmazonLambdaConfig());
    }

    public AmazonS3Client S3()
    {
        return _s3 ??= new AmazonS3Client(_credentials, new AmazonS3Config
        {
            ServiceURL = _endpoint,
            ForcePathStyle = true,
            UseHttp = _endpoint.StartsWith("http://", StringComparison.OrdinalIgnoreCase),
            AuthenticationRegion = _region.SystemName,
        });
    }

    public AmazonSecretsManagerClient SecretsManager()
    {
        return _secretsmanager ??= CreateClient((creds, cfg) => new AmazonSecretsManagerClient(creds, (AmazonSecretsManagerConfig)cfg), new AmazonSecretsManagerConfig());
    }

    public AmazonSecurityTokenServiceClient STS()
    {
        return _sts ??= new AmazonSecurityTokenServiceClient(_credentials, new AmazonSecurityTokenServiceConfig
        {
            ServiceURL = _endpoint,
            UseHttp = _endpoint.StartsWith("http://", StringComparison.OrdinalIgnoreCase),
            AuthenticationRegion = _region.SystemName,
        });
    }

    public AmazonSimpleNotificationServiceClient SNS()
    {
        return _sns ??= CreateClient((creds, cfg) => new AmazonSimpleNotificationServiceClient(creds, (AmazonSimpleNotificationServiceConfig)cfg), new AmazonSimpleNotificationServiceConfig());
    }

    public AmazonSimpleSystemsManagementClient SSM()
    {
        return _ssm ??= CreateClient((creds, cfg) => new AmazonSimpleSystemsManagementClient(creds, (AmazonSimpleSystemsManagementConfig)cfg), new AmazonSimpleSystemsManagementConfig());
    }

    public AmazonSQSClient SQS()
    {
        return _sqs ??= CreateClient((creds, cfg) => new AmazonSQSClient(creds, (AmazonSQSConfig)cfg), new AmazonSQSConfig());
    }
}

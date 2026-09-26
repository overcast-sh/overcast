package io.overcast.compat.clients;

import software.amazon.awssdk.auth.credentials.AwsBasicCredentials;
import software.amazon.awssdk.auth.credentials.StaticCredentialsProvider;
import software.amazon.awssdk.awscore.client.builder.AwsClientBuilder;
import software.amazon.awssdk.awscore.client.builder.AwsSyncClientBuilder;
import software.amazon.awssdk.http.apache.ApacheHttpClient;
import software.amazon.awssdk.regions.Region;
import software.amazon.awssdk.services.apigateway.ApiGatewayClient;
import software.amazon.awssdk.services.apigatewayv2.ApiGatewayV2Client;
import software.amazon.awssdk.services.appsync.AppSyncClient;
import software.amazon.awssdk.services.cloudformation.CloudFormationClient;
import software.amazon.awssdk.services.cloudfront.CloudFrontClient;
import software.amazon.awssdk.services.cloudwatchlogs.CloudWatchLogsClient;
import software.amazon.awssdk.services.cognitoidentityprovider.CognitoIdentityProviderClient;
import software.amazon.awssdk.services.dynamodb.DynamoDbClient;
import software.amazon.awssdk.services.ec2.Ec2Client;
import software.amazon.awssdk.services.ecs.EcsClient;
import software.amazon.awssdk.services.efs.EfsClient;
import software.amazon.awssdk.services.elasticache.ElastiCacheClient;
import software.amazon.awssdk.services.eventbridge.EventBridgeClient;
import software.amazon.awssdk.services.pipes.PipesClient;
import software.amazon.awssdk.services.kms.KmsClient;
import software.amazon.awssdk.services.lambda.LambdaAsyncClient;
import software.amazon.awssdk.services.lambda.LambdaClient;
import software.amazon.awssdk.services.rds.RdsClient;
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.s3.S3Configuration;
import software.amazon.awssdk.services.secretsmanager.SecretsManagerClient;
import software.amazon.awssdk.services.ses.SesClient;
import software.amazon.awssdk.services.sfn.SfnClient;
import software.amazon.awssdk.services.shield.ShieldClient;
import software.amazon.awssdk.services.sns.SnsClient;
import software.amazon.awssdk.services.sqs.SqsClient;
import software.amazon.awssdk.services.ssm.SsmClient;
import software.amazon.awssdk.services.sts.StsClient;
import software.amazon.awssdk.services.wafv2.Wafv2Client;

import java.net.URI;

/**
 * Lazily-initialised factory for all AWS service clients.
 *
 * <p>Every client is configured with:
 * <ul>
 *   <li>The Overcast endpoint override ({@code OVERCAST_ENDPOINT})</li>
 *   <li>Fake static credentials — Overcast accepts but does not validate them</li>
 *   <li>Path-style access for S3 (required for the local emulator)</li>
 *   <li>The SDK's Apache HTTP client for every sync client. Not
 *       {@code UrlConnectionHttpClient}: {@code HttpURLConnection} refuses the
 *       {@code PATCH} method outright, so every PATCH-bound operation (AppConfig's
 *       {@code Update*}, among others) failed in the client and never reached
 *       Overcast (#2261).</li>
 * </ul>
 *
 * <p>Clients are created lazily on first access and reused thereafter.
 * All fields are initialised in the constructor to avoid double-checked
 * locking; the factory itself is shared across all test groups.
 */
public final class AwsClients {

    private final URI endpoint;
    private final Region region;
    private final StaticCredentialsProvider credentials;

    // Lazily initialised clients
    private volatile S3Client s3;
    private volatile SqsClient sqs;
    private volatile DynamoDbClient dynamodb;
    private volatile SnsClient sns;
    private volatile LambdaClient lambda;
    private volatile LambdaAsyncClient lambdaAsync;
    private volatile StsClient sts;
    private volatile KmsClient kms;
    private volatile SecretsManagerClient secretsManager;
    private volatile SsmClient ssm;
    private volatile CloudWatchLogsClient cloudWatchLogs;
    private volatile SesClient ses;
    private volatile EventBridgeClient eventBridge;
    private volatile PipesClient pipes;
    private volatile CloudFormationClient cloudFormation;
    private volatile Ec2Client ec2;
    private volatile EcsClient ecs;
    private volatile EfsClient efs;
    private volatile ElastiCacheClient elasticache;
    private volatile CognitoIdentityProviderClient cognito;
    private volatile AppSyncClient appSync;
    private volatile ApiGatewayClient apiGateway;
    private volatile ApiGatewayV2Client apiGatewayV2;
    private volatile CloudFrontClient cloudFront;
    private volatile RdsClient rds;
    private volatile SfnClient sfn;
    private volatile Wafv2Client wafv2;
    private volatile ShieldClient shield;

    public AwsClients(String endpoint, String region) {
        this.endpoint    = URI.create(endpoint);
        this.region      = Region.of(region);
        this.credentials = StaticCredentialsProvider.create(
                AwsBasicCredentials.create("test", "test"));
    }

    /**
     * Applies this factory's endpoint, region, credentials and HTTP client to
     * any AWS SDK v2 sync client builder.
     *
     * <p>It exists for the generated scenario groups
     * ({@code io.overcast.compat.groups.Scenarios*Gen}), which cover services
     * this class has no accessor for and would otherwise need one added per
     * service the generator learns to cover. They still obtain their client
     * <em>through</em> this factory, so there is one description of how a client
     * in this suite is configured — a generated group's client differs from a
     * hand-written group's in nothing but the service.
     */
    public <C, B extends AwsSyncClientBuilder<B, C> & AwsClientBuilder<B, C>> B configure(B builder) {
        return builder
                .endpointOverride(endpoint)
                .region(region)
                .credentialsProvider(credentials)
                .httpClient(ApacheHttpClient.create());
    }

    public S3Client s3() {
        if (s3 == null) {
            synchronized (this) {
                if (s3 == null) {
                    s3 = S3Client.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .serviceConfiguration(S3Configuration.builder()
                                    .pathStyleAccessEnabled(true)
                                    .chunkedEncodingEnabled(false)
                                    .build())
                            .build();
                }
            }
        }
        return s3;
    }

    public SqsClient sqs() {
        if (sqs == null) {
            synchronized (this) {
                if (sqs == null) {
                    sqs = SqsClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return sqs;
    }

    public DynamoDbClient dynamoDb() {
        if (dynamodb == null) {
            synchronized (this) {
                if (dynamodb == null) {
                    dynamodb = DynamoDbClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return dynamodb;
    }

    public SnsClient sns() {
        if (sns == null) {
            synchronized (this) {
                if (sns == null) {
                    sns = SnsClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return sns;
    }

    public LambdaClient lambda() {
        if (lambda == null) {
            synchronized (this) {
                if (lambda == null) {
                    lambda = LambdaClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return lambda;
    }

    public LambdaAsyncClient lambdaAsync() {
        if (lambdaAsync == null) {
            synchronized (this) {
                if (lambdaAsync == null) {
                    lambdaAsync = LambdaAsyncClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .build();
                }
            }
        }
        return lambdaAsync;
    }

    public StsClient sts() {
        if (sts == null) {
            synchronized (this) {
                if (sts == null) {
                    sts = StsClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return sts;
    }

    public KmsClient kms() {
        if (kms == null) {
            synchronized (this) {
                if (kms == null) {
                    kms = KmsClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return kms;
    }

    public SecretsManagerClient secretsManager() {
        if (secretsManager == null) {
            synchronized (this) {
                if (secretsManager == null) {
                    secretsManager = SecretsManagerClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return secretsManager;
    }

    public SsmClient ssm() {
        if (ssm == null) {
            synchronized (this) {
                if (ssm == null) {
                    ssm = SsmClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return ssm;
    }

    public CloudWatchLogsClient cloudWatchLogs() {
        if (cloudWatchLogs == null) {
            synchronized (this) {
                if (cloudWatchLogs == null) {
                    cloudWatchLogs = CloudWatchLogsClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return cloudWatchLogs;
    }

    public SesClient ses() {
        if (ses == null) {
            synchronized (this) {
                if (ses == null) {
                    ses = SesClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return ses;
    }

    public EventBridgeClient eventBridge() {
        if (eventBridge == null) {
            synchronized (this) {
                if (eventBridge == null) {
                    eventBridge = EventBridgeClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return eventBridge;
    }

    public PipesClient pipes() {
        if (pipes == null) {
            synchronized (this) {
                if (pipes == null) {
                    pipes = PipesClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return pipes;
    }

    public CloudFormationClient cloudFormation() {
        if (cloudFormation == null) {
            synchronized (this) {
                if (cloudFormation == null) {
                    cloudFormation = CloudFormationClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return cloudFormation;
    }

    public Ec2Client ec2() {
        if (ec2 == null) {
            synchronized (this) {
                if (ec2 == null) {
                    ec2 = Ec2Client.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return ec2;
    }

    public EcsClient ecs() {
        if (ecs == null) {
            synchronized (this) {
                if (ecs == null) {
                    ecs = EcsClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return ecs;
    }

    public EfsClient efs() {
        if (efs == null) {
            synchronized (this) {
                if (efs == null) {
                    efs = EfsClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return efs;
    }

    public ElastiCacheClient elasticache() {
        if (elasticache == null) {
            synchronized (this) {
                if (elasticache == null) {
                    elasticache = ElastiCacheClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return elasticache;
    }

    public CognitoIdentityProviderClient cognito() {
        if (cognito == null) {
            synchronized (this) {
                if (cognito == null) {
                    cognito = CognitoIdentityProviderClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return cognito;
    }

    public AppSyncClient appSync() {
        if (appSync == null) {
            synchronized (this) {
                if (appSync == null) {
                    appSync = AppSyncClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return appSync;
    }

    public ApiGatewayClient apiGateway() {
        if (apiGateway == null) {
            synchronized (this) {
                if (apiGateway == null) {
                    apiGateway = ApiGatewayClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return apiGateway;
    }

    public ApiGatewayV2Client apiGatewayV2() {
        if (apiGatewayV2 == null) {
            synchronized (this) {
                if (apiGatewayV2 == null) {
                    apiGatewayV2 = ApiGatewayV2Client.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return apiGatewayV2;
    }

    public CloudFrontClient cloudFront() {
        if (cloudFront == null) {
            synchronized (this) {
                if (cloudFront == null) {
                    cloudFront = CloudFrontClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return cloudFront;
    }

    public RdsClient rds() {
        if (rds == null) {
            synchronized (this) {
                if (rds == null) {
                    rds = RdsClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return rds;
    }

    public SfnClient sfn() {
        if (sfn == null) {
            synchronized (this) {
                if (sfn == null) {
                    sfn = SfnClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return sfn;
    }

    public Wafv2Client wafv2() {
        if (wafv2 == null) {
            synchronized (this) {
                if (wafv2 == null) {
                    wafv2 = Wafv2Client.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return wafv2;
    }

    public ShieldClient shield() {
        if (shield == null) {
            synchronized (this) {
                if (shield == null) {
                    shield = ShieldClient.builder()
                            .endpointOverride(endpoint)
                            .region(region)
                            .credentialsProvider(credentials)
                            .httpClient(ApacheHttpClient.create())
                            .build();
                }
            }
        }
        return shield;
    }
}

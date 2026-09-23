// Package clients provides a lazy AWS client bundle for the Go SDK compat suite.
package clients

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/appsync"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/pipes"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/shield"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
)

// Clients holds lazily-initialised AWS service clients.
type Clients struct {
	endpoint string
	region   string

	mu           sync.Mutex
	cfg          *aws.Config
	s3C          *s3.Client
	sqsC         *sqs.Client
	dynamodbC    *dynamodb.Client
	snsC         *sns.Client
	lambdaC      *lambda.Client
	logsC        *cloudwatchlogs.Client
	sesC         *ses.Client
	iamC         *iam.Client
	stsC         *sts.Client
	smC          *secretsmanager.Client
	kmsC         *kms.Client
	ssmC         *ssm.Client
	kinesisC     *kinesis.Client
	eventbridgeC *eventbridge.Client
	pipesC       *pipes.Client
	cfnC         *cloudformation.Client
	ec2C         *ec2.Client
	ecsC         *ecs.Client
	elasticacheC *elasticache.Client
	cognitoC     *cognitoidentityprovider.Client
	appsyncC     *appsync.Client
	apigwC       *apigateway.Client
	apigwv2C     *apigatewayv2.Client
	cloudfrontC  *cloudfront.Client
	rdsC         *rds.Client
	sfnC         *sfn.Client
	wafv2C       *wafv2.Client
	shieldC      *shield.Client
	glueC        *glue.Client
	efsC         *efs.Client
}

// New creates a Clients bundle for the given endpoint and region.
func New(endpoint, region string) *Clients {
	return &Clients{endpoint: endpoint, region: region}
}

func (c *Clients) awsCfgLocked() aws.Config {
	// Must be called with c.mu held.
	if c.cfg != nil {
		return *c.cfg
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(c.region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("overcast", "overcast", ""),
		),
		awsconfig.WithBaseEndpoint(c.endpoint),
	)
	if err != nil {
		panic("clients: failed to load AWS config: " + err.Error())
	}
	c.cfg = &cfg
	return cfg
}

// Config returns the shared AWS config the per-service accessors above build
// their clients from.
//
// It exists for the generated scenario groups (internal/groups/scenarios_*_gen.go),
// which build one client per generated service themselves rather than adding a
// hand-written accessor here for every service the generator learns to cover.
// Hand-written groups still take their client from the accessors — a test
// function must never construct one (compat/suites/go-sdk/AGENTS.md).
func (c *Clients) Config() aws.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.awsCfgLocked()
}

// S3 returns a lazily-initialised S3 client.
func (c *Clients) S3() *s3.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.s3C == nil {
		cfg := c.awsCfgLocked()
		c.s3C = s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.UsePathStyle = true
		})
	}
	return c.s3C
}

// SQS returns a lazily-initialised SQS client.
func (c *Clients) SQS() *sqs.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sqsC == nil {
		cfg := c.awsCfgLocked()
		c.sqsC = sqs.NewFromConfig(cfg)
	}
	return c.sqsC
}

// DynamoDB returns a lazily-initialised DynamoDB client.
func (c *Clients) DynamoDB() *dynamodb.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dynamodbC == nil {
		cfg := c.awsCfgLocked()
		c.dynamodbC = dynamodb.NewFromConfig(cfg)
	}
	return c.dynamodbC
}

// SNS returns a lazily-initialised SNS client.
func (c *Clients) SNS() *sns.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snsC == nil {
		cfg := c.awsCfgLocked()
		c.snsC = sns.NewFromConfig(cfg)
	}
	return c.snsC
}

// Lambda returns a lazily-initialised Lambda client.
func (c *Clients) Lambda() *lambda.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lambdaC == nil {
		cfg := c.awsCfgLocked()
		c.lambdaC = lambda.NewFromConfig(cfg)
	}
	return c.lambdaC
}

// CloudWatchLogs returns a lazily-initialised CloudWatch Logs client.
func (c *Clients) CloudWatchLogs() *cloudwatchlogs.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logsC == nil {
		cfg := c.awsCfgLocked()
		c.logsC = cloudwatchlogs.NewFromConfig(cfg)
	}
	return c.logsC
}

// SES returns a lazily-initialised SES client.
func (c *Clients) SES() *ses.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sesC == nil {
		cfg := c.awsCfgLocked()
		c.sesC = ses.NewFromConfig(cfg)
	}
	return c.sesC
}

// IAM returns a lazily-initialised IAM client.
func (c *Clients) IAM() *iam.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.iamC == nil {
		cfg := c.awsCfgLocked()
		c.iamC = iam.NewFromConfig(cfg)
	}
	return c.iamC
}

// STS returns a lazily-initialised STS client.
func (c *Clients) STS() *sts.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stsC == nil {
		cfg := c.awsCfgLocked()
		c.stsC = sts.NewFromConfig(cfg)
	}
	return c.stsC
}

// SecretsManager returns a lazily-initialised Secrets Manager client.
func (c *Clients) SecretsManager() *secretsmanager.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.smC == nil {
		cfg := c.awsCfgLocked()
		c.smC = secretsmanager.NewFromConfig(cfg)
	}
	return c.smC
}

// KMS returns a lazily-initialised KMS client.
func (c *Clients) KMS() *kms.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kmsC == nil {
		cfg := c.awsCfgLocked()
		c.kmsC = kms.NewFromConfig(cfg)
	}
	return c.kmsC
}

// SSM returns a lazily-initialised SSM client.
func (c *Clients) SSM() *ssm.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ssmC == nil {
		cfg := c.awsCfgLocked()
		c.ssmC = ssm.NewFromConfig(cfg)
	}
	return c.ssmC
}

// Kinesis returns a lazily-initialised Kinesis client.
func (c *Clients) Kinesis() *kinesis.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kinesisC == nil {
		cfg := c.awsCfgLocked()
		c.kinesisC = kinesis.NewFromConfig(cfg)
	}
	return c.kinesisC
}

// EventBridge returns a lazily-initialised EventBridge client.
func (c *Clients) EventBridge() *eventbridge.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eventbridgeC == nil {
		cfg := c.awsCfgLocked()
		c.eventbridgeC = eventbridge.NewFromConfig(cfg)
	}
	return c.eventbridgeC
}

// Pipes returns a lazily-initialised EventBridge Pipes client.
func (c *Clients) Pipes() *pipes.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pipesC == nil {
		cfg := c.awsCfgLocked()
		c.pipesC = pipes.NewFromConfig(cfg)
	}
	return c.pipesC
}

// CloudFormation returns a lazily-initialised CloudFormation client.
func (c *Clients) CloudFormation() *cloudformation.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfnC == nil {
		cfg := c.awsCfgLocked()
		c.cfnC = cloudformation.NewFromConfig(cfg)
	}
	return c.cfnC
}

// EC2 returns a lazily-initialised EC2 client.
func (c *Clients) EC2() *ec2.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ec2C == nil {
		cfg := c.awsCfgLocked()
		c.ec2C = ec2.NewFromConfig(cfg)
	}
	return c.ec2C
}

// ECS returns a lazily-initialised ECS client.
func (c *Clients) ECS() *ecs.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ecsC == nil {
		cfg := c.awsCfgLocked()
		c.ecsC = ecs.NewFromConfig(cfg)
	}
	return c.ecsC
}

// ElastiCache returns a lazily-initialised ElastiCache client.
func (c *Clients) ElastiCache() *elasticache.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.elasticacheC == nil {
		cfg := c.awsCfgLocked()
		c.elasticacheC = elasticache.NewFromConfig(cfg)
	}
	return c.elasticacheC
}

// EFS returns a lazily-initialised Elastic File System client.
func (c *Clients) EFS() *efs.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.efsC == nil {
		cfg := c.awsCfgLocked()
		c.efsC = efs.NewFromConfig(cfg)
	}
	return c.efsC
}

// Cognito returns a lazily-initialised Cognito Identity Provider client.
func (c *Clients) Cognito() *cognitoidentityprovider.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cognitoC == nil {
		cfg := c.awsCfgLocked()
		c.cognitoC = cognitoidentityprovider.NewFromConfig(cfg)
	}
	return c.cognitoC
}

// AppSync returns a lazily-initialised AppSync client.
func (c *Clients) AppSync() *appsync.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appsyncC == nil {
		cfg := c.awsCfgLocked()
		c.appsyncC = appsync.NewFromConfig(cfg)
	}
	return c.appsyncC
}

// APIGateway returns a lazily-initialised API Gateway v1 client.
func (c *Clients) APIGateway() *apigateway.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.apigwC == nil {
		cfg := c.awsCfgLocked()
		c.apigwC = apigateway.NewFromConfig(cfg)
	}
	return c.apigwC
}

// APIGatewayV2 returns a lazily-initialised API Gateway v2 client.
func (c *Clients) APIGatewayV2() *apigatewayv2.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.apigwv2C == nil {
		cfg := c.awsCfgLocked()
		c.apigwv2C = apigatewayv2.NewFromConfig(cfg)
	}
	return c.apigwv2C
}

// CloudFront returns a lazily-initialised CloudFront client.
func (c *Clients) CloudFront() *cloudfront.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cloudfrontC == nil {
		cfg := c.awsCfgLocked()
		c.cloudfrontC = cloudfront.NewFromConfig(cfg)
	}
	return c.cloudfrontC
}

// RDS returns a lazily-initialised RDS client.
func (c *Clients) RDS() *rds.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rdsC == nil {
		cfg := c.awsCfgLocked()
		c.rdsC = rds.NewFromConfig(cfg)
	}
	return c.rdsC
}

// SFN returns a lazily-initialised Step Functions client.
func (c *Clients) SFN() *sfn.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sfnC == nil {
		cfg := c.awsCfgLocked()
		c.sfnC = sfn.NewFromConfig(cfg)
	}
	return c.sfnC
}

// WAFv2 returns a lazily-initialised WAF v2 client.
func (c *Clients) WAFv2() *wafv2.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wafv2C == nil {
		cfg := c.awsCfgLocked()
		c.wafv2C = wafv2.NewFromConfig(cfg)
	}
	return c.wafv2C
}

// Glue returns a lazily-initialised Glue client.
func (c *Clients) Glue() *glue.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.glueC == nil {
		cfg := c.awsCfgLocked()
		c.glueC = glue.NewFromConfig(cfg)
	}
	return c.glueC
}

// Shield returns a lazily-initialised Shield client.
func (c *Clients) Shield() *shield.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shieldC == nil {
		cfg := c.awsCfgLocked()
		c.shieldC = shield.NewFromConfig(cfg)
	}
	return c.shieldC
}

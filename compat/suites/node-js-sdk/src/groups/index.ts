/**
 * index.ts — the suite's complete group list, in registration order.
 *
 * Separate from `runner.ts`, which starts a run as a side effect of being
 * imported, so tests can resolve the suite's real impl keys against the real
 * registry.json without running anything.
 */
import type { TestGroup } from "../lib/harness.ts";
import type { ImplMap } from "../lib/registry.ts";
import { mergeImpls } from "../lib/registry.ts";
import { makeS3Groups } from "./s3.ts";
import { makeSQSGroups } from "./sqs.ts";
import { makeDynamoDBGroups } from "./dynamodb.ts";
import { makeSNSGroups } from "./sns.ts";
import { makeLambdaGroups } from "./lambda.ts";
import { makeSESGroups } from "./ses.ts";
import { makeIAMGroups } from "./iam.ts";
import { makeSTSGroups } from "./sts.ts";
import { makeSecretsManagerGroups } from "./secretsmanager.ts";
import { makeKMSGroups } from "./kms.ts";
import { makeSSMGroups } from "./ssm.ts";
import { makeKinesisGroups } from "./kinesis.ts";
import { makeEventBridgeGroups } from "./eventbridge.ts";
import { makePipesGroups } from "./pipes.ts";
import { makeCloudFormationGroups } from "./cloudformation.ts";
import { makeEC2Groups } from "./ec2.ts";
import { makeECSGroups } from "./ecs.ts";
import { makeCognitoGroups } from "./cognito.ts";
import { makeAppSyncGroups } from "./appsync.ts";
import { makeAPIGatewayGroups } from "./apigateway.ts";
import { makeCloudFrontGroups } from "./cloudfront.ts";
import { makeRDSGroups } from "./rds.ts";
import { makeElastiCacheGroups } from "./elasticache.ts";
import { makeStepFunctionsGroups } from "./stepfunctions.ts";
import { makeWAFGroups } from "./waf.ts";
import { makeShieldGroups } from "./shield.ts";
import { makeGlueGroups } from "./glue.ts";
import { makeAthenaGroups } from "./athena.ts";
import { makeEFSGroups } from "./efs.ts";

/** Every group the suite implements, in registration order. */
export function makeAllGroups(suite: string): TestGroup[] {
  return [
    ...makeS3Groups(suite),
    ...makeSQSGroups(suite),
    ...makeDynamoDBGroups(suite),
    ...makeSNSGroups(suite),
    ...makeLambdaGroups(suite),
    ...makeSESGroups(suite),
    ...makeIAMGroups(suite),
    ...makeSTSGroups(suite),
    ...makeSecretsManagerGroups(suite),
    ...makeKMSGroups(suite),
    ...makeSSMGroups(suite),
    ...makeKinesisGroups(suite),
    ...makeEventBridgeGroups(suite),
    ...makePipesGroups(suite),
    ...makeCloudFormationGroups(suite),
    ...makeEC2Groups(suite),
    ...makeECSGroups(suite),
    ...makeCognitoGroups(suite),
    ...makeAppSyncGroups(suite),
    ...makeAPIGatewayGroups(suite),
    ...makeCloudFrontGroups(suite),
    ...makeRDSGroups(suite),
    ...makeElastiCacheGroups(suite),
    ...makeStepFunctionsGroups(suite),
    ...makeWAFGroups(suite),
    ...makeShieldGroups(suite),
    ...makeGlueGroups(suite),
    ...makeAthenaGroups(suite),
    ...makeEFSGroups(suite),
  ];
}

/**
 * The suite's impl map: group-qualified keys only.
 *
 * A bare test name cannot say which group it implements when several groups
 * declare it, so every key carries its group — see `validateImpls`.
 *
 * Built through `mergeImpls` rather than by assigning into one object, so a key
 * two group files both produce is refused instead of silently discarding one
 * implementation. Each test is its own source, which catches a group listing
 * the same test twice as well as two group files claiming the same group name.
 * Sources are labelled `service/group` — a bare group name would not say which
 * file to look in when two files disagree about who owns it.
 */
export function makeImplMap(groups: TestGroup[], suite: string): ImplMap {
  return mergeImpls(
    groups.flatMap((g) =>
      g.tests
        .filter((t) => !t.skip)
        .map((t) => ({
          name: `${g.service}/${g.name}`,
          impls: { [`${g.name}:${t.name}`]: t.fn },
        })),
    ),
    suite,
  );
}

import { awsClients } from "../aws-clients"
import { overcastFetch } from "./base"
import {
  ListSecretsCommand,
  CreateSecretCommand,
  DescribeSecretCommand,
  GetSecretValueCommand,
  PutSecretValueCommand,
  DeleteSecretCommand,
  GetResourcePolicyCommand,
} from "@aws-sdk/client-secrets-manager"

/** One version of a secret, with the staging labels attached to it. */
export interface SecretVersionSummary {
  versionId: string
  stages: string[]
  createdDate: number
}

/** The outcome of the most recent rotation run. */
export interface RotationAttempt {
  status?: string
  step?: string
  error?: string
  trigger?: string
  clientRequestToken?: string
  startedDate?: number
  completedDate?: number
}

/**
 * Rotation status for one secret.
 *
 * Sourced from Overcast's own `/_overcast/` endpoint rather than an AWS API:
 * real Secrets Manager reports rotation progress through CloudTrail and the
 * console, so there is no AWS operation that returns "which step failed".
 */
export interface SecretRotationStatus {
  name: string
  arn: string
  rotationEnabled: boolean
  rotationLambdaArn?: string
  rotationRules?: {
    AutomaticallyAfterDays?: number
    Duration?: string
    ScheduleExpression?: string
  } | null
  lastRotatedDate?: number
  nextRotationDate?: number
  versions: SecretVersionSummary[]
  steps: string[]
  lastAttempt?: RotationAttempt
}

export const secretsmanager = {
  listSecrets: async () => {
    const res = await awsClients.secretsmanager().send(new ListSecretsCommand({}))
    return res.SecretList ?? []
  },

  createSecret: async (params: { Name: string; SecretString?: string; Description?: string }) => {
    return await awsClients.secretsmanager().send(new CreateSecretCommand(params))
  },

  describeSecret: async (secretId: string) => {
    return await awsClients.secretsmanager().send(new DescribeSecretCommand({ SecretId: secretId }))
  },

  getSecretValue: async (secretId: string) => {
    const res = await awsClients
      .secretsmanager()
      .send(new GetSecretValueCommand({ SecretId: secretId }))
    return {
      ...res,
      SecretBinary: res.SecretBinary ? btoa(String.fromCharCode(...res.SecretBinary)) : undefined,
    }
  },

  updateSecretValue: async (secretId: string, secretString: string) => {
    return await awsClients
      .secretsmanager()
      .send(new PutSecretValueCommand({ SecretId: secretId, SecretString: secretString }))
  },

  deleteSecret: async (secretId: string) => {
    await awsClients
      .secretsmanager()
      .send(new DeleteSecretCommand({ SecretId: secretId, ForceDeleteWithoutRecovery: true }))
  },

  /**
   * The secret's resource policy, or null when none is attached.
   *
   * Overcast stores and validates this policy but does not evaluate it — see
   * docs/services/secretsmanager.md. The UI says so where it shows it.
   */
  getResourcePolicy: async (secretId: string): Promise<string | null> => {
    const res = await awsClients
      .secretsmanager()
      .send(new GetResourcePolicyCommand({ SecretId: secretId }))
    return res.ResourcePolicy ?? null
  },

  getRotationStatus: (secretId: string): Promise<SecretRotationStatus> =>
    overcastFetch<SecretRotationStatus>(
      `/_overcast/secretsmanager/secrets/${encodeURIComponent(secretId)}/rotation`,
    ),
}

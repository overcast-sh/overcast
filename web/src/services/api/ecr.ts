import {
  BatchGetImageCommand,
  CreateRepositoryCommand,
  DeleteRepositoryCommand,
  DescribeRepositoriesCommand,
  GetAuthorizationTokenCommand,
  type ImageDetail,
  paginateDescribeImages,
  paginateDescribeRepositories,
} from "@aws-sdk/client-ecr"
import { awsClients } from "../aws-clients"
import type {
  EcrAuthorizationToken,
  EcrRepository,
  EcrRepositoryDetail,
  EcrImageDetail,
} from "@/types"

function decodeToken(token?: string): EcrAuthorizationToken | undefined {
  if (!token) return undefined
  try {
    const decoded = atob(token)
    const [username, password] = decoded.split(":", 2)
    if (!username || !password) return undefined
    return { username, password, proxyEndpoint: "" }
  } catch {
    return undefined
  }
}

function mapRepository(repo: {
  repositoryName?: string
  repositoryArn?: string
  repositoryUri?: string
  registryId?: string
  createdAt?: Date
  imageTagMutability?: string
}): EcrRepository {
  return {
    name: repo.repositoryName ?? "",
    arn: repo.repositoryArn ?? "",
    uri: repo.repositoryUri ?? "",
    registryId: repo.registryId ?? "",
    createdAt: repo.createdAt?.getTime(),
    imageTagMutability: repo.imageTagMutability,
  }
}

function mapImage(detail: {
  imageDigest?: string
  imageTags?: string[]
  imageManifestMediaType?: string
}): EcrImageDetail {
  return {
    digest: detail.imageDigest ?? "",
    tags: detail.imageTags ?? [],
    mediaType: detail.imageManifestMediaType,
  }
}

async function listImageDetails(repositoryName: string) {
  const out: ImageDetail[] = []
  for await (const page of paginateDescribeImages(
    { client: awsClients.ecr() },
    { repositoryName },
  )) {
    out.push(...(page.imageDetails ?? []))
  }
  return out
}

export const ecr = {
  listRepositories: async (): Promise<EcrRepository[]> => {
    // DescribeRepositories returns at most 100 repositories per page, as on AWS.
    const repos: EcrRepository[] = []
    for await (const page of paginateDescribeRepositories({ client: awsClients.ecr() }, {})) {
      repos.push(...(page.repositories ?? []).map(mapRepository))
    }
    return repos
  },

  getRepository: async (name: string): Promise<EcrRepositoryDetail> => {
    const client = awsClients.ecr()
    const [reposRes, imagesRes, authRes] = await Promise.all([
      client.send(new DescribeRepositoriesCommand({ repositoryNames: [name] })),
      listImageDetails(name).catch(() => []),
      client.send(new GetAuthorizationTokenCommand({})).catch(() => ({ authorizationData: [] })),
    ])
    const repo = reposRes.repositories?.[0]
    if (!repo) throw new Error(`Repository ${name} not found`)
    const authEntry = authRes.authorizationData?.[0]
    const decoded = decodeToken(authEntry?.authorizationToken)
    return {
      ...mapRepository(repo),
      images: imagesRes.map(mapImage),
      login:
        authEntry?.proxyEndpoint && decoded
          ? {
              ...decoded,
              proxyEndpoint: authEntry.proxyEndpoint,
              expiresAt: authEntry.expiresAt?.getTime(),
            }
          : undefined,
    }
  },

  createRepository: async (name: string) => {
    await awsClients.ecr().send(new CreateRepositoryCommand({ repositoryName: name }))
  },

  deleteRepository: async (name: string) => {
    await awsClients.ecr().send(new DeleteRepositoryCommand({ repositoryName: name, force: true }))
  },

  batchGetImage: async (repositoryName: string, imageTag: string) => {
    const res = await awsClients.ecr().send(
      new BatchGetImageCommand({
        repositoryName,
        imageIds: [{ imageTag }],
      }),
    )
    return res.images?.[0]
  },
}

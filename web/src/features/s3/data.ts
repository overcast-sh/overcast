/**
 * S3 query/mutation definitions.
 *
 * Key factory follows the hierarchical pattern recommended by TkDodo:
 *   s3Keys.all                                  -> ["s3"]
 *   s3Keys.buckets()                            -> ["s3", "buckets"]
 *   s3Keys.bucketList(baseUrl)                  -> ["s3", "buckets", baseUrl]
 *   s3Keys.objects()                            -> ["s3", "objects"]
 *   s3Keys.objectList(baseUrl, bucket, prefix)  -> ["s3", "objects", baseUrl, bucket, prefix]
 *   s3Keys.meta()                               -> ["s3", "meta"]
 *   s3Keys.objectMeta(baseUrl, bucket, key, versionId)
 *                                               -> ["s3", "meta", baseUrl, bucket, key, versionId ?? null]
 *
 * Queries — pass directly to useQuery:
 *   useQuery(s3Queries.buckets(endpoint.baseUrl))
 *
 * Mutations — all factories, always called. Spread into useMutation and add callbacks:
 *   useMutation({ ...deleteBucketMutationOptions(), onSuccess: () => ... })
 *   useMutation({ ...deleteObjectMutationOptions(bucket), onSuccess: () => ... })
 *
 * Mutation keys enable useIsMutating / useMutationState across components:
 *   useIsMutating({ mutationKey: deleteBucketMutationOptions().mutationKey })
 *
 * Invalidation uses the coarse keys so it catches all related queries:
 *   qc.invalidateQueries({ queryKey: s3Keys.buckets() })
 *   qc.invalidateQueries({ queryKey: s3Keys.objectList(baseUrl, bucket, prefix) })
 */

import { queryOptions, infiniteQueryOptions, mutationOptions } from "@tanstack/react-query"
import { s3 } from "@/services/api"
import { endpointStore } from "@/services/endpoint-store"
import type { S3ObjectVersion } from "@/types"

// ─── Key factory ───────────────────────────────────────────────────────────

/**
 * How far a listing reaches. `"folder"` sends a `/` delimiter so nested keys
 * come back rolled up as folders; `"recursive"` drops it so every key beneath
 * the prefix is listed flat. The two answer different questions and cache
 * separately, which is why the scope is part of the key.
 */
export type ListScope = "folder" | "recursive"

/**
 * Keys per page. A recursive listing exists to be scanned end to end — for a
 * search or a non-default sort — so it trades a larger first response for far
 * fewer round trips; folder browsing keeps the smaller page that paints sooner.
 */
const PAGE_SIZE: Record<ListScope, number> = { folder: 200, recursive: 1000 }

export const s3Keys = {
  all: () => [...endpointStore.getKeys(), "s3"] as const,
  buckets: () => [...s3Keys.all(), "buckets"] as const,
  objects: () => [...s3Keys.all(), "objects"] as const,
  objectList: (bucket: string, prefix: string, scope: ListScope = "folder") =>
    [...s3Keys.objects(), bucket, prefix, scope] as const,
  versions: () => [...s3Keys.all(), "versions"] as const,
  versionList: (bucket: string, prefix: string, scope: ListScope = "folder") =>
    [...s3Keys.versions(), bucket, prefix, scope] as const,
  // One key's own history, which is a different question from versionList's
  // prefix-wide listing. Nested under versions() so that deleting a version
  // invalidates both.
  objectHistory: (bucket: string, key: string) =>
    [...s3Keys.versions(), "history", bucket, key] as const,
  versioning: () => [...s3Keys.all(), "versioning"] as const,
  bucketVersioning: (bucket: string) => [...s3Keys.versioning(), bucket] as const,
  meta: () => [...s3Keys.all(), "meta"] as const,
  // The version id is part of the key, so two revisions of one object are two
  // cache entries rather than one that overwrites the other. `null` stands for
  // "whichever is current" and cannot collide with the literal id `"null"`,
  // which is a real version id and stays a string.
  objectMeta: (bucket: string, key: string, versionId?: string) =>
    [...s3Keys.meta(), bucket, key, versionId ?? null] as const,
  objectPreview: (bucket: string, key: string, versionId?: string) =>
    [...s3Keys.objectMeta(bucket, key, versionId), "preview"] as const,
  notification: () => [...s3Keys.all(), "notification"] as const,
  bucketNotification: (bucket: string) => [...s3Keys.notification(), bucket] as const,
  lifecycle: () => [...s3Keys.all(), "lifecycle"] as const,
  bucketLifecycle: (bucket: string) => [...s3Keys.lifecycle(), bucket] as const,
  website: () => [...s3Keys.all(), "website"] as const,
  bucketWebsite: (bucket: string) => [...s3Keys.website(), bucket] as const,
}

// ─── Query definitions ─────────────────────────────────────────────────────

export function s3BucketsQueryOptions() {
  return queryOptions({
    queryKey: s3Keys.buckets(),
    queryFn: () => s3.listBuckets(),
  })
}

export function s3ObjectsQueryOptions(bucket: string, prefix: string, scope: ListScope = "folder") {
  return infiniteQueryOptions({
    queryKey: s3Keys.objectList(bucket, prefix, scope),
    queryFn: ({ pageParam }) =>
      s3.listObjects(bucket, {
        prefix,
        delimiter: scope === "recursive" ? "" : "/",
        maxKeys: PAGE_SIZE[scope],
        token: pageParam,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.nextContinuationToken ?? undefined,
  })
}

/**
 * Every version and delete marker under a prefix. Separate from
 * s3ObjectsQueryOptions because it answers a different question: that one shows
 * what the bucket currently holds, this one shows what it is still storing.
 */
export function s3ObjectVersionsQueryOptions(
  bucket: string,
  prefix: string,
  scope: ListScope = "folder",
) {
  return infiniteQueryOptions({
    queryKey: s3Keys.versionList(bucket, prefix, scope),
    queryFn: ({ pageParam }) =>
      s3.listObjectVersions(bucket, {
        prefix,
        delimiter: scope === "recursive" ? "" : "/",
        maxKeys: PAGE_SIZE[scope],
        keyMarker: pageParam?.keyMarker,
        versionIdMarker: pageParam?.versionIdMarker,
      }),
    initialPageParam: undefined as { keyMarker?: string; versionIdMarker?: string } | undefined,
    getNextPageParam: (lastPage) =>
      lastPage.isTruncated && lastPage.nextKeyMarker
        ? { keyMarker: lastPage.nextKeyMarker, versionIdMarker: lastPage.nextVersionIdMarker }
        : undefined,
  })
}

/** How much of one object's history is read in a single pass. */
const HISTORY_PAGE_SIZE = 200

/** Every stored revision of one key, newest first. */
export interface ObjectHistory {
  versions: S3ObjectVersion[]
  /** This key has more revisions than were read. */
  isTruncated: boolean
}

/**
 * One object's own history — every version and delete marker stored under
 * exactly this key.
 *
 * Distinct from {@link s3ObjectVersionsQueryOptions}, which answers "what is
 * this bucket still storing beneath a prefix". This one answers "what happened
 * to this object", which is the question a version list inside the inspector
 * exists for.
 */
export function s3ObjectHistoryQueryOptions(bucket: string, key: string) {
  return queryOptions({
    queryKey: s3Keys.objectHistory(bucket, key),
    queryFn: async (): Promise<ObjectHistory> => {
      const res = await s3.listObjectVersions(bucket, {
        prefix: key,
        delimiter: "",
        maxKeys: HISTORY_PAGE_SIZE,
      })
      // ListObjectVersions filters by prefix, not by key: asking for
      // `logs/app.log` also answers with every version of `logs/app.log.bak`,
      // and showing a neighbour's revisions as this object's history would be
      // wrong in exactly the buckets where it matters.
      const versions = res.versions.filter((v) => v.key === key)
      return {
        versions,
        // Keys come back ascending, so this key's own versions lead the
        // answer: a truncated page has cut into them only if it filled up
        // before reaching the next key.
        isTruncated: res.isTruncated && res.versions.at(-1)?.key === key,
      }
    },
  })
}

/** The bucket's versioning state, which decides whether versions exist at all. */
export function s3BucketVersioningQueryOptions(bucket: string) {
  return queryOptions({
    queryKey: s3Keys.bucketVersioning(bucket),
    queryFn: () => s3.getBucketVersioning(bucket),
  })
}

/**
 * HeadObject for one object. `versionId` names a stored revision; omit it for
 * whichever version is current.
 */
export function s3ObjectMetaQueryOptions(bucket: string, key: string, versionId?: string) {
  return queryOptions({
    queryKey: s3Keys.objectMeta(bucket, key, versionId),
    queryFn: () => s3.getObjectMetadata(bucket, key, versionId),
  })
}

export function s3ObjectPreviewQueryOptions(bucket: string, key: string, versionId?: string) {
  return queryOptions({
    queryKey: s3Keys.objectPreview(bucket, key, versionId),
    queryFn: () => s3.getObjectText(bucket, key, versionId),
  })
}

export function s3BucketNotificationQueryOptions(bucket: string) {
  return queryOptions({
    queryKey: s3Keys.bucketNotification(bucket),
    queryFn: () => s3.getBucketNotification(bucket),
  })
}

/**
 * The bucket's lifecycle rules, or null when it has none. Feeds both the
 * configuration tab's rule list and the object list's per-row expiry hint, so
 * the object list costs one extra request per bucket rather than one per row.
 */
export function s3BucketLifecycleQueryOptions(bucket: string) {
  return queryOptions({
    queryKey: s3Keys.bucketLifecycle(bucket),
    queryFn: () => s3.getBucketLifecycle(bucket),
  })
}

/**
 * The bucket's website configuration, or null when it is not a website bucket.
 * Redirect-all and routing rules are read-only here: they are set through
 * `PutBucketWebsite` or a CloudFormation stack, and the panel exists to make
 * visible that requests to this bucket's site would be redirected.
 */
export function s3BucketWebsiteQueryOptions(bucket: string) {
  return queryOptions({
    queryKey: s3Keys.bucketWebsite(bucket),
    queryFn: () => s3.getBucketWebsite(bucket),
  })
}

export function s3BucketExistsQueryOptions(bucket: string) {
  return queryOptions({
    queryKey: [...s3Keys.all(), "bucket-exists", bucket] as const,
    queryFn: () => s3.headBucket(bucket),
    retry: false,
    staleTime: 30_000,
  })
}

// ─── Mutation definitions ──────────────────────────────────────────────────
//
// All are factory functions — always called, never referenced directly.
// This keeps the call pattern uniform whether or not args are needed, and
// puts any scoping params (bucket) into the key for precise observation:
//   useIsMutating({ mutationKey: deleteObjectMutationOptions(bucket).mutationKey })

export function createBucketMutationOptions() {
  return mutationOptions({
    mutationKey: [...s3Keys.buckets(), "create"] as const,
    mutationFn: ({
      name,
      region,
      namespace,
    }: {
      name: string
      region?: string
      /** Set only for an account-regional namespace bucket — see s3.createBucket. */
      namespace?: "account-regional"
    }) => s3.createBucket(name, region, namespace),
  })
}

export function deleteBucketMutationOptions() {
  return mutationOptions({
    mutationKey: [...s3Keys.buckets(), "delete"] as const,
    mutationFn: (name: string) => s3.deleteBucket(name),
  })
}

export function deleteObjectMutationOptions(bucket: string) {
  return mutationOptions({
    mutationKey: [...s3Keys.objectList(bucket, "*"), "delete"] as const,
    mutationFn: (key: string) => s3.deleteObject(bucket, key),
  })
}

export function deleteObjectVersionMutationOptions(bucket: string) {
  return mutationOptions({
    mutationKey: [...s3Keys.versionList(bucket, "*"), "delete"] as const,
    mutationFn: ({ key, versionId }: { key: string; versionId: string }) =>
      s3.deleteObjectVersion(bucket, key, versionId),
  })
}

export function deleteByPrefixMutationOptions(bucket: string) {
  return mutationOptions({
    mutationKey: [...s3Keys.objectList(bucket, "*"), "delete-prefix"] as const,
    mutationFn: (prefix: string) => s3.deleteObjectsByPrefix(bucket, prefix),
  })
}

/**
 * Turns version history on, or suspends it.
 *
 * Both directions invalidate the object and version listings as well as the
 * status itself: what a listing means changes with the state, and a suspended
 * bucket keeps serving the history it already has.
 */
export function putBucketVersioningMutationOptions(bucket: string) {
  return mutationOptions({
    mutationKey: [...s3Keys.versioning(), bucket, "put"] as const,
    mutationFn: (status: "Enabled" | "Suspended") => s3.putBucketVersioning(bucket, status),
  })
}

export function putBucketNotificationMutationOptions(bucket: string) {
  return mutationOptions({
    mutationKey: [...s3Keys.notification(), bucket, "put"] as const,
    mutationFn: (config: Parameters<typeof s3.putBucketNotification>[1]) =>
      s3.putBucketNotification(bucket, config),
  })
}

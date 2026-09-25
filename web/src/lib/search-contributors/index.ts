/**
 * Imports all search contributors, triggering their `registerContributor` side-effects.
 * Import this file once from the application entry point or search component.
 *
 * Each contributor's cacheKey reuses the corresponding feature's own key
 * factory (e.g. s3Keys.buckets()) rather than hand-building the array, so the
 * shape — including endpoint/region scoping — can never drift out of sync
 * with the query it's meant to hit. See create-contributor.ts.
 */
import "./s3"
import "./sqs"
import "./sns"
import "./dynamodb"
import "./kinesis"
import "./lambda"
import "./secretsmanager"
import "./logs"
import "./elasticache"
import "./msk"
import "./ecr"
import "./waf"
import "./athena"
import "./glue"
import "./s3tables"
import "./inbox"
import "./docs"

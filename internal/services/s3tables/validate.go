package s3tables

// Names, configuration shapes and the modeled errors.
//
// Naming rules are AWS's, from "Amazon S3 table bucket, table, and namespace
// naming rules":
// https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-buckets-naming.html
//
// Error messages: where a message has been observed from AWS (or is quoted by
// the re:Post and moto corpora that record AWS's answers) it is used verbatim.
// The rest are marked in place as unverified; the code and status always come
// from the model, which is what SDKs branch on.

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ─── Errors ───────────────────────────────────────────────────────────────────

func badRequest(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "BadRequestException", Message: msg, HTTPStatus: http.StatusBadRequest}
}

func notFound(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "NotFoundException", Message: msg, HTTPStatus: http.StatusNotFound}
}

func conflict(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "ConflictException", Message: msg, HTTPStatus: http.StatusConflict}
}

// validationError is the model-validation error AWS answers a constraint
// violation with: "1 validation error detected: " and the constraint.
func validationError(format string, args ...any) *protocol.AWSError {
	return badRequest("1 validation error detected: " + fmt.Sprintf(format, args...))
}

// enumError is validationError for a member outside its enum.
func enumError(field, value string, allowed ...string) *protocol.AWSError {
	return validationError("Value '%s' at '%s' failed to satisfy constraint: Member must satisfy enum value set: [%s]",
		value, field, strings.Join(allowed, ", "))
}

func notImplemented(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: protocol.ErrNotImplemented.Code, Message: msg, HTTPStatus: http.StatusNotImplemented}
}

var (
	errBucketNotFound    = notFound("The specified bucket does not exist.")
	errNamespaceNotFound = notFound("The specified namespace does not exist.")
	errTableNotFound     = notFound("The specified table does not exist.")
	errBucketExists      = conflict("The bucket that you tried to create already exists, and you own it.")
	errNamespaceExists   = conflict("A namespace with an identical name already exists in the bucket.")
	errTableExists       = conflict("A table with an identical name already exists in the namespace.")
	errVersionMismatch   = conflict("Provided version token does not match the table version token.")
	errBadToken          = badRequest("The continuation token is not valid.")
	errBadMetadataLoc    = badRequest("The specified metadata location is not valid.")
	errNothingToRename   = badRequest("Neither a new namespace name nor a new table name is specified.")
	errDestNamespace     = notFound("The specified destination namespace does not exist.")
	// "The bucket that you tried to delete is not empty" is the message AWS
	// users report from DeleteTableBucket on a bucket that still has
	// namespaces. The ConflictException code is inferred (409 is the model's
	// only "state forbids this" error).
	errBucketNotEmpty = conflict("The bucket that you tried to delete is not empty.")
	// Unverified: modeled on the bucket message above.
	errNamespaceNotEmpty = conflict("The namespace that you tried to delete is not empty.")
	// Unverified wording for the absent-configuration reads.
	errNoBucketPolicy     = notFound("The specified bucket policy does not exist.")
	errNoTablePolicy      = notFound("The specified table policy does not exist.")
	errNoMetricsConfig    = notFound("The specified bucket does not have a metrics configuration.")
	errNoReplicationBkt   = notFound("The specified bucket does not have a replication configuration.")
	errNoReplicationTable = notFound("The specified table does not have a replication configuration.")
)

// ─── Names ────────────────────────────────────────────────────────────────────

var (
	tableBucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	// namespaceOrTableName: lowercase letters, numbers and underscores,
	// beginning with a letter or number.
	namespaceOrTableName = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,254}$`)

	tableBucketReservedPrefixes = []string{"xn--", "sthree-", "amzn-s3-demo-", "aws"}
	tableBucketReservedSuffixes = []string{"-s3alias", "--ol-s3", "--x-s3", "--table-s3"}
)

// validateTableBucketName applies the table bucket naming rules.
func validateTableBucketName(name string) *protocol.AWSError {
	bad := badRequest("The specified bucket name is not valid.")
	if !tableBucketNamePattern.MatchString(name) {
		return bad
	}
	for _, p := range tableBucketReservedPrefixes {
		if strings.HasPrefix(name, p) {
			return bad
		}
	}
	for _, sfx := range tableBucketReservedSuffixes {
		if strings.HasSuffix(name, sfx) {
			return bad
		}
	}
	return nil
}

// validateNamespaceName applies the namespace naming rules, which add the
// reserved "aws" prefix to the rules tables share.
func validateNamespaceName(name string) *protocol.AWSError {
	if !namespaceOrTableName.MatchString(name) || strings.HasPrefix(name, "aws") {
		return badRequest("The specified namespace name is not valid.")
	}
	return nil
}

// validateTableName applies the table naming rules. The message is the
// model-validation wording AWS answers a table name with.
func validateTableName(name string) *protocol.AWSError {
	if !namespaceOrTableName.MatchString(name) {
		return validationError("Value '%s' at 'name' failed to satisfy constraint: Member must satisfy regular expression pattern: [0-9a-z_]*", name)
	}
	return nil
}

// ─── Configuration shapes ─────────────────────────────────────────────────────

func validateEncryption(c *encryptionConfiguration) *protocol.AWSError {
	if c == nil {
		return badRequest("encryptionConfiguration is required.")
	}
	switch c.SSEAlgorithm {
	case sseAES256:
		if c.KMSKeyARN != "" {
			return badRequest("kmsKeyArn must not be specified when sseAlgorithm is AES256.")
		}
	case sseKMS:
		if c.KMSKeyARN == "" {
			return badRequest("kmsKeyArn must be specified when sseAlgorithm is aws:kms.")
		}
	default:
		return enumError("encryptionConfiguration.sseAlgorithm", c.SSEAlgorithm, sseAES256, sseKMS)
	}
	return nil
}

func validateStorageClass(c *storageClassConfiguration) *protocol.AWSError {
	if c == nil {
		return badRequest("storageClassConfiguration is required.")
	}
	switch c.StorageClass {
	case storageStandard, storageIntelligentTiering:
		return nil
	}
	return enumError("storageClassConfiguration.storageClass", c.StorageClass, storageStandard, storageIntelligentTiering)
}

func validateStatus(status, field string) *protocol.AWSError {
	switch status {
	case "", statusEnabled, statusDisabled:
		return nil
	}
	return enumError(field, status, statusEnabled, statusDisabled)
}

// validateMaintenance checks a maintenance value against the type its path
// names: the settings union may only carry that type's member.
func validateMaintenance(typ string, v *maintenanceValue, allowed ...string) *protocol.AWSError {
	if !slices.Contains(allowed, typ) {
		return enumError("type", typ, allowed...)
	}
	if v == nil {
		return badRequest("value is required.")
	}
	if aerr := validateStatus(v.Status, "value.status"); aerr != nil {
		return aerr
	}
	if v.Settings == nil {
		return nil
	}
	set := map[string]bool{
		maintUnreferencedFileRemoval: v.Settings.IcebergUnreferencedFileRemoval != nil,
		maintCompaction:              v.Settings.IcebergCompaction != nil,
		maintSnapshotManagement:      v.Settings.IcebergSnapshotManagement != nil,
	}
	for member, present := range set {
		if present && member != typ {
			return badRequest(fmt.Sprintf("The settings for maintenance type %s cannot contain %s.", typ, member))
		}
	}
	if c := v.Settings.IcebergCompaction; c != nil && c.Strategy != "" {
		strategies := []string{"auto", "binpack", "sort", "z-order"}
		if !slices.Contains(strategies, c.Strategy) {
			return enumError("value.settings.icebergCompaction.strategy", c.Strategy, strategies...)
		}
	}
	return nil
}

// iamRolePattern is the model's IAMRole pattern.
var iamRolePattern = regexp.MustCompile(`^arn:.+:iam::[0-9]{12}:role/.+$`)

func validateReplication(c *replicationConfiguration) *protocol.AWSError {
	if c == nil {
		return badRequest("configuration is required.")
	}
	if len(c.Role) < 20 || !iamRolePattern.MatchString(c.Role) {
		return badRequest("The specified IAM role is not valid.")
	}
	if len(c.Rules) != 1 {
		return badRequest("A replication configuration must contain exactly one rule.")
	}
	dests := c.Rules[0].Destinations
	if len(dests) < 1 || len(dests) > 5 {
		return badRequest("A replication rule must contain between 1 and 5 destinations.")
	}
	for _, d := range dests {
		if _, aerr := parseBucketARN(d.DestinationTableBucketARN); aerr != nil {
			return aerr
		}
	}
	return nil
}

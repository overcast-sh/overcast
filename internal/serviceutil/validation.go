package serviceutil

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ---- S3 validation ---------------------------------------------------------

var (
	// validBucketName matches the character and structure rules for an S3
	// general purpose bucket name: 3–63 chars of lowercase letters, numbers,
	// periods and hyphens, beginning and ending with a letter or number.
	// Periods ARE legal — AWS documents example.com and my.example.s3.bucket
	// as valid — they are merely discouraged, because they break the
	// *.s3.<region>.amazonaws.com wildcard certificate for virtual-hosted-style
	// addressing over HTTPS.
	// https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html
	validBucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{1,61}[a-z0-9]$`)
	ipAddress       = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)

	// reservedBucketPrefixes and reservedBucketSuffixes are the namespaces AWS
	// reserves for access point aliases, Object Lambda, Multi-Region Access
	// Points, directory buckets and S3 Tables. Listed in the same order as the
	// naming-rules page so the two can be diffed.
	reservedBucketPrefixes = []string{"xn--", "sthree-", "amzn-s3-demo-"}
	reservedBucketSuffixes = []string{"-s3alias", "--ol-s3", ".mrap", "--x-s3", "--table-s3"}

	// GraphQLIdentifierPattern matches AppSync GraphQL identifiers documented for
	// data source, function, type, and field names.
	// https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateDataSource.html
	GraphQLIdentifierPattern = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)
)

// NameRule describes one AWS resource-name validation rule. It intentionally
// carries service-specific error details because AWS does not use one global
// validation error shape across services.
type NameRule struct {
	MinLength      int
	MaxLength      int
	Allowed        func(rune) bool
	Pattern        *regexp.Regexp
	ErrorCode      string
	LengthMessage  string
	AllowedMessage string
	PatternMessage string
	HTTPStatus     int
}

// ResourceName validates name with a reusable rule and returns the configured
// AWS-style error. Service-specific validators should wrap this helper rather
// than exposing generic resource-name policy from handlers.
func ResourceName(name string, rule NameRule) *protocol.AWSError {
	if (rule.MinLength > 0 && len(name) < rule.MinLength) || (rule.MaxLength > 0 && len(name) > rule.MaxLength) {
		return nameError(rule, rule.LengthMessage)
	}
	if rule.Allowed != nil {
		for _, c := range name {
			if !rule.Allowed(c) {
				return nameError(rule, rule.AllowedMessage)
			}
		}
	}
	if rule.Pattern != nil && !rule.Pattern.MatchString(name) {
		return nameError(rule, rule.PatternMessage)
	}
	return nil
}

// AlphaNumericHyphenUnderscorePeriod matches the common AWS identifier alphabet
// used by SQS queue names and DynamoDB table names.
func AlphaNumericHyphenUnderscorePeriod(c rune) bool {
	return isAlphanumeric(c) || c == '-' || c == '_' || c == '.'
}

// BucketName validates an S3 bucket name against AWS naming rules.
// Returns nil if valid, or a *protocol.AWSError with code "InvalidBucketName"
// — the code S3's documented error list gives for a name that breaks the
// rules; protocol.ErrInvalidBucketName carries the citation.
//
// The MESSAGE deliberately names the rule that was broken rather than
// repeating AWS's one-line "The specified bucket is not valid.". Two reasons.
// That sentence is the error list's *Description* of the condition, not the
// bytes S3 puts in <Message>, so adopting it would trade an evidenced code for
// an unevidenced message. And an SDK branches on the code while the message is
// the whole diagnostic a human gets when `cdk deploy` stops on a generated
// name — "must not contain two adjacent periods" is worth more there than a
// sentence that does not say which of seven rules was hit.
func BucketName(name string) *protocol.AWSError {
	return bucketName(name, "")
}

// TableWarehouseBucketSuffix is the suffix S3 reserves for the warehouse
// bucket S3 Tables gives every table. CreateBucket refuses it (BucketName),
// because only S3 Tables may create a bucket that carries it.
const TableWarehouseBucketSuffix = "--table-s3"

// TableWarehouseBucketName validates the name of an S3 Tables warehouse
// bucket: every general purpose naming rule except the one reserving the
// "--table-s3" suffix, which such a bucket must carry. It exists so the one
// caller entitled to that suffix can create the bucket without weakening the
// rule for CreateBucket.
func TableWarehouseBucketName(name string) *protocol.AWSError {
	if !strings.HasSuffix(name, TableWarehouseBucketSuffix) {
		return protocol.ErrInvalidBucketName("The specified bucket name is not valid. A table warehouse bucket name must end with the suffix " + TableWarehouseBucketSuffix + ".")
	}
	return bucketName(name, TableWarehouseBucketSuffix)
}

// bucketName applies the general purpose naming rules, exempting allowedSuffix
// (when non-empty) from the reserved-suffix rule.
func bucketName(name, allowedSuffix string) *protocol.AWSError {
	if len(name) < 3 || len(name) > 63 {
		return protocol.ErrInvalidBucketName("The specified bucket name is not valid. Bucket names must be between 3 and 63 characters.")
	}
	if !validBucketName.MatchString(name) {
		return protocol.ErrInvalidBucketName("The specified bucket name is not valid. Bucket names can consist only of lowercase letters, numbers, periods (.), and hyphens (-), and must begin and end with a letter or number.")
	}
	// AWS forbids two adjacent PERIODS. Adjacent hyphens are legal — its own
	// reserved suffixes (--ol-s3, --x-s3, --table-s3) contain them.
	if strings.Contains(name, "..") {
		return protocol.ErrInvalidBucketName("The specified bucket name is not valid. Bucket names must not contain two adjacent periods.")
	}
	if ipAddress.MatchString(name) {
		return protocol.ErrInvalidBucketName("The specified bucket name is not valid. Bucket names must not be formatted as an IP address.")
	}
	for _, prefix := range reservedBucketPrefixes {
		if strings.HasPrefix(name, prefix) {
			return protocol.ErrInvalidBucketName("The specified bucket name is not valid. Bucket names must not start with the prefix " + prefix + ".")
		}
	}
	for _, suffix := range reservedBucketSuffixes {
		if suffix != allowedSuffix && strings.HasSuffix(name, suffix) {
			return protocol.ErrInvalidBucketName("The specified bucket name is not valid. Bucket names must not end with the suffix " + suffix + ".")
		}
	}
	// Not enforced: "bucket names can only end with the suffix -an when you
	// are creating buckets in your account regional namespace". That rule is
	// conditional on the x-amz-bucket-namespace request header, so it belongs
	// to CreateBucket rather than to a context-free name validator.
	return nil
}

// accountRegionalBucketSuffixMarker is the reserved trailing segment AWS's
// 2026 naming-rules update carved out for account regional namespace bucket
// names: a name may only end this way when it is being created inside that
// namespace. https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html#account-regional-naming-rules
const accountRegionalBucketSuffixMarker = "-an"

// AccountRegionalBucketSuffix returns the suffix an account regional
// namespace bucket name must end with: "-<accountId>-<region>-an". It is the
// single place that knows this grammar: CreateBucket's full-name validation
// (ValidateAccountRegionalBucketName) and CloudFormation's BucketNamePrefix
// property both build on it, so the format cannot drift between the two
// callers.
func AccountRegionalBucketSuffix(accountID, region string) string {
	return "-" + accountID + "-" + region + accountRegionalBucketSuffixMarker
}

// HasAccountRegionalBucketSuffix reports whether name carries the reserved
// "-an" suffix, regardless of which account or region it names. A
// global-namespace CreateBucket uses this cheap check — a single
// strings.HasSuffix, no regex, no allocation — to reject a name AWS reserves
// for the account regional namespace when the caller did not ask for that
// namespace.
func HasAccountRegionalBucketSuffix(name string) bool {
	return strings.HasSuffix(name, accountRegionalBucketSuffixMarker)
}

// ValidateAccountRegionalBucketName validates a full account-regional S3
// bucket name: the base bucket-naming rules (BucketName), plus AWS's
// account-regional naming rule that the name end with exactly
// "-<accountID>-<region>-an". A name with no non-empty prefix ahead of that
// suffix is already rejected by BucketName's "must begin with a letter or
// number" rule, since the suffix itself begins with a hyphen — so no separate
// empty-prefix check is needed here.
//
// The exact error CODE for a name that does carry a suffix but names the
// wrong account or region is unverified against real AWS (see CreateBucket);
// modeled here as the same InvalidBucketName shape the base naming rules use,
// via a single-pass suffix comparison rather than parsing the name apart.
func ValidateAccountRegionalBucketName(name, accountID, region string) *protocol.AWSError {
	if aerr := BucketName(name); aerr != nil {
		return aerr
	}
	suffix := AccountRegionalBucketSuffix(accountID, region)
	if !strings.HasSuffix(name, suffix) {
		return protocol.ErrInvalidBucketName("The specified bucket name is not valid. Account regional namespace bucket names must end with " + suffix + ".")
	}
	return nil
}

// ---- SQS validation --------------------------------------------------------

// QueueName validates an SQS queue name.
// Standard queues: alphanumeric + hyphens + underscores, 1–80 chars.
// FIFO queues: same rules + must end in .fifo.
// https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-queue-message-identifiers.html
func QueueName(name string) *protocol.AWSError {
	return ResourceName(name, NameRule{
		MinLength:      1,
		MaxLength:      80,
		Allowed:        AlphaNumericHyphenUnderscorePeriod,
		ErrorCode:      "InvalidParameterValue",
		LengthMessage:  "Queue name must be between 1 and 80 characters.",
		AllowedMessage: "Queue name can only contain alphanumeric characters, hyphens, underscores, and periods.",
	})
}

// ---- DynamoDB validation ---------------------------------------------------

// TableName validates a DynamoDB table name.
// Rules: 3–255 chars, alphanumeric + hyphens + underscores + periods.
// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/HowItWorks.NamingRulesDataTypes.html
func TableName(name string) *protocol.AWSError {
	return ResourceName(name, NameRule{
		MinLength:      3,
		MaxLength:      255,
		Allowed:        AlphaNumericHyphenUnderscorePeriod,
		ErrorCode:      "ValidationException",
		LengthMessage:  "Table name must be between 3 and 255 characters.",
		AllowedMessage: "Table name can only contain alphanumeric characters, hyphens, underscores, and periods.",
	})
}

// ---- AppSync validation -----------------------------------------------------

// AppSyncGraphQLAPIName validates the required GraphQL API name. AWS documents
// this field as required without publishing a stricter length or pattern.
func AppSyncGraphQLAPIName(name string) *protocol.AWSError {
	if name == "" {
		return &protocol.AWSError{Code: "BadRequestException", Message: "name is required", HTTPStatus: http.StatusBadRequest}
	}
	return nil
}

// AppSyncIdentifierName validates AppSync names documented with the
// [_A-Za-z][_0-9A-Za-z]* pattern and 1..65536 length constraint.
func AppSyncIdentifierName(name, field string) *protocol.AWSError {
	return ResourceName(name, NameRule{
		MinLength:      1,
		MaxLength:      65536,
		Pattern:        GraphQLIdentifierPattern,
		ErrorCode:      "BadRequestException",
		LengthMessage:  field + " must be between 1 and 65536 characters",
		PatternMessage: field + " must match pattern [_A-Za-z][_0-9A-Za-z]*",
	})
}

func AppSyncDataSourceName(name string) *protocol.AWSError {
	return AppSyncIdentifierName(name, "dataSourceName")
}

func AppSyncFunctionName(name string) *protocol.AWSError {
	return AppSyncIdentifierName(name, "name")
}

func AppSyncTypeName(name string) *protocol.AWSError {
	return AppSyncIdentifierName(name, "typeName")
}

func AppSyncFieldName(name string) *protocol.AWSError {
	return AppSyncIdentifierName(name, "fieldName")
}

// ---- Helpers ----------------------------------------------------------------

func nameError(rule NameRule, message string) *protocol.AWSError {
	status := rule.HTTPStatus
	if status == 0 {
		status = http.StatusBadRequest
	}
	return &protocol.AWSError{
		Code:       rule.ErrorCode,
		Message:    message,
		HTTPStatus: status,
	}
}

// ValidateAndRespond is a convenience helper that writes the error and returns
// false if aerr is non-nil, otherwise returns true. Reduces boilerplate in
// handlers that call multiple validators in sequence.
//
//	if !serviceutil.ValidateAndRespond(w, r, serviceutil.BucketName(bucket)) {
//	    return
//	}
func ValidateAndRespond(w http.ResponseWriter, r *http.Request, aerr *protocol.AWSError) bool {
	if aerr != nil {
		// Use the appropriate format based on what content type the client expects.
		// S3 uses XML; most others use JSON. The caller should use the service-
		// appropriate helper directly when the format is known — this helper is for
		// cases where the service format is ambiguous (e.g. shared validators).
		// Default to JSON since it's used by more services.
		protocol.WriteJSONError(w, r, aerr)
		return false
	}
	return true
}

func isAlphanumeric(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// ---- Tag validation ---------------------------------------------------------

// MaxTags is the upper limit for resource tags across most AWS services.
const MaxTags = 50

// TagValidationConfig tunes the error codes a service returns for tag-
// validation violations so that every handler stays faithful to its own
// AWS wire form while sharing the common checks.
type TagValidationConfig struct {
	// ExceededCode is the error code returned when len(tags) > limit.
	ExceededCode string
	// ExceededMessage is the error message for exceeding the limit.
	ExceededMessage string
	// InvalidCode is the error code returned when a tag key or value
	// does not meet the length or character constraints.
	InvalidCode string
	// Limit overrides MaxTags for this service. Zero means use MaxTags.
	Limit int
}

// legalTagRune reports whether r may appear in a tag key or value.
//
// Every AWS service that models tags documents the same pattern for them —
// ^([\p{L}\p{Z}\p{N}_.:/=+\-@]*)$ — so the charset is enforced for every
// service sharing this validator rather than being something each one opts
// into. There is no per-service override because no service's model asks for
// one; the day one does, it earns a field on TagValidationConfig.
//
// It is spelled as a rune test rather than a regexp because tags are validated
// on every tag write and the character class is small enough to read. The
// Unicode categories are matched exactly: unicode.IsSpace would also admit the
// control characters \t and \n, which are category Cc and not \p{Z}.
func legalTagRune(r rune) bool {
	switch r {
	case '_', '.', ':', '/', '=', '+', '-', '@':
		return true
	}
	return unicode.Is(unicode.L, r) || unicode.Is(unicode.Z, r) || unicode.Is(unicode.N, r)
}

// legalTagText reports whether every rune of s may appear in a tag.
func legalTagText(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return !legalTagRune(r) }) < 0
}

// ValidateTags checks standard AWS tag constraints and returns an AWSError
// that carries the service's own error codes via cfg. Each service should
// define its own exported TagValidationConfig var so callers can see them.
//
// Keys are checked in sorted order so a map with several violations always
// reports the same one — Go map iteration order is randomised, and a validator
// whose error changes between identical requests is untestable.
func ValidateTags(cfg TagValidationConfig, tags map[string]string) *protocol.AWSError {
	limit := cfg.Limit
	if limit == 0 {
		limit = MaxTags
	}
	if len(tags) > limit {
		return &protocol.AWSError{
			Code:       cfg.ExceededCode,
			Message:    cfg.ExceededMessage,
			HTTPStatus: http.StatusBadRequest,
		}
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := tags[k]
		if k == "" {
			return &protocol.AWSError{
				Code:       cfg.InvalidCode,
				Message:    "Tag key cannot be empty.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		if len(k) > 128 {
			return &protocol.AWSError{
				Code:       cfg.InvalidCode,
				Message:    "Tag key must be 128 characters or fewer.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		if strings.HasPrefix(k, "aws:") {
			return &protocol.AWSError{
				Code:       cfg.InvalidCode,
				Message:    "Tag keys must not start with aws:.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		if !legalTagText(k) {
			return &protocol.AWSError{
				Code:       cfg.InvalidCode,
				Message:    "Tag key " + strconv.Quote(k) + " contains characters that are not letters, numbers, spaces, or one of _ . : / = + - @.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		if len(v) > 256 {
			return &protocol.AWSError{
				Code:       cfg.InvalidCode,
				Message:    "Tag value must be 256 characters or fewer.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		if !legalTagText(v) {
			return &protocol.AWSError{
				Code:       cfg.InvalidCode,
				Message:    "Tag value for key " + strconv.Quote(k) + " contains characters that are not letters, numbers, spaces, or one of _ . : / = + - @.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}
	return nil
}

// TagPair is the common Key-Value tag element used across AWS tag response
// shapes. Services that store tags as map[string]string internally can use
// TagsToList to produce the wire format without a bespoke loop.
type TagPair struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

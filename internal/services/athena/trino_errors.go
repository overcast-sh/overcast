package athena

import "strings"

// trino_errors.go — a failed Trino query as Athena reports it.
//
// Athena engine version 3 prefixes a failure's StateChangeReason with the
// engine's error name ("TABLE_NOT_FOUND: line 1:15: Table … does not
// exist"), and classifies it into an AthenaError: ErrorCategory 1 (system),
// 2 (user) or 3 (other), and an ErrorType from the Athena error catalog
// (https://docs.aws.amazon.com/athena/latest/ug/error-reference.html).

// AthenaError categories.
const (
	errorCategorySystem int32 = 1
	errorCategoryUser   int32 = 2
)

// Athena error types, from the error catalog, that a Trino failure maps to.
const (
	errorTypeResourcesExhausted int32 = 0    // Query exhausted resources at this scale factor
	errorTypeInternal           int32 = 100  // Internal service error
	errorTypeEngineInternal     int32 = 200  // Query engine had an internal error
	errorTypeMetastore          int32 = 204  // The metastore had an error
	errorTypeTimedOut           int32 = 206  // Query timed out
	errorTypeIceberg            int32 = 233  // Iceberg error
	errorTypeWriteResults       int32 = 401  // Failed to write query results to Amazon S3
	errorTypeUser               int32 = 1000 // User error
	errorTypeData               int32 = 1001 // Data error
	errorTypeDDLFailed          int32 = 1003 // DDL task failed
	errorTypeSyntax             int32 = 1006 // Syntax error
	errorTypeRejected           int32 = 1008 // Query rejected
	errorTypeInvalidArgument    int32 = 1100 // Invalid argument provided
	errorTypeInvalidTableProp   int32 = 1103 // Invalid table property provided
	errorTypeInvalidFunctionArg int32 = 1106 // Invalid function argument provided
	errorTypeNotFound           int32 = 1110 // Provided table or view does not exist
	errorTypeNotSupported       int32 = 1200 // Query not supported
	errorTypeFunctionNotFound   int32 = 1303 // Provided function or function implementation not found
	errorTypeBucketNotFound     int32 = 1306 // Amazon S3 bucket not found
	errorTypePermission         int32 = 1500 // Permission error
)

// trinoErrorTypes maps Trino's error names to Athena error types. A name
// that is not here is classified by its Trino error type alone.
var trinoErrorTypes = map[string]int32{
	"SYNTAX_ERROR": errorTypeSyntax, "COLUMN_NOT_FOUND": errorTypeSyntax, "TYPE_MISMATCH": errorTypeSyntax,
	"AMBIGUOUS_NAME": errorTypeSyntax, "MISSING_ORDER_BY": errorTypeSyntax, "MISSING_GROUP_BY": errorTypeSyntax,
	"EXPRESSION_NOT_AGGREGATE": errorTypeSyntax, "INVALID_LITERAL": errorTypeSyntax, "MISMATCHED_COLUMN_ALIASES": errorTypeSyntax,
	"TABLE_NOT_FOUND": errorTypeNotFound, "SCHEMA_NOT_FOUND": errorTypeNotFound, "CATALOG_NOT_FOUND": errorTypeNotFound,
	"TABLE_ALREADY_EXISTS": errorTypeDDLFailed, "SCHEMA_ALREADY_EXISTS": errorTypeDDLFailed,
	"FUNCTION_NOT_FOUND": errorTypeFunctionNotFound, "INVALID_FUNCTION_ARGUMENT": errorTypeInvalidFunctionArg,
	"INVALID_TABLE_PROPERTY": errorTypeInvalidTableProp, "INVALID_ARGUMENTS": errorTypeInvalidArgument,
	"NOT_SUPPORTED": errorTypeNotSupported, "PERMISSION_DENIED": errorTypePermission,
	"DIVISION_BY_ZERO": errorTypeData, "INVALID_CAST_ARGUMENT": errorTypeData, "NUMERIC_VALUE_OUT_OF_RANGE": errorTypeData,
	"EXCEEDED_TIME_LIMIT": errorTypeTimedOut, "ADMINISTRATIVELY_KILLED": errorTypeTimedOut,
	"HIVE_METASTORE_ERROR": errorTypeMetastore,
}

// athenaErrorFor classifies a failed Trino query.
func athenaErrorFor(e *trinoError) *AthenaError {
	out := &AthenaError{ErrorMessage: e.Error(), ErrorCategory: errorCategorySystem, ErrorType: errorTypeEngineInternal}
	t, mapped := trinoErrorTypes[e.ErrorName]
	if mapped {
		out.ErrorType = t
	} else if strings.HasPrefix(e.ErrorName, "ICEBERG_") {
		out.ErrorType = errorTypeIceberg
	}
	switch e.ErrorType {
	case "USER_ERROR":
		out.ErrorCategory = errorCategoryUser
		if out.ErrorType == errorTypeEngineInternal {
			out.ErrorType = errorTypeUser
		}
	case "INSUFFICIENT_RESOURCES":
		out.Retryable = true
		if !mapped {
			out.ErrorType = errorTypeResourcesExhausted
		}
	}
	return out
}

package athena

import (
	"regexp"
	"strings"
)

// Statement types (StatementType).
const (
	statementDDL     = "DDL"
	statementDML     = "DML"
	statementUtility = "UTILITY"
)

// statementKinds maps a statement's first keyword to its StatementType.
// "DML indicates DML (Data Manipulation Language) query statements, such as
// CREATE TABLE AS SELECT. UTILITY indicates query statements other than DDL
// and DML, such as SHOW CREATE TABLE, or DESCRIBE TABLE."
var statementKinds = map[string]string{
	"SELECT": statementDML, "WITH": statementDML, "VALUES": statementDML, "TABLE": statementDML,
	"INSERT": statementDML, "UPDATE": statementDML, "DELETE": statementDML, "MERGE": statementDML,
	"UNLOAD": statementDML, "OPTIMIZE": statementDML, "VACUUM": statementDML, "EXECUTE": statementDML,
	"CREATE": statementDDL, "DROP": statementDDL, "ALTER": statementDDL, "MSCK": statementDDL,
	"SHOW": statementUtility, "DESCRIBE": statementUtility, "DESC": statementUtility,
	"EXPLAIN": statementUtility, "PREPARE": statementUtility, "DEALLOCATE": statementUtility,
}

var (
	// leadingNoise is whitespace, SQL comments and opening parentheses
	// before a statement's first keyword.
	leadingNoise = regexp.MustCompile(`^(?:\s+|--[^\n]*(?:\n|$)|/\*(?s:.*?)\*/|\()*`)
	// firstWord is a statement's leading keyword.
	firstWord = regexp.MustCompile(`^[A-Za-z]+`)
	// createAsSelect recognises CREATE TABLE ... AS SELECT/WITH, which Athena
	// classes as DML.
	createAsSelect = regexp.MustCompile(`(?is)^CREATE\s+TABLE\b.*\bAS\s*\(?\s*(?:SELECT|WITH)\b`)
)

// statementType classifies a query the way Athena's StatementType does, from
// its first keyword. A statement it cannot place is DML, the class of
// everything a query engine runs.
func statementType(query string) string {
	q := query[len(leadingNoise.FindString(query)):]
	keyword := strings.ToUpper(firstWord.FindString(q))
	kind, ok := statementKinds[keyword]
	if !ok {
		return statementDML
	}
	if kind == statementDDL && createAsSelect.MatchString(q) {
		return statementDML
	}
	return kind
}

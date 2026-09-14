package logs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Empty / matchAll -------------------------------------------------------

func TestCompileFilter_empty(t *testing.T) {
	m, err := CompileFilter("")
	require.NoError(t, err)
	assert.True(t, m("anything"))
	assert.True(t, m(""))
}

func TestCompileFilter_whitespaceOnly(t *testing.T) {
	m, err := CompileFilter("   ")
	require.NoError(t, err)
	assert.True(t, m("anything"))
}

// ---- Text: single term (AND) -----------------------------------------------

func TestTextFilter_singleTerm(t *testing.T) {
	m, err := CompileFilter("ERROR")
	require.NoError(t, err)
	assert.True(t, m("ERROR something broke"))
	assert.True(t, m("an ERROR occurred"))
	assert.False(t, m("INFO all good"))
	assert.False(t, m("error lowercase")) // case-sensitive
}

// ---- Text: multiple terms (AND) --------------------------------------------

func TestTextFilter_multipleTerms(t *testing.T) {
	m, err := CompileFilter("ERROR timeout")
	require.NoError(t, err)
	assert.True(t, m("ERROR connection timeout"))
	assert.False(t, m("ERROR something else"))
	assert.False(t, m("timeout but just a warning"))
}

// ---- Text: quoted phrase ----------------------------------------------------

func TestTextFilter_quotedPhrase(t *testing.T) {
	m, err := CompileFilter(`"connection refused"`)
	require.NoError(t, err)
	assert.True(t, m("error: connection refused by server"))
	assert.False(t, m("connection was refused"))
	assert.False(t, m("refused connection"))
}

func TestTextFilter_quotedPhraseWithOtherTerms(t *testing.T) {
	m, err := CompileFilter(`ERROR "connection refused"`)
	require.NoError(t, err)
	assert.True(t, m("ERROR: connection refused"))
	assert.False(t, m("WARN: connection refused"))
	assert.False(t, m("ERROR: timeout"))
}

// ---- Text: include / OR with ? prefix ---------------------------------------

func TestTextFilter_includeOR(t *testing.T) {
	m, err := CompileFilter("?ERROR ?WARN")
	require.NoError(t, err)
	assert.True(t, m("ERROR something"))
	assert.True(t, m("WARN something"))
	assert.False(t, m("INFO something"))
}

func TestTextFilter_includeORWithRequiredTerms(t *testing.T) {
	m, err := CompileFilter("server ?ERROR ?WARN")
	require.NoError(t, err)
	assert.True(t, m("server ERROR down"))
	assert.True(t, m("server WARN degraded"))
	assert.False(t, m("server INFO ok"))    // neither OR term
	assert.False(t, m("client ERROR down")) // missing required "server"
}

func TestTextFilter_includeORQuotedPhrase(t *testing.T) {
	m, err := CompileFilter(`?"connection refused" ?"connection reset"`)
	require.NoError(t, err)
	assert.True(t, m("error: connection refused"))
	assert.True(t, m("error: connection reset by peer"))
	assert.False(t, m("error: connection timeout"))
}

// ---- Text: unterminated quote -----------------------------------------------

func TestTextFilter_unterminatedQuote(t *testing.T) {
	m, err := CompileFilter(`"no closing quote`)
	require.NoError(t, err)
	assert.True(t, m("no closing quote here"))
	assert.False(t, m("something else"))
}

// ---- JSON: field equality ---------------------------------------------------

func TestJSONFilter_stringEquals(t *testing.T) {
	m, err := CompileFilter(`{ $.level = "ERROR" }`)
	require.NoError(t, err)
	assert.True(t, m(`{"level":"ERROR","msg":"oops"}`))
	assert.False(t, m(`{"level":"INFO","msg":"ok"}`))
	assert.False(t, m("not json at all"))
}

func TestJSONFilter_stringNotEquals(t *testing.T) {
	m, err := CompileFilter(`{ $.level != "INFO" }`)
	require.NoError(t, err)
	assert.True(t, m(`{"level":"ERROR"}`))
	assert.True(t, m(`{"level":"WARN"}`))
	assert.False(t, m(`{"level":"INFO"}`))
}

// ---- JSON: numeric comparison -----------------------------------------------

func TestJSONFilter_numericEquals(t *testing.T) {
	m, err := CompileFilter(`{ $.statusCode = 200 }`)
	require.NoError(t, err)
	assert.True(t, m(`{"statusCode":200}`))
	assert.False(t, m(`{"statusCode":404}`))
}

func TestJSONFilter_numericGreaterThan(t *testing.T) {
	m, err := CompileFilter(`{ $.latency > 1000 }`)
	require.NoError(t, err)
	assert.True(t, m(`{"latency":1500}`))
	assert.False(t, m(`{"latency":1000}`))
	assert.False(t, m(`{"latency":500}`))
}

func TestJSONFilter_numericGreaterOrEqual(t *testing.T) {
	m, err := CompileFilter(`{ $.statusCode >= 400 }`)
	require.NoError(t, err)
	assert.True(t, m(`{"statusCode":500}`))
	assert.True(t, m(`{"statusCode":400}`))
	assert.False(t, m(`{"statusCode":200}`))
}

func TestJSONFilter_numericLessThan(t *testing.T) {
	m, err := CompileFilter(`{ $.code < 300 }`)
	require.NoError(t, err)
	assert.True(t, m(`{"code":200}`))
	assert.False(t, m(`{"code":300}`))
	assert.False(t, m(`{"code":500}`))
}

func TestJSONFilter_numericLessOrEqual(t *testing.T) {
	m, err := CompileFilter(`{ $.code <= 299 }`)
	require.NoError(t, err)
	assert.True(t, m(`{"code":200}`))
	assert.True(t, m(`{"code":299}`))
	assert.False(t, m(`{"code":300}`))
}

// ---- JSON: boolean values ---------------------------------------------------

func TestJSONFilter_booleanEquals(t *testing.T) {
	m, err := CompileFilter(`{ $.active = true }`)
	require.NoError(t, err)
	assert.True(t, m(`{"active":true}`))
	assert.False(t, m(`{"active":false}`))
}

// ---- JSON: AND / OR combinators ---------------------------------------------

func TestJSONFilter_and(t *testing.T) {
	m, err := CompileFilter(`{ $.level = "ERROR" && $.code >= 500 }`)
	require.NoError(t, err)
	assert.True(t, m(`{"level":"ERROR","code":503}`))
	assert.False(t, m(`{"level":"ERROR","code":400}`))
	assert.False(t, m(`{"level":"INFO","code":503}`))
}

func TestJSONFilter_or(t *testing.T) {
	m, err := CompileFilter(`{ $.level = "ERROR" || $.level = "WARN" }`)
	require.NoError(t, err)
	assert.True(t, m(`{"level":"ERROR"}`))
	assert.True(t, m(`{"level":"WARN"}`))
	assert.False(t, m(`{"level":"INFO"}`))
}

func TestJSONFilter_andOrPrecedence(t *testing.T) {
	// && binds tighter than ||:
	// $.a = 1 || $.b = 2 && $.c = 3  →  $.a = 1 || ($.b = 2 && $.c = 3)
	m, err := CompileFilter(`{ $.a = 1 || $.b = 2 && $.c = 3 }`)
	require.NoError(t, err)
	assert.True(t, m(`{"a":1}`))        // left side of OR
	assert.True(t, m(`{"b":2,"c":3}`))  // right side (AND)
	assert.False(t, m(`{"b":2,"c":4}`)) // AND fails
	assert.False(t, m(`{"a":2,"b":1}`)) // neither
}

// ---- JSON: EXISTS / NOT EXISTS ----------------------------------------------

func TestJSONFilter_exists(t *testing.T) {
	m, err := CompileFilter(`{ $.error EXISTS }`)
	require.NoError(t, err)
	assert.True(t, m(`{"error":"something"}`))
	assert.True(t, m(`{"error":null}`)) // field exists even if null
	assert.False(t, m(`{"msg":"ok"}`))
}

func TestJSONFilter_notExists(t *testing.T) {
	m, err := CompileFilter(`{ $.error NOT EXISTS }`)
	require.NoError(t, err)
	assert.False(t, m(`{"error":"something"}`))
	assert.True(t, m(`{"msg":"ok"}`))
}

// ---- JSON: IS NULL / IS NOT NULL --------------------------------------------

func TestJSONFilter_isNull(t *testing.T) {
	m, err := CompileFilter(`{ $.error IS NULL }`)
	require.NoError(t, err)
	assert.True(t, m(`{"error":null}`))
	assert.True(t, m(`{"msg":"ok"}`)) // missing field → IS NULL true
	assert.False(t, m(`{"error":"value"}`))
}

func TestJSONFilter_isNotNull(t *testing.T) {
	m, err := CompileFilter(`{ $.error IS NOT NULL }`)
	require.NoError(t, err)
	assert.True(t, m(`{"error":"value"}`))
	assert.False(t, m(`{"error":null}`))
	assert.False(t, m(`{"msg":"ok"}`)) // missing field → IS NOT NULL false
}

// ---- JSON: nested fields ----------------------------------------------------

func TestJSONFilter_nestedField(t *testing.T) {
	m, err := CompileFilter(`{ $.request.method = "GET" }`)
	require.NoError(t, err)
	assert.True(t, m(`{"request":{"method":"GET"}}`))
	assert.False(t, m(`{"request":{"method":"POST"}}`))
	assert.False(t, m(`{"request":"flat"}`))
	assert.False(t, m(`{"other":"field"}`))
}

func TestJSONFilter_deeplyNested(t *testing.T) {
	m, err := CompileFilter(`{ $.a.b.c = "deep" }`)
	require.NoError(t, err)
	assert.True(t, m(`{"a":{"b":{"c":"deep"}}}`))
	assert.False(t, m(`{"a":{"b":{"c":"shallow"}}}`))
}

// ---- JSON: non-JSON messages ------------------------------------------------

func TestJSONFilter_nonJSONMessage(t *testing.T) {
	m, err := CompileFilter(`{ $.level = "ERROR" }`)
	require.NoError(t, err)
	assert.False(t, m("plain text log line"))
	assert.False(t, m("[2026-04-17] ERROR something"))
	assert.False(t, m(""))
}

// ---- JSON: missing field in comparison --------------------------------------

func TestJSONFilter_missingFieldComparison(t *testing.T) {
	m, err := CompileFilter(`{ $.missing = "val" }`)
	require.NoError(t, err)
	assert.False(t, m(`{"other":"val"}`))
}

// ---- JSON: complex combined expression --------------------------------------

func TestJSONFilter_complexExpression(t *testing.T) {
	m, err := CompileFilter(`{ $.level = "ERROR" && $.code >= 500 || $.critical = true }`)
	require.NoError(t, err)
	assert.True(t, m(`{"level":"ERROR","code":503}`))
	assert.True(t, m(`{"critical":true}`))
	assert.False(t, m(`{"level":"ERROR","code":400}`))
	assert.False(t, m(`{"level":"INFO","code":200}`))
}

// ---- JSON: empty braces -----------------------------------------------------

func TestJSONFilter_emptyBraces(t *testing.T) {
	m, err := CompileFilter(`{ }`)
	require.NoError(t, err)
	assert.True(t, m("anything"))
}

// ---- Compile errors ---------------------------------------------------------

func TestCompileFilter_invalidJSON_missingPath(t *testing.T) {
	_, err := CompileFilter(`{ level = "ERROR" }`)
	assert.Error(t, err)
}

func TestCompileFilter_invalidJSON_missingOperator(t *testing.T) {
	_, err := CompileFilter(`{ $.level "ERROR" }`)
	assert.Error(t, err)
}

func TestCompileFilter_invalidJSON_unterminatedString(t *testing.T) {
	_, err := CompileFilter(`{ $.level = "ERROR }`)
	assert.Error(t, err)
}

func TestCompileFilter_invalidJSON_trailingText(t *testing.T) {
	_, err := CompileFilter(`{ $.level = "ERROR" garbage }`)
	assert.Error(t, err)
}

func TestCompileFilter_invalidJSON_badValue(t *testing.T) {
	_, err := CompileFilter(`{ $.level = NOTAVALUE }`)
	assert.Error(t, err)
}

// ---- tokenizeText -----------------------------------------------------------

func TestTokenizeText_basic(t *testing.T) {
	assert.Equal(t, []string{"ERROR"}, tokenizeText("ERROR"))
	assert.Equal(t, []string{"ERROR", "timeout"}, tokenizeText("ERROR timeout"))
	assert.Equal(t, []string{"connection refused"}, tokenizeText(`"connection refused"`))
	assert.Equal(t, []string{"?ERROR", "?WARN"}, tokenizeText("?ERROR ?WARN"))
	assert.Equal(t, []string{"?exact phrase"}, tokenizeText(`?"exact phrase"`))
}

// ---- Space-delimited (columnar) patterns ------------------------------------

func TestColumnarFilter_allWildcard(t *testing.T) {
	// [w1, w2, w3] with no constraints — matches any line with 3+ fields
	m, err := CompileFilter("[w1, w2, w3]")
	require.NoError(t, err)
	assert.True(t, m("a b c"))
	assert.True(t, m("a b c d"))
	assert.False(t, m("a b")) // too few columns
}

func TestColumnarFilter_stringEquality(t *testing.T) {
	m, err := CompileFilter(`[ip, user, username, timestamp, request, status_code = 404, bytes]`)
	require.NoError(t, err)
	assert.True(t, m("1.2.3.4 frank admin 2026-04-17 /page 404 1234"))
	assert.False(t, m("1.2.3.4 frank admin 2026-04-17 /page 200 1234"))
}

func TestColumnarFilter_quotedStringEquality(t *testing.T) {
	m, err := CompileFilter(`[ip, user = "frank", action]`)
	require.NoError(t, err)
	assert.True(t, m("1.2.3.4 frank login"))
	assert.False(t, m("1.2.3.4 bob login"))
}

func TestColumnarFilter_wildcardPatterns(t *testing.T) {
	m, err := CompileFilter("[ip, user, username, timestamp, request, status_code = 4*, bytes]")
	require.NoError(t, err)
	assert.True(t, m("1.2.3.4 frank admin 2026-04-17 /page 404 5000"))
	assert.True(t, m("1.2.3.4 frank admin 2026-04-17 /page 403 5000"))
	assert.False(t, m("1.2.3.4 frank admin 2026-04-17 /page 200 5000"))
}

func TestColumnarFilter_wildcardInMiddle(t *testing.T) {
	m, err := CompileFilter("[ip, user, request = *.html*]")
	require.NoError(t, err)
	assert.True(t, m("1.2.3.4 frank /index.html"))
	assert.True(t, m("1.2.3.4 frank /index.html?q=1"))
	assert.False(t, m("1.2.3.4 frank /api/v1"))
}

func TestColumnarFilter_numericOperators(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		// > (greater than)
		{"gt match", "[name, bytes > 1000]", "file 5000", true},
		{"gt boundary miss", "[name, bytes > 1000]", "file 1000", false},
		{"gt below", "[name, bytes > 1000]", "file 500", false},
		// != (not equals)
		{"ne match 404", "[name, status != 200]", "req 404", true},
		{"ne match 500", "[name, status != 200]", "req 500", true},
		{"ne miss", "[name, status != 200]", "req 200", false},
		// <= (less or equal)
		{"le below", "[name, val <= 100]", "x 50", true},
		{"le boundary", "[name, val <= 100]", "x 100", true},
		{"le above", "[name, val <= 100]", "x 101", false},
		// >= (greater or equal)
		{"ge boundary", "[name, code >= 400]", "req 400", true},
		{"ge above", "[name, code >= 400]", "req 500", true},
		{"ge below", "[name, code >= 400]", "req 399", false},
		// < (less than)
		{"lt below", "[name, code < 400]", "req 200", true},
		{"lt just below", "[name, code < 400]", "req 399", true},
		{"lt boundary miss", "[name, code < 400]", "req 400", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := CompileFilter(tt.pattern)
			require.NoError(t, err)
			assert.Equal(t, tt.want, m(tt.input))
		})
	}
}

func TestColumnarFilter_ellipsis(t *testing.T) {
	m, err := CompileFilter("[..., status_code = 4*, bytes]")
	require.NoError(t, err)
	assert.True(t, m("1.2.3.4 frank admin 2026-04-17 /page 403 5000"))
	assert.True(t, m("x y 404 1234"))
	assert.False(t, m("x y 200 1234"))
}

func TestColumnarFilter_ellipsisMatchesVariableColumns(t *testing.T) {
	m, err := CompileFilter("[..., request = *.html*, status_code = 4*, bytes]")
	require.NoError(t, err)
	assert.True(t, m("1.2.3.4 frank admin /index.html 404 5000"))
	assert.True(t, m("a /page.html 403 9999"))
	assert.False(t, m("1.2.3.4 frank admin /api 404 5000"))
	assert.False(t, m("1.2.3.4 frank admin /index.html 200 5000"))
}

func TestColumnarFilter_emptyColumnsSkip(t *testing.T) {
	m, err := CompileFilter("[,, request = /health, status]")
	require.NoError(t, err)
	assert.True(t, m("1.2.3.4 frank /health 200"))
	assert.False(t, m("1.2.3.4 frank /api 200"))
}

func TestColumnarFilter_tooFewFields(t *testing.T) {
	m, err := CompileFilter("[a, b, c, d, e]")
	require.NoError(t, err)
	assert.False(t, m("only three fields"))
	assert.True(t, m("a b c d e"))
	assert.True(t, m("a b c d e f")) // extra fields ok
}

func TestColumnarFilter_emptyBrackets(t *testing.T) {
	m, err := CompileFilter("[]")
	require.NoError(t, err)
	assert.True(t, m("anything"))
	assert.True(t, m(""))
}

func TestColumnarFilter_invalidColumnDef(t *testing.T) {
	_, err := CompileFilter("[a, b = ]")
	assert.Error(t, err)
}

// ---- Columnar: regex (%pattern%) -------------------------------------------

func TestColumnarFilter_regexBasic(t *testing.T) {
	// ip=%127\.0\.0\.[1-9]% matches 127.0.0.1 through 127.0.0.9
	m, err := CompileFilter(`[ip=%127\.0\.0\.[1-9]%, user]`)
	require.NoError(t, err)
	assert.True(t, m("127.0.0.1 frank"))
	assert.True(t, m("127.0.0.9 frank"))
	assert.False(t, m("192.168.1.1 frank"))
}

func TestColumnarFilter_regexIPRange(t *testing.T) {
	// Full example from the AWS docs
	m, err := CompileFilter(`[ip=%127\.0\.0\.[1-9]%, user, username, timestamp, request =*.html*, status_code = 4*, bytes]`)
	require.NoError(t, err)
	assert.True(t, m(`127.0.0.3 Prod frank [10/Oct/2000:13:25:15] "GET /index.html HTTP/1.0" 404 1534`))
	assert.False(t, m(`192.168.0.1 Prod frank [10/Oct/2000:13:25:15] "GET /index.html HTTP/1.0" 404 1534`))
}

func TestColumnarFilter_regexResourceURI(t *testing.T) {
	// Match top-level resource URI with regex
	m, err := CompileFilter(`[logLevel, date, time, method, url=%/service/resource/[0-9]+$%, response_time]`)
	require.NoError(t, err)
	assert.True(t, m("INFO 09/25/2014 12:00:00 GET /service/resource/67 1200"))
	assert.False(t, m("INFO 09/25/2014 12:00:01 POST /service/resource/67/part/111 1310"))
}

func TestColumnarFilter_regexChildResourceURI(t *testing.T) {
	// Match child-level resource URI with regex
	m, err := CompileFilter(`[logLevel, date, time, method, url=%/service/resource/[0-9]+/part/[0-9]+$%, response_time]`)
	require.NoError(t, err)
	assert.False(t, m("INFO 09/25/2014 12:00:00 GET /service/resource/67 1200"))
	assert.True(t, m("INFO 09/25/2014 12:00:01 POST /service/resource/67/part/111 1310"))
}

func TestColumnarFilter_regexNotEquals(t *testing.T) {
	m, err := CompileFilter(`[ip !=%127\.0\.0\.[1-9]%, user]`)
	require.NoError(t, err)
	assert.False(t, m("127.0.0.1 frank"))
	assert.True(t, m("192.168.1.1 frank"))
}

func TestColumnarFilter_regexInvalid(t *testing.T) {
	_, err := CompileFilter(`[ip=%[invalid%, user]`)
	assert.Error(t, err)
}

func TestColumnarFilter_regexContainingOperator(t *testing.T) {
	// Regex contains ">=" which should NOT be treated as the operator.
	// The real operator is "=" between "val" and "%...%".
	m, err := CompileFilter(`[val=%[0-9]>=5%, other]`)
	require.NoError(t, err)
	assert.True(t, m("7>=5 ok"))
	assert.False(t, m("hello ok"))
}

// ---- Columnar: compound expressions (|| and &&) ----------------------------

func TestColumnarFilter_compoundOr(t *testing.T) {
	// status_code = 404 || status_code = 410
	m, err := CompileFilter(`[ip, user, username, timestamp, request =*.html*, status_code = 404 || status_code = 410, bytes]`)
	require.NoError(t, err)
	assert.True(t, m(`127.0.0.1 - frank [10/Oct/2000:13:25:15] "GET /index.html HTTP/1.0" 404 1534`))
	assert.True(t, m(`127.0.0.1 - frank [10/Oct/2000:13:25:15] "GET /index.html HTTP/1.0" 410 1534`))
	assert.False(t, m(`127.0.0.1 - frank [10/Oct/2000:13:25:15] "GET /index.html HTTP/1.0" 200 1534`))
}

func TestColumnarFilter_compoundAnd(t *testing.T) {
	// w1!=ERROR && w1!=WARNING
	m, err := CompileFilter("[w1!=ERROR && w1!=WARNING, w2]")
	require.NoError(t, err)
	assert.True(t, m("INFO details"))
	assert.False(t, m("ERROR details"))
	assert.False(t, m("WARNING details"))
}

func TestColumnarFilter_compoundOrSimple(t *testing.T) {
	// w1=ERROR || w1=WARNING
	m, err := CompileFilter("[w1=ERROR || w1=WARNING, w2]")
	require.NoError(t, err)
	assert.True(t, m("ERROR details"))
	assert.True(t, m("WARNING details"))
	assert.False(t, m("INFO details"))
}

// ---- Columnar: bracket/quote-aware field splitting -------------------------

func TestColumnarFilter_bracketedFieldIsSingleToken(t *testing.T) {
	// [10/Oct/2000:13:25:15 -0700] should be one field
	m, err := CompileFilter("[ip, user, username, timestamp, request, status_code, bytes]")
	require.NoError(t, err)
	msg := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`
	assert.True(t, m(msg))
}

func TestColumnarFilter_quotedFieldIsSingleToken(t *testing.T) {
	// "GET /index.html HTTP/1.0" should be one field
	m, err := CompileFilter("[ip, user, username, timestamp, request = *.html*, status_code = 4*, bytes]")
	require.NoError(t, err)
	msg := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`
	assert.True(t, m(msg))
}

func TestSplitLogFields_awsExample(t *testing.T) {
	msg := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`
	fields := splitLogFields(msg)
	assert.Equal(t, []string{
		"127.0.0.1",
		"Prod",
		"frank",
		"[10/Oct/2000:13:25:15 -0700]",
		`"GET /index.html HTTP/1.0"`,
		"404",
		"1534",
	}, fields)
}

func TestSplitLogFields_noSpecialChars(t *testing.T) {
	fields := splitLogFields("a b c")
	assert.Equal(t, []string{"a", "b", "c"}, fields)
}

func TestSplitLogFields_empty(t *testing.T) {
	fields := splitLogFields("")
	assert.Empty(t, fields)
}

// ---- Columnar: the full AWS doc example ------------------------------------

func TestColumnarFilter_awsDocFullExample(t *testing.T) {
	// The exact example from the AWS CloudWatch Logs documentation.
	m, err := CompileFilter(`[ip=%127\.0\.0\.[1-9]%, user, username, timestamp, request =*.html*, status_code = 4*, bytes]`)
	require.NoError(t, err)

	msg := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`
	assert.True(t, m(msg))

	// Wrong IP range
	msg2 := `192.168.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`
	assert.False(t, m(msg2))

	// request doesn't contain .html
	msg3 := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /api HTTP/1.0" 404 1534`
	assert.False(t, m(msg3))

	// status_code is 200, not 4*
	msg4 := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 200 1534`
	assert.False(t, m(msg4))
}

func TestColumnarFilter_awsDocEllipsisExample(t *testing.T) {
	// [..., request =*.html*, status_code = 4*, bytes]
	m, err := CompileFilter("[..., request =*.html*, status_code = 4*, bytes]")
	require.NoError(t, err)

	msg := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`
	assert.True(t, m(msg))
}

func TestColumnarFilter_awsDocCompoundOrExample(t *testing.T) {
	// [ip, user, username, timestamp, request =*.html*, status_code = 404 || status_code = 410, bytes]
	m, err := CompileFilter(`[ip, user, username, timestamp, request =*.html*, status_code = 404 || status_code = 410, bytes]`)
	require.NoError(t, err)

	msg404 := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`
	msg410 := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 410 1534`
	msg200 := `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 200 1534`
	assert.True(t, m(msg404))
	assert.True(t, m(msg410))
	assert.False(t, m(msg200))
}

func TestColumnarFilter_awsDocPatternMatching(t *testing.T) {
	// [w1=ERROR, w2] — first word is ERROR
	m, err := CompileFilter("[w1=ERROR, w2]")
	require.NoError(t, err)

	assert.True(t, m("ERROR 09/25/2014"))
	assert.False(t, m("INFO 09/25/2014"))
	assert.False(t, m("WARNING 09/25/2014"))
}

// ---- Field extraction (metric filters) --------------------------------------
//
// compileFilterPattern is CompileFilter's extraction-aware form: one evaluation
// both matches a message and exposes the fields a metric filter's metricValue
// and dimensions name — `$name` for a space-delimited column, `$.path` for a
// JSON property. The reported field sets are AWS's own, from the
// TestMetricFilter reference examples:
// https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html

func TestCompileFilterPattern_textPatternExtractsNothing(t *testing.T) {
	f, err := compileFilterPattern(`"[ERROR]"`)
	require.NoError(t, err)
	fields, ok := f.eval("02 May 2014 00:34:16,142 [ERROR] Terminating the application")
	require.True(t, ok)
	assert.Nil(t, fields)
	_, ok = f.eval("02 May 2014 00:34:12,525 [INFO] Starting the application")
	assert.False(t, ok)
}

func TestCompileFilterPattern_columnarNamedColumns(t *testing.T) {
	f, err := compileFilterPattern("[ip, identity, user_id, timestamp, request, status_code, size]")
	require.NoError(t, err)
	fields, ok := f.eval(`127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /apache_pb.gif HTTP/1.0" 200 1534`)
	require.True(t, ok)
	require.NotNil(t, fields)
	assert.Equal(t, map[string]string{
		"$ip":          "127.0.0.1",
		"$identity":    "-",
		"$user_id":     "frank",
		"$timestamp":   "10/Oct/2000:13:25:15 -0700",
		"$request":     "GET /apache_pb.gif HTTP/1.0",
		"$status_code": "200",
		"$size":        "1534",
	}, fields.named())
	v, found := fields.lookup("$size")
	assert.True(t, found)
	assert.Equal(t, "1534", v)
	_, found = fields.lookup("$missing")
	assert.False(t, found)
}

func TestCompileFilterPattern_columnarEllipsisNamesUnnamedByPosition(t *testing.T) {
	// AWS's second TestMetricFilter example: [..., size] reports $size for the
	// last field and $1..$6 for the fields the ellipsis absorbed.
	f, err := compileFilterPattern("[..., size]")
	require.NoError(t, err)
	fields, ok := f.eval(`127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /apache_pb.gif HTTP/1.0" 200 1534`)
	require.True(t, ok)
	assert.Equal(t, map[string]string{
		"$1": "127.0.0.1", "$2": "-", "$3": "frank", "$4": "10/Oct/2000:13:25:15 -0700",
		"$5": "GET /apache_pb.gif HTTP/1.0", "$6": "200", "$size": "1534",
	}, fields.named())
}

func TestCompileFilterPattern_columnarEmptyPatternNamesEveryFieldByPosition(t *testing.T) {
	// AWS's third example: [] matches everything and reports $1..$N.
	f, err := compileFilterPattern("[]")
	require.NoError(t, err)
	fields, ok := f.eval(`127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /apache_pb.gif HTTP/1.0" 200 1534`)
	require.True(t, ok)
	assert.Equal(t, map[string]string{
		"$1": "127.0.0.1", "$2": "-", "$3": "frank", "$4": "10/Oct/2000:13:25:15 -0700",
		"$5": "GET /apache_pb.gif HTTP/1.0", "$6": "200", "$7": "1534",
	}, fields.named())
}

func TestCompileFilterPattern_columnarConstrainedAndTrailingUnnamedColumn(t *testing.T) {
	// AWS's fifth example: a constrained column keeps its name and a trailing
	// unnamed column is reported by position.
	f, err := compileFilterPattern("[..., request=*.html*, status_code=4*,]")
	require.NoError(t, err)
	fields, ok := f.eval(`127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`)
	require.True(t, ok)
	assert.Equal(t, map[string]string{
		"$1": "127.0.0.1", "$2": "-", "$3": "frank", "$4": "10/Oct/2000:13:25:15 -0700",
		"$request": "GET /index.html HTTP/1.0", "$status_code": "404", "$7": "1534",
	}, fields.named())
	_, ok = f.eval(`127.0.0.1 - frank [10/Oct/2000:13:35:22 -0700] "GET /about-us/index.html HTTP/1.0" 200 5324`)
	assert.False(t, ok)
}

func TestCompileFilterPattern_columnarCompoundColumnKeepsName(t *testing.T) {
	f, err := compileFilterPattern("[level = ERROR || level = WARN, rest]")
	require.NoError(t, err)
	fields, ok := f.eval("WARN disk almost full")
	require.True(t, ok)
	v, found := fields.lookup("$level")
	assert.True(t, found)
	assert.Equal(t, "WARN", v)
}

func TestCompileFilterPattern_jsonLookupResolvesAnyPath(t *testing.T) {
	// metricValue and dimensions may name properties the pattern never
	// mentions, so the lookup walks the parsed document, not the selectors.
	f, err := compileFilterPattern(`{ $.level = "ERROR" }`)
	require.NoError(t, err)
	fields, ok := f.eval(`{"level":"ERROR","latency":12.5,"http":{"status":500},"ok":false,"nothing":null,"tags":["a"]}`)
	require.True(t, ok)
	require.NotNil(t, fields)
	for ref, want := range map[string]string{
		"$.level":       "ERROR",
		"$.latency":     "12.5",
		"$.http.status": "500",
		"$.ok":          "false",
	} {
		v, found := fields.lookup(ref)
		assert.True(t, found, ref)
		assert.Equal(t, want, v, ref)
	}
	for _, ref := range []string{"$.missing", "$.nothing", "$.tags", "$.http", "$level", "$.", ""} {
		_, found := fields.lookup(ref)
		assert.False(t, found, ref)
	}
	// TestMetricFilter reports the selectors the pattern itself names.
	assert.Equal(t, map[string]string{"$.level": "ERROR"}, fields.named())
}

func TestCompileFilterPattern_jsonNamedOmitsUnresolvedSelectors(t *testing.T) {
	f, err := compileFilterPattern(`{ $.a = 1 || $.b NOT EXISTS }`)
	require.NoError(t, err)
	fields, ok := f.eval(`{"a":1}`)
	require.True(t, ok)
	assert.Equal(t, map[string]string{"$.a": "1"}, fields.named())
}

func TestCompileFilterPattern_jsonNonMatchExtractsNothing(t *testing.T) {
	f, err := compileFilterPattern(`{ $.level = "ERROR" }`)
	require.NoError(t, err)
	_, ok := f.eval(`{"level":"INFO"}`)
	assert.False(t, ok)
	_, ok = f.eval(`not json`)
	assert.False(t, ok)
}

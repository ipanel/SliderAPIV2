package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	modernsqlite "modernc.org/sqlite"
)

const sqliteCompatDriverName = "sqlite-ikik-compat"

var (
	insertIgnoreRE    = regexp.MustCompile(`(?i)\bINSERT\s+IGNORE\b`)
	truncateTableRE   = regexp.MustCompile(`(?i)\bTRUNCATE\s+TABLE\b`)
	nullSafeEqualRE   = regexp.MustCompile(`<=>`)
	updateAliasRE     = regexp.MustCompile("(?i)\\bUPDATE\\s+((?:`(?:``|[^`])+`|[A-Za-z_][A-Za-z0-9_$]*)(?:\\s*\\.\\s*(?:`(?:``|[^`])+`|[A-Za-z_][A-Za-z0-9_$]*))?)\\s+((?:`(?:``|[^`])+`|[A-Za-z_][A-Za-z0-9_$]*))\\s+SET\\b")
	forUpdateRE       = regexp.MustCompile(`(?i)\s+FOR\s+UPDATE\b`)
	signedCastRE      = regexp.MustCompile(`(?i)\bAS\s+SIGNED\b`)
	sessionTimezoneRE = regexp.MustCompile(`(?i)@@session\.time_zone\b`)
	duplicateKeyRE    = regexp.MustCompile(`(?i)\bON\s+DUPLICATE\s+KEY\s+UPDATE\b`)
	insertedValueRE   = regexp.MustCompile("(?i)\\bVALUES\\s*\\(\\s*(`[^`]+`|[A-Za-z_][A-Za-z0-9_$]*)\\s*\\)")
	percentileRE      = regexp.MustCompile(`(?i)\bPERCENTILE_CONT\b`)
	castRE            = regexp.MustCompile(`(?i)\bCAST\b`)
	intervalRE        = regexp.MustCompile(`(?i)\bINTERVAL\b`)
	isNullFunctionRE  = regexp.MustCompile(`(?i)\bISNULL\b`)
	dateColumnParamRE = regexp.MustCompile(`(?i)((?:[A-Za-z_][A-Za-z0-9_$]*\s*\.\s*)?[A-Za-z_][A-Za-z0-9_$]*_date)\s*(=|<>|!=|<=|>=|<|>)\s*\?`)
)

func init() {
	registerSQLiteCompatFunctions()
	sql.Register(sqliteCompatDriverName, sqliteCompatDriver{})
}

// sqliteCompatDriver opens the already registered modernc.org/sqlite driver
// through database/sql, but rewrites every statement before SQLite sees it.
type sqliteCompatDriver struct{}

func (sqliteCompatDriver) Open(name string) (driver.Conn, error) {
	db, err := sql.Open("sqlite", name)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &sqliteCompatConn{db: db, conn: conn}, nil
}

type sqliteCompatConn struct {
	db   *sql.DB
	conn *sql.Conn
}

func (c *sqliteCompatConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *sqliteCompatConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.conn.PrepareContext(ctx, rewriteSQLiteQuery(query))
	if err != nil {
		return nil, err
	}
	return &sqliteCompatStmt{stmt: stmt}, nil
}

func (c *sqliteCompatConn) Close() error {
	return errors.Join(c.conn.Close(), c.db.Close())
}

func (c *sqliteCompatConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *sqliteCompatConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.IsolationLevel(opts.Isolation), ReadOnly: opts.ReadOnly})
	if err != nil {
		return nil, err
	}
	return &sqliteCompatTx{tx: tx}, nil
}

func (c *sqliteCompatConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.conn.ExecContext(ctx, rewriteSQLiteQuery(query), namedValuesToArgs(args)...)
}

func (c *sqliteCompatConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.conn.QueryContext(ctx, rewriteSQLiteQuery(query), namedValuesToArgs(args)...)
	if err != nil {
		return nil, err
	}
	return newSQLiteCompatRows(rows)
}

func (c *sqliteCompatConn) Ping(ctx context.Context) error { return c.conn.PingContext(ctx) }
func (c *sqliteCompatConn) ResetSession(ctx context.Context) error {
	return c.conn.PingContext(ctx)
}
func (c *sqliteCompatConn) IsValid() bool { return c.conn != nil }

type sqliteCompatStmt struct{ stmt *sql.Stmt }

func (s *sqliteCompatStmt) Close() error  { return s.stmt.Close() }
func (s *sqliteCompatStmt) NumInput() int { return -1 }
func (s *sqliteCompatStmt) Exec(args []driver.Value) (driver.Result, error) {
	values := make([]any, len(args))
	for i := range args {
		values[i] = normalizeSQLiteBindValue(args[i])
	}
	return s.stmt.Exec(values...)
}
func (s *sqliteCompatStmt) Query(args []driver.Value) (driver.Rows, error) {
	values := make([]any, len(args))
	for i := range args {
		values[i] = normalizeSQLiteBindValue(args[i])
	}
	rows, err := s.stmt.Query(values...)
	if err != nil {
		return nil, err
	}
	return newSQLiteCompatRows(rows)
}
func (s *sqliteCompatStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.stmt.ExecContext(ctx, namedValuesToArgs(args)...)
}
func (s *sqliteCompatStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := s.stmt.QueryContext(ctx, namedValuesToArgs(args)...)
	if err != nil {
		return nil, err
	}
	return newSQLiteCompatRows(rows)
}

type sqliteCompatTx struct{ tx *sql.Tx }

func (t *sqliteCompatTx) Commit() error   { return t.tx.Commit() }
func (t *sqliteCompatTx) Rollback() error { return t.tx.Rollback() }

type sqliteCompatRows struct {
	rows    *sql.Rows
	columns []string
}

func newSQLiteCompatRows(rows *sql.Rows) (*sqliteCompatRows, error) {
	columns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return nil, err
	}
	return &sqliteCompatRows{rows: rows, columns: columns}, nil
}
func (r *sqliteCompatRows) Columns() []string { return r.columns }
func (r *sqliteCompatRows) Close() error      { return r.rows.Close() }
func (r *sqliteCompatRows) Next(dest []driver.Value) error {
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	values := make([]any, len(dest))
	pointers := make([]any, len(dest))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := r.rows.Scan(pointers...); err != nil {
		return err
	}
	for i := range values {
		dest[i] = normalizeDriverValue(values[i])
	}
	return nil
}

func normalizeDriverValue(value any) driver.Value {
	switch value := value.(type) {
	case nil, int64, float64, bool, string, time.Time:
		return value
	case []byte:
		return append([]byte(nil), value...)
	case int:
		return int64(value)
	case int32:
		return int64(value)
	case uint:
		return int64(value)
	case uint32:
		return int64(value)
	case uint64:
		if value <= math.MaxInt64 {
			return int64(value)
		}
		return strconv.FormatUint(value, 10)
	default:
		return fmt.Sprint(value)
	}
}

func namedValuesToArgs(values []driver.NamedValue) []any {
	args := make([]any, len(values))
	for i, value := range values {
		normalized := normalizeSQLiteBindValue(value.Value)
		if value.Name != "" {
			args[i] = sql.Named(value.Name, normalized)
		} else {
			args[i] = normalized
		}
	}
	return args
}

func normalizeSQLiteBindValue(value any) any {
	if timestamp, ok := value.(time.Time); ok {
		// MySQL DATETIME values are timezone-free and this application persists
		// them in UTC. modernc/sqlite otherwise serializes time.Time with a zone
		// suffix (for example "+0000 UTC"), which makes values produced by SQL
		// date functions compare unequal to bound timestamps.
		return formatSQLTimestamp(timestamp.UTC(), timestampPrecision(timestamp))
	}
	return value
}

func rewriteSQLiteQuery(query string) string {
	query = replaceRegexpOutsideSQLLiterals(query, insertIgnoreRE, "INSERT OR IGNORE")
	query = replaceRegexpOutsideSQLLiterals(query, truncateTableRE, "DELETE FROM")
	query = replaceRegexpOutsideSQLLiterals(query, nullSafeEqualRE, " IS ")
	query = rewriteUpdateAliases(query)
	query = rewriteDuplicateKeyUpdate(query)
	query = replaceRegexpOutsideSQLLiterals(query, forUpdateRE, "")
	query = replaceRegexpOutsideSQLLiterals(query, signedCastRE, "AS INTEGER")
	query = rewriteDatetimeCasts(query)
	query = replaceRegexpOutsideSQLLiterals(query, sessionTimezoneRE, "'UTC'")
	query = rewritePercentileCont(query)
	query = rewriteIsNullFunctions(query)
	query = rewriteDateColumnComparisons(query)
	return rewriteIntervals(query)
}

// rewriteDateColumnComparisons preserves MySQL DATE-to-DATETIME comparison
// semantics. SQLite compares DATE text (YYYY-MM-DD) lexically against bound
// timestamps, so midnight time.Time values would not match the same calendar day.
func rewriteDateColumnComparisons(query string) string {
	return replaceRegexpOutsideSQLLiteralsFunc(query, dateColumnParamRE, func(match string) string {
		parts := dateColumnParamRE.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + " " + parts[2] + " DATE(?)"
	})
}

func rewriteUpdateAliases(query string) string {
	masked := maskSQLLiterals(query)
	matches := updateAliasRE.FindAllStringSubmatchIndex(query, -1)
	if len(matches) == 0 {
		return query
	}

	var out strings.Builder
	last := 0
	for _, match := range matches {
		if !hasSQLWordAt(masked, match[0], "UPDATE") {
			continue
		}
		_, _ = out.WriteString(query[last:match[0]])
		_, _ = out.WriteString("UPDATE ")
		_, _ = out.WriteString(query[match[2]:match[3]])
		_, _ = out.WriteString(" AS ")
		_, _ = out.WriteString(query[match[4]:match[5]])
		_, _ = out.WriteString(" SET")
		last = match[1]
	}
	if last == 0 {
		return query
	}
	_, _ = out.WriteString(query[last:])
	return out.String()
}

func rewriteDuplicateKeyUpdate(query string) string {
	masked := maskSQLLiterals(query)
	locations := duplicateKeyRE.FindAllStringIndex(masked, -1)
	if len(locations) == 0 {
		return query
	}
	var out strings.Builder
	last := 0
	for i, location := range locations {
		_, _ = out.WriteString(query[last:location[0]])
		_, _ = out.WriteString("ON CONFLICT DO UPDATE SET")
		segmentEnd := len(query)
		if i+1 < len(locations) {
			segmentEnd = locations[i+1][0]
		}
		suffix := query[location[1]:segmentEnd]
		suffix = replaceRegexpOutsideSQLLiteralsFunc(suffix, insertedValueRE, func(match string) string {
			return "excluded." + insertedValueRE.FindStringSubmatch(match)[1]
		})
		_, _ = out.WriteString(suffix)
		last = segmentEnd
	}
	if last < len(query) {
		_, _ = out.WriteString(query[last:])
	}
	return out.String()
}

// rewriteDatetimeCasts preserves MySQL DATETIME comparison semantics. SQLite's
// CAST(... AS DATETIME) applies numeric affinity (for example, an ISO timestamp
// becomes only its year), while datetime(...) returns a normalized timestamp.
func rewriteDatetimeCasts(query string) string {
	for {
		masked := maskSQLLiterals(query)
		locations := castRE.FindAllStringIndex(masked, -1)
		rewritten := false
		for _, location := range locations {
			open := skipSQLSpace(masked, location[1])
			if open >= len(masked) || masked[open] != '(' {
				continue
			}
			close := matchingParen(masked, open)
			if close < 0 {
				continue
			}
			expressionEnd, ok := datetimeCastExpressionEnd(masked[open+1 : close])
			if !ok {
				continue
			}
			expression := strings.TrimSpace(query[open+1 : open+1+expressionEnd])
			if expression == "" {
				continue
			}
			query = query[:location[0]] + "datetime(" + expression + ")" + query[close+1:]
			rewritten = true
			break
		}
		if !rewritten {
			return query
		}
	}
}

func datetimeCastExpressionEnd(body string) (int, bool) {
	depth := 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth != 0 || !hasSQLWordAt(body, i, "AS") {
				continue
			}
			typeStart := skipSQLSpace(body, i+len("AS"))
			if !hasSQLWordAt(body, typeStart, "DATETIME") {
				continue
			}
			typeEnd := skipSQLSpace(body, typeStart+len("DATETIME"))
			if typeEnd < len(body) && body[typeEnd] == '(' {
				precisionEnd := matchingParen(body, typeEnd)
				if precisionEnd < 0 {
					return 0, false
				}
				typeEnd = skipSQLSpace(body, precisionEnd+1)
			}
			if typeEnd == len(body) {
				return i, true
			}
		}
	}
	return 0, false
}

func rewriteIsNullFunctions(query string) string {
	for {
		masked := maskSQLLiterals(query)
		locations := isNullFunctionRE.FindAllStringIndex(masked, -1)
		rewritten := false
		for _, location := range locations {
			open := skipSQLSpace(masked, location[1])
			if open >= len(masked) || masked[open] != '(' {
				continue
			}
			close := matchingParen(masked, open)
			if close < 0 {
				continue
			}
			expression := strings.TrimSpace(query[open+1 : close])
			if expression == "" {
				continue
			}
			query = query[:location[0]] + "(" + expression + " IS NULL)" + query[close+1:]
			rewritten = true
			break
		}
		if !rewritten {
			return query
		}
	}
}

func rewritePercentileCont(query string) string {
	for {
		masked := maskSQLLiterals(query)
		locations := percentileRE.FindAllStringIndex(masked, -1)
		rewritten := false
		for _, location := range locations {
			open := skipSQLSpace(masked, location[1])
			if open >= len(masked) || masked[open] != '(' {
				continue
			}
			close := matchingParen(masked, open)
			if close < 0 {
				continue
			}
			percentile := strings.TrimSpace(query[open+1 : close])
			pos := skipSQLSpace(masked, close+1)
			if !hasSQLWordAt(masked, pos, "WITHIN") {
				continue
			}
			pos = skipSQLSpace(masked, pos+len("WITHIN"))
			if !hasSQLWordAt(masked, pos, "GROUP") {
				continue
			}
			pos = skipSQLSpace(masked, pos+len("GROUP"))
			if pos >= len(masked) || masked[pos] != '(' {
				continue
			}
			groupClose := matchingParen(masked, pos)
			if groupClose < 0 {
				continue
			}
			contentStart := skipSQLSpace(masked, pos+1)
			if !hasSQLWordAt(masked, contentStart, "ORDER") {
				continue
			}
			contentStart = skipSQLSpace(masked, contentStart+len("ORDER"))
			if !hasSQLWordAt(masked, contentStart, "BY") {
				continue
			}
			contentStart = skipSQLSpace(masked, contentStart+len("BY"))
			expression := strings.TrimSpace(query[contentStart:groupClose])
			if percentile == "" || expression == "" {
				continue
			}
			query = query[:location[0]] + "percentile_cont(" + expression + ", " + percentile + ")" + query[groupClose+1:]
			rewritten = true
			break
		}
		if !rewritten {
			return query
		}
	}
}

func rewriteIntervals(query string) string {
	for {
		masked := maskSQLLiterals(query)
		locations := intervalRE.FindAllStringIndex(masked, -1)
		rewritten := false
		for _, location := range locations {
			amountStart := skipSQLSpace(masked, location[1])
			amountEnd := intervalAmountEnd(masked, amountStart)
			if amountEnd < 0 {
				continue
			}
			unitStart := skipSQLSpace(masked, amountEnd)
			unitEnd := unitStart
			for unitEnd < len(masked) && isIdentifierByte(masked[unitEnd]) {
				unitEnd++
			}
			if unitEnd == unitStart {
				continue
			}
			unit, ok := mysqlIntervalUnit(masked[unitStart:unitEnd])
			if !ok {
				continue
			}
			opPos := location[0] - 1
			for opPos >= 0 && isSQLSpace(query[opPos]) {
				opPos--
			}
			if opPos < 0 || masked[opPos] != '+' && masked[opPos] != '-' {
				continue
			}
			leftEnd := opPos
			for leftEnd > 0 && isSQLSpace(query[leftEnd-1]) {
				leftEnd--
			}
			leftStart := sqlOperandStart(query, masked, leftEnd)
			if leftStart < 0 || leftStart == leftEnd {
				continue
			}
			left := strings.TrimSpace(query[leftStart:leftEnd])
			amount := strings.TrimSpace(query[amountStart:amountEnd])
			sign := "1"
			if masked[opPos] == '-' {
				sign = "-1"
			}
			query = query[:leftStart] + "mysql_datetime_add(" + left + ", " + amount + ", '" + unit + "', " + sign + ")" + query[unitEnd:]
			rewritten = true
			break
		}
		if !rewritten {
			return query
		}
	}
}

func intervalAmountEnd(masked string, start int) int {
	if start >= len(masked) {
		return -1
	}
	if masked[start] == '(' {
		close := matchingParen(masked, start)
		if close < 0 {
			return -1
		}
		return close + 1
	}
	if masked[start] == '?' {
		return start + 1
	}
	end := start
	if masked[end] == '+' || masked[end] == '-' {
		end++
	}
	for end < len(masked) && (masked[end] >= '0' && masked[end] <= '9' || masked[end] == '.') {
		end++
	}
	if end == start {
		return -1
	}
	return end
}

func mysqlIntervalUnit(unit string) (string, bool) {
	switch strings.TrimSuffix(strings.ToLower(unit), "s") {
	case "second", "minute", "hour", "day", "week", "month", "quarter", "year", "millisecond", "microsecond":
		return strings.TrimSuffix(strings.ToLower(unit), "s"), true
	default:
		return "", false
	}
}

func sqlOperandStart(query, masked string, end int) int {
	if end <= 0 {
		return -1
	}
	last := end - 1
	if masked[last] == ')' {
		open := matchingParenBackward(masked, last)
		if open < 0 {
			return -1
		}
		start := open
		for start > 0 && isIdentifierByte(masked[start-1]) {
			start--
		}
		return start
	}
	if query[last] == '\'' || query[last] == '"' {
		quote := query[last]
		for i := last - 1; i >= 0; i-- {
			if query[i] == quote {
				return i
			}
		}
		return -1
	}
	if masked[last] == '?' {
		return last
	}
	start := last
	for start >= 0 && (isIdentifierByte(masked[start]) || masked[start] == '.') {
		start--
	}
	return start + 1
}

func matchingParen(masked string, open int) int {
	depth := 0
	for i := open; i < len(masked); i++ {
		switch masked[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func matchingParenBackward(masked string, close int) int {
	depth := 0
	for i := close; i >= 0; i-- {
		switch masked[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func skipSQLSpace(query string, pos int) int {
	for pos < len(query) && isSQLSpace(query[pos]) {
		pos++
	}
	return pos
}
func isSQLSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' || ch == '\f'
}
func isIdentifierByte(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '$' || ch == '`'
}
func hasSQLWordAt(query string, pos int, word string) bool {
	return pos >= 0 && pos+len(word) <= len(query) && strings.EqualFold(query[pos:pos+len(word)], word) && (pos+len(word) == len(query) || !isIdentifierByte(query[pos+len(word)]))
}

func replaceRegexpOutsideSQLLiterals(query string, expression *regexp.Regexp, replacement string) string {
	return replaceRegexpOutsideSQLLiteralsFunc(query, expression, func(string) string { return replacement })
}
func replaceRegexpOutsideSQLLiteralsFunc(query string, expression *regexp.Regexp, replacement func(string) string) string {
	masked := maskSQLLiterals(query)
	matches := expression.FindAllStringIndex(masked, -1)
	if len(matches) == 0 {
		return query
	}
	var out strings.Builder
	last := 0
	for _, match := range matches {
		_, _ = out.WriteString(query[last:match[0]])
		_, _ = out.WriteString(replacement(query[match[0]:match[1]]))
		last = match[1]
	}
	_, _ = out.WriteString(query[last:])
	return out.String()
}

func maskSQLLiterals(query string) string {
	masked := []byte(query)
	const (
		plain = iota
		singleQuote
		doubleQuote
		backtickQuote
		lineComment
		blockComment
	)
	state := plain
	for i := 0; i < len(masked); i++ {
		switch state {
		case plain:
			switch masked[i] {
			case '\'':
				state = singleQuote
				masked[i] = ' '
			case '"':
				state = doubleQuote
				masked[i] = ' '
			case '`':
				state = backtickQuote
				masked[i] = ' '
			case '#':
				state = lineComment
				masked[i] = ' '
			case '-':
				if i+1 < len(masked) && masked[i+1] == '-' {
					state = lineComment
					masked[i] = ' '
				}
			case '/':
				if i+1 < len(masked) && masked[i+1] == '*' {
					state = blockComment
					masked[i] = ' '
				}
			}
		case singleQuote:
			if masked[i] == '\\' && i+1 < len(masked) {
				masked[i] = ' '
				i++
				masked[i] = ' '
				continue
			}
			if masked[i] == '\'' {
				if i+1 < len(masked) && masked[i+1] == '\'' {
					masked[i] = ' '
					i++
					masked[i] = ' '
					continue
				}
				state = plain
			}
			masked[i] = ' '
		case doubleQuote:
			if masked[i] == '\\' && i+1 < len(masked) {
				masked[i] = ' '
				i++
				masked[i] = ' '
				continue
			}
			if masked[i] == '"' {
				if i+1 < len(masked) && masked[i+1] == '"' {
					masked[i] = ' '
					i++
					masked[i] = ' '
					continue
				}
				state = plain
			}
			masked[i] = ' '
		case backtickQuote:
			if masked[i] == '`' {
				state = plain
			}
			masked[i] = ' '
		case lineComment:
			if masked[i] == '\n' {
				state = plain
			} else {
				masked[i] = ' '
			}
		case blockComment:
			if masked[i] == '*' && i+1 < len(masked) && masked[i+1] == '/' {
				masked[i] = ' '
				i++
				masked[i] = ' '
				state = plain
			} else {
				masked[i] = ' '
			}
		}
	}
	return string(masked)
}

func registerSQLiteCompatFunctions() {
	registerScalar := func(name string, args int32, deterministic bool, fn func(*modernsqlite.FunctionContext, []driver.Value) (driver.Value, error)) {
		modernsqlite.MustRegisterFunction(name, &modernsqlite.FunctionImpl{NArgs: args, Deterministic: deterministic, Scalar: fn})
	}
	registerScalar("find_in_set", 2, true, sqliteFindInSet)
	registerScalar("json_unquote", 1, true, sqliteJSONUnquote)
	registerScalar("json_merge_patch", -1, true, sqliteJSONMergePatch)
	registerScalar("now", -1, false, sqliteNow)
	registerScalar("utc_timestamp", -1, false, sqliteNow)
	registerScalar("curdate", 0, false, sqliteCurdate)
	registerScalar("date_format", 2, true, sqliteDateFormat)
	registerScalar("convert_tz", 3, true, sqliteConvertTZ)
	registerScalar("substring_index", 3, true, sqliteSubstringIndex)
	registerScalar("left", 2, true, sqliteLeft)
	registerScalar("concat", -1, true, sqliteConcat)
	registerScalar("dayofweek", 1, true, sqliteDayOfWeek)
	registerScalar("regexp", 2, true, sqliteRegexp)
	registerScalar("mysql_datetime_add", 4, true, sqliteMySQLDateTimeAdd)
	registerScalar("greatest", -1, true, func(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		return sqliteExtremum(args, true)
	})
	registerScalar("least", -1, true, func(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		return sqliteExtremum(args, false)
	})
	registerScalar("if", 3, true, sqliteIf)
	registerScalar("unix_timestamp", -1, false, sqliteUnixTimestamp)
	registerScalar("from_unixtime", -1, true, sqliteFromUnixTime)
	modernsqlite.MustRegisterFunction("percentile_cont", &modernsqlite.FunctionImpl{
		NArgs: 2, Deterministic: true,
		MakeAggregate: func(modernsqlite.FunctionContext) (modernsqlite.AggregateFunction, error) {
			return &sqlitePercentileCont{}, nil
		},
	})
}

func sqliteFindInSet(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil || args[1] == nil {
		return nil, nil
	}
	needle := valueString(args[0])
	for i, item := range strings.Split(valueString(args[1]), ",") {
		if item == needle {
			return int64(i + 1), nil
		}
	}
	return int64(0), nil
}

func sqliteJSONUnquote(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil {
		return nil, nil
	}
	s := valueString(args[0])
	var decoded string
	if json.Unmarshal([]byte(s), &decoded) == nil {
		return decoded, nil
	}
	return s, nil
}

func sqliteJSONMergePatch(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("JSON_MERGE_PATCH requires at least two arguments")
	}
	var target any
	for i, arg := range args {
		if arg == nil {
			return nil, nil
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(valueString(arg)))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("JSON_MERGE_PATCH argument %d: %w", i+1, err)
		}
		if i == 0 {
			target = value
		} else {
			target = mergeJSONPatch(target, value)
		}
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func mergeJSONPatch(target, patch any) any {
	patchObject, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	targetObject, ok := target.(map[string]any)
	if !ok {
		targetObject = make(map[string]any)
	}
	for key, value := range patchObject {
		if value == nil {
			delete(targetObject, key)
		} else {
			targetObject[key] = mergeJSONPatch(targetObject[key], value)
		}
	}
	return targetObject
}

func sqliteNow(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	precision := 0
	if len(args) > 1 {
		return nil, fmt.Errorf("NOW accepts zero or one argument")
	}
	if len(args) == 1 {
		value, ok := numericValue(args[0])
		if !ok || value < 0 || value > 6 || math.Trunc(value) != value {
			return nil, fmt.Errorf("invalid fractional seconds precision")
		}
		precision = int(value)
	}
	return formatSQLTimestamp(time.Now().UTC(), precision), nil
}
func sqliteCurdate(_ *modernsqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
	return time.Now().UTC().Format("2006-01-02"), nil
}
func sqliteDateFormat(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil || args[1] == nil {
		return nil, nil
	}
	value, ok := parseSQLTime(args[0], time.UTC)
	if !ok {
		return nil, nil
	}
	return formatMySQLDate(value, valueString(args[1])), nil
}
func sqliteConvertTZ(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil || args[1] == nil || args[2] == nil {
		return nil, nil
	}
	from, ok := mysqlTimeLocation(valueString(args[1]))
	if !ok {
		return nil, nil
	}
	to, ok := mysqlTimeLocation(valueString(args[2]))
	if !ok {
		return nil, nil
	}
	value, ok := parseSQLTime(args[0], from)
	if !ok {
		return nil, nil
	}
	return formatSQLTimestamp(value.In(to), timestampPrecision(args[0])), nil
}
func sqliteLeft(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil || args[1] == nil {
		return nil, nil
	}
	length, ok := numericValue(args[1])
	if !ok {
		return nil, fmt.Errorf("LEFT length must be numeric")
	}
	if length <= 0 {
		return "", nil
	}
	runes := []rune(valueString(args[0]))
	if int(length) >= len(runes) {
		return string(runes), nil
	}
	return string(runes[:int(length)]), nil
}

func sqliteConcat(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	var out strings.Builder
	for _, arg := range args {
		if arg == nil {
			return nil, nil
		}
		_, _ = out.WriteString(valueString(arg))
	}
	return out.String(), nil
}

func sqliteDayOfWeek(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil {
		return nil, nil
	}
	value, ok := parseSQLTime(args[0], time.UTC)
	if !ok {
		return nil, nil
	}
	return int64(value.Weekday()) + 1, nil
}

func sqliteRegexp(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil || args[1] == nil {
		return nil, nil
	}
	pattern, err := regexp.Compile(valueString(args[0]))
	if err != nil {
		return nil, err
	}
	if pattern.MatchString(valueString(args[1])) {
		return int64(1), nil
	}
	return int64(0), nil
}

func sqliteMySQLDateTimeAdd(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil || args[1] == nil || args[2] == nil || args[3] == nil {
		return nil, nil
	}
	value, ok := parseSQLTime(args[0], time.UTC)
	if !ok {
		return nil, nil
	}
	amount, ok := numericValue(args[1])
	if !ok {
		return nil, nil
	}
	sign, ok := numericValue(args[3])
	if !ok {
		return nil, nil
	}
	amount *= sign
	unit := strings.ToLower(strings.TrimSpace(valueString(args[2])))
	precision := timestampPrecision(args[0])

	switch unit {
	case "microsecond":
		value = value.Add(time.Duration(amount * float64(time.Microsecond)))
		if precision < 6 {
			precision = 6
		}
	case "millisecond":
		value = value.Add(time.Duration(amount * float64(time.Millisecond)))
		if precision < 3 {
			precision = 3
		}
	case "second":
		value = value.Add(time.Duration(amount * float64(time.Second)))
	case "minute":
		value = value.Add(time.Duration(amount * float64(time.Minute)))
	case "hour":
		value = value.Add(time.Duration(amount * float64(time.Hour)))
	case "day":
		value = value.Add(time.Duration(amount * 24 * float64(time.Hour)))
	case "week":
		value = value.Add(time.Duration(amount * 7 * 24 * float64(time.Hour)))
	case "month":
		value = addMySQLCalendarMonths(value, int(math.Trunc(amount)))
	case "quarter":
		value = addMySQLCalendarMonths(value, int(math.Trunc(amount))*3)
	case "year":
		value = addMySQLCalendarMonths(value, int(math.Trunc(amount))*12)
	default:
		return nil, nil
	}
	return formatSQLTimestamp(value, precision), nil
}

func addMySQLCalendarMonths(value time.Time, months int) time.Time {
	year, month, day := value.Date()
	monthIndex := int(month) - 1 + months
	targetYear := year + monthIndex/12
	targetMonthIndex := monthIndex % 12
	if targetMonthIndex < 0 {
		targetMonthIndex += 12
		targetYear--
	}
	targetMonth := time.Month(targetMonthIndex + 1)
	lastDay := time.Date(targetYear, targetMonth+1, 0, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(targetYear, targetMonth, day, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
}

func sqliteSubstringIndex(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil || args[1] == nil || args[2] == nil {
		return nil, nil
	}
	input, delim := valueString(args[0]), valueString(args[1])
	countValue, ok := numericValue(args[2])
	if !ok || delim == "" || countValue == 0 {
		return "", nil
	}
	count, parts := int(countValue), strings.Split(input, delim)
	if count > 0 {
		if count >= len(parts) {
			return input, nil
		}
		return strings.Join(parts[:count], delim), nil
	}
	count = -count
	if count >= len(parts) {
		return input, nil
	}
	return strings.Join(parts[len(parts)-count:], delim), nil
}
func sqliteExtremum(args []driver.Value, greatest bool) (driver.Value, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("GREATEST/LEAST requires at least one argument")
	}
	for _, arg := range args {
		if arg == nil {
			return nil, nil
		}
	}
	if values, ok := allNumeric(args); ok {
		selected := values[0]
		for _, value := range values[1:] {
			if greatest && value > selected || !greatest && value < selected {
				selected = value
			}
		}
		return selected, nil
	}
	selected := valueString(args[0])
	for _, arg := range args[1:] {
		value := valueString(arg)
		if greatest && value > selected || !greatest && value < selected {
			selected = value
		}
	}
	return selected, nil
}
func sqliteIf(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if mysqlTruthy(args[0]) {
		return args[1], nil
	}
	return args[2], nil
}
func sqliteUnixTimestamp(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("UNIX_TIMESTAMP accepts zero or one argument")
	}
	if len(args) == 0 {
		return time.Now().UTC().Unix(), nil
	}
	if args[0] == nil {
		return nil, nil
	}
	value, ok := parseSQLTime(args[0], time.UTC)
	if !ok {
		return nil, nil
	}
	return value.Unix(), nil
}
func sqliteFromUnixTime(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("FROM_UNIXTIME accepts one or two arguments")
	}
	if args[0] == nil {
		return nil, nil
	}
	seconds, ok := numericValue(args[0])
	if !ok {
		return nil, nil
	}
	whole, fraction := math.Modf(seconds)
	value := time.Unix(int64(whole), int64(fraction*float64(time.Second))).UTC()
	if len(args) == 2 {
		if args[1] == nil {
			return nil, nil
		}
		return formatMySQLDate(value, valueString(args[1])), nil
	}
	return formatSQLTimestamp(value, 6), nil
}

type sqlitePercentileCont struct {
	values     []float64
	percentile float64
	set        bool
	err        error
}

func (p *sqlitePercentileCont) Step(_ *modernsqlite.FunctionContext, args []driver.Value) error {
	if p.err != nil || len(args) != 2 {
		return p.err
	}
	percentile, ok := numericValue(args[1])
	if !ok || percentile < 0 || percentile > 1 {
		p.err = fmt.Errorf("percentile must be between 0 and 1")
		return p.err
	}
	if p.set && percentile != p.percentile {
		p.err = fmt.Errorf("percentile argument must be constant")
		return p.err
	}
	p.percentile, p.set = percentile, true
	if args[0] == nil {
		return nil
	}
	value, ok := numericValue(args[0])
	if !ok {
		p.err = fmt.Errorf("percentile value is not numeric")
		return p.err
	}
	p.values = append(p.values, value)
	return nil
}
func (p *sqlitePercentileCont) WindowInverse(_ *modernsqlite.FunctionContext, args []driver.Value) error {
	if len(args) == 0 || args[0] == nil {
		return nil
	}
	value, ok := numericValue(args[0])
	if !ok {
		return nil
	}
	for i, current := range p.values {
		if current == value {
			p.values = append(p.values[:i], p.values[i+1:]...)
			break
		}
	}
	return nil
}
func (p *sqlitePercentileCont) WindowValue(_ *modernsqlite.FunctionContext) (driver.Value, error) {
	if p.err != nil {
		return nil, p.err
	}
	if len(p.values) == 0 || !p.set {
		return nil, nil
	}
	values := append([]float64(nil), p.values...)
	sort.Float64s(values)
	position := p.percentile * float64(len(values)-1)
	lower, upper := int(math.Floor(position)), int(math.Ceil(position))
	if lower == upper {
		return values[lower], nil
	}
	weight := position - float64(lower)
	return values[lower] + (values[upper]-values[lower])*weight, nil
}
func (p *sqlitePercentileCont) Final(*modernsqlite.FunctionContext) {}

func valueString(value driver.Value) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	case time.Time:
		return formatSQLTimestamp(value, timestampPrecision(value))
	default:
		return fmt.Sprint(value)
	}
}
func numericValue(value driver.Value) (float64, bool) {
	switch value := value.(type) {
	case int64:
		return float64(value), true
	case float64:
		return value, true
	case bool:
		if value {
			return 1, true
		}
		return 0, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(string(value)), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
func allNumeric(args []driver.Value) ([]float64, bool) {
	values := make([]float64, len(args))
	for i, arg := range args {
		value, ok := numericValue(arg)
		if !ok {
			return nil, false
		}
		values[i] = value
	}
	return values, true
}
func mysqlTruthy(value driver.Value) bool {
	if value == nil {
		return false
	}
	numeric, ok := numericValue(value)
	return ok && numeric != 0
}

func parseSQLTime(value driver.Value, defaultLocation *time.Location) (time.Time, bool) {
	if value == nil {
		return time.Time{}, false
	}
	if parsed, ok := value.(time.Time); ok {
		return parsed, true
	}
	text := strings.TrimSpace(valueString(value))
	if text == "" || strings.EqualFold(text, "null") {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05 -0700 MST"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	if defaultLocation == nil {
		defaultLocation = time.UTC
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, text, defaultLocation); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
func mysqlTimeLocation(name string) (*time.Location, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	if strings.EqualFold(name, "UTC") || strings.EqualFold(name, "GMT") || strings.EqualFold(name, "SYSTEM") || name == "+00:00" || name == "-00:00" {
		return time.UTC, true
	}
	if len(name) == 6 && (name[0] == '+' || name[0] == '-') && name[3] == ':' {
		hours, hourErr := strconv.Atoi(name[1:3])
		minutes, minuteErr := strconv.Atoi(name[4:6])
		if hourErr == nil && minuteErr == nil && hours <= 14 && minutes < 60 {
			offset := (hours*60 + minutes) * 60
			if name[0] == '-' {
				offset = -offset
			}
			return time.FixedZone(name, offset), true
		}
	}
	location, err := time.LoadLocation(name)
	return location, err == nil
}
func timestampPrecision(value any) int {
	var text string
	switch value := value.(type) {
	case string:
		text = value
	case []byte:
		text = string(value)
	case time.Time:
		microseconds := value.Nanosecond() / 1000
		if microseconds == 0 {
			return 0
		}
		precision := 6
		for precision > 0 && microseconds%10 == 0 {
			microseconds /= 10
			precision--
		}
		return precision
	}
	dot := strings.IndexByte(text, '.')
	if dot < 0 {
		return 0
	}
	precision := 0
	for i := dot + 1; i < len(text) && text[i] >= '0' && text[i] <= '9' && precision < 6; i++ {
		precision++
	}
	return precision
}
func formatSQLTimestamp(value time.Time, precision int) string {
	base := value.Format("2006-01-02 15:04:05")
	if precision <= 0 {
		return base
	}
	if precision > 6 {
		precision = 6
	}
	fraction := fmt.Sprintf("%06d", value.Nanosecond()/1000)
	return base + "." + fraction[:precision]
}
func formatMySQLDate(value time.Time, format string) string {
	var out strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			_ = out.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] {
		case '%':
			_ = out.WriteByte('%')
		case 'Y':
			fmt.Fprintf(&out, "%04d", value.Year())
		case 'y':
			fmt.Fprintf(&out, "%02d", value.Year()%100)
		case 'x':
			year, _ := value.ISOWeek()
			fmt.Fprintf(&out, "%04d", year)
		case 'v':
			_, week := value.ISOWeek()
			fmt.Fprintf(&out, "%02d", week)
		case 'm':
			fmt.Fprintf(&out, "%02d", int(value.Month()))
		case 'c':
			fmt.Fprintf(&out, "%d", int(value.Month()))
		case 'M':
			_, _ = out.WriteString(value.Month().String())
		case 'b':
			_, _ = out.WriteString(value.Month().String()[:3])
		case 'd':
			fmt.Fprintf(&out, "%02d", value.Day())
		case 'e':
			fmt.Fprintf(&out, "%d", value.Day())
		case 'H':
			fmt.Fprintf(&out, "%02d", value.Hour())
		case 'k':
			fmt.Fprintf(&out, "%d", value.Hour())
		case 'h', 'I':
			hour := value.Hour() % 12
			if hour == 0 {
				hour = 12
			}
			fmt.Fprintf(&out, "%02d", hour)
		case 'l':
			hour := value.Hour() % 12
			if hour == 0 {
				hour = 12
			}
			fmt.Fprintf(&out, "%d", hour)
		case 'i':
			fmt.Fprintf(&out, "%02d", value.Minute())
		case 's', 'S':
			fmt.Fprintf(&out, "%02d", value.Second())
		case 'f':
			fmt.Fprintf(&out, "%06d", value.Nanosecond()/1000)
		case 'p':
			_, _ = out.WriteString(value.Format("PM"))
		case 'W':
			_, _ = out.WriteString(value.Weekday().String())
		case 'a':
			_, _ = out.WriteString(value.Weekday().String()[:3])
		case 'j':
			fmt.Fprintf(&out, "%03d", value.YearDay())
		case 'w':
			fmt.Fprintf(&out, "%d", int(value.Weekday()))
		case 'T':
			_, _ = out.WriteString(value.Format("15:04:05"))
		case 'r':
			_, _ = out.WriteString(value.Format("03:04:05 PM"))
		default:
			_ = out.WriteByte('%')
			if unicode.IsPrint(rune(format[i])) {
				_ = out.WriteByte(format[i])
			}
		}
	}
	return out.String()
}

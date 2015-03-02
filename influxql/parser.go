package influxql

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DateFormat represents the format for date literals.
	DateFormat = "2006-01-02"

	// DateTimeFormat represents the format for date time literals.
	DateTimeFormat = "2006-01-02 15:04:05.999999"
)

// Parser represents an InfluxQL parser.
type Parser struct {
	s *bufScanner
}

// NewParser returns a new instance of Parsr.
func NewParser(r io.Reader) *Parser {
	return &Parser{s: newBufScanner(r)}
}

// ParseQuery parses a query string and returns its AST representation.
func ParseQuery(s string) (*Query, error) { return NewParser(strings.NewReader(s)).ParseQuery() }

// ParseExpr parses an expression string and returns its AST representation.
func ParseExpr(s string) (Expr, error) { return NewParser(strings.NewReader(s)).ParseExpr() }

// ParseQuery parses an InfluxQL string and returns a Query AST object.
func (p *Parser) ParseQuery() (*Query, error) {
	// If there's only whitespace then return no statements.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok == EOF {
		return &Query{}, nil
	}
	p.unscan()

	// Otherwise parse statements until EOF.
	var statements Statements
	for {

		// Read the next statement.
		s, err := p.ParseStatement()
		if err != nil {
			return nil, err
		}
		statements = append(statements, s)

		// Expect a semicolon or EOF after the statement.
		if tok, pos, lit := p.scanIgnoreWhitespace(); tok != SEMICOLON && tok != EOF {
			return nil, newParseError(tokstr(tok, lit), []string{";", "EOF"}, pos)
		} else if tok == EOF {
			break
		}
	}

	return &Query{Statements: statements}, nil
}

// ParseStatement parses an InfluxQL string and returns a Statement AST object.
func (p *Parser) ParseStatement() (Statement, error) {
	// Inspect the first token.
	tok, pos, lit := p.scanIgnoreWhitespace()
	switch tok {
	case SELECT:
		return p.parseSelectStatement(targetNotRequired)
	case DELETE:
		return p.parseDeleteStatement()
	case SHOW:
		return p.parseShowStatement()
	case CREATE:
		return p.parseCreateStatement()
	case DROP:
		return p.parseDropStatement()
	case GRANT:
		return p.parseGrantStatement()
	case REVOKE:
		return p.parseRevokeStatement()
	case ALTER:
		return p.parseAlterStatement()
	default:
		return nil, newParseError(tokstr(tok, lit), []string{"SELECT"}, pos)
	}
}

// parseShowStatement parses a string and returns a list statement.
// This function assumes the SHOW token has already been consumed.
func (p *Parser) parseShowStatement() (Statement, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	switch tok {
	case CONTINUOUS:
		return p.parseShowContinuousQueriesStatement()
	case DATABASES:
		return p.parseShowDatabasesStatement()
	case FIELD:
		tok, pos, lit := p.scanIgnoreWhitespace()
		if tok == KEYS {
			return p.parseShowFieldKeysStatement()
		}
		return nil, newParseError(tokstr(tok, lit), []string{"KEYS", "VALUES"}, pos)
	case MEASUREMENTS:
		return p.parseShowMeasurementsStatement()
	case RETENTION:
		tok, pos, lit := p.scanIgnoreWhitespace()
		if tok == POLICIES {
			return p.parseShowRetentionPoliciesStatement()
		}
		return nil, newParseError(tokstr(tok, lit), []string{"POLICIES"}, pos)
	case SERIES:
		return p.parseShowSeriesStatement()
	case TAG:
		tok, pos, lit := p.scanIgnoreWhitespace()
		if tok == KEYS {
			return p.parseShowTagKeysStatement()
		} else if tok == VALUES {
			return p.parseShowTagValuesStatement()
		}
		return nil, newParseError(tokstr(tok, lit), []string{"KEYS", "VALUES"}, pos)
	case USERS:
		return p.parseShowUsersStatement()
	}

	return nil, newParseError(tokstr(tok, lit), []string{"CONTINUOUS", "DATABASES", "FIELD", "MEASUREMENTS", "RETENTION", "SERIES", "TAG", "USERS"}, pos)
}

// parseCreateStatement parses a string and returns a create statement.
// This function assumes the CREATE token has already been consumned.
func (p *Parser) parseCreateStatement() (Statement, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok == CONTINUOUS {
		return p.parseCreateContinuousQueryStatement()
	} else if tok == DATABASE {
		return p.parseCreateDatabaseStatement()
	} else if tok == USER {
		return p.parseCreateUserStatement()
	} else if tok == RETENTION {
		tok, pos, lit = p.scanIgnoreWhitespace()
		if tok != POLICY {
			return nil, newParseError(tokstr(tok, lit), []string{"POLICY"}, pos)
		}
		return p.parseCreateRetentionPolicyStatement()
	}

	return nil, newParseError(tokstr(tok, lit), []string{"CONTINUOUS", "DATABASE", "USER", "RETENTION"}, pos)
}

// parseDropStatement parses a string and returns a drop statement.
// This function assumes the DROP token has already been consumed.
func (p *Parser) parseDropStatement() (Statement, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok == SERIES {
		return p.parseDropSeriesStatement()
	} else if tok == MEASUREMENT {
		return p.parseDropMeasurementStatement()
	} else if tok == CONTINUOUS {
		return p.parseDropContinuousQueryStatement()
	} else if tok == DATABASE {
		return p.parseDropDatabaseStatement()
	} else if tok == RETENTION {
		if tok, pos, lit := p.scanIgnoreWhitespace(); tok != POLICY {
			return nil, newParseError(tokstr(tok, lit), []string{"POLICY"}, pos)
		}
		return p.parseDropRetentionPolicyStatement()
	} else if tok == USER {
		return p.parseDropUserStatement()
	}

	return nil, newParseError(tokstr(tok, lit), []string{"SERIES", "CONTINUOUS", "MEASUREMENT"}, pos)
}

// parseAlterStatement parses a string and returns an alter statement.
// This function assumes the ALTER token has already been consumed.
func (p *Parser) parseAlterStatement() (Statement, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok == RETENTION {
		if tok, pos, lit = p.scanIgnoreWhitespace(); tok != POLICY {
			return nil, newParseError(tokstr(tok, lit), []string{"POLICY"}, pos)
		}
		return p.parseAlterRetentionPolicyStatement()
	}

	return nil, newParseError(tokstr(tok, lit), []string{"RETENTION"}, pos)
}

// parseCreateRetentionPolicyStatement parses a string and returns a create retention policy statement.
// This function assumes the CREATE RETENTION POLICY tokens have already been consumed.
func (p *Parser) parseCreateRetentionPolicyStatement() (*CreateRetentionPolicyStatement, error) {
	stmt := &CreateRetentionPolicyStatement{}

	// Parse the retention policy name.
	ident, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = ident

	// Consume the required ON token.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != ON {
		return nil, newParseError(tokstr(tok, lit), []string{"ON"}, pos)
	}

	// Parse the database name.
	ident, err = p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Database = ident

	// Parse required DURATION token.
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok != DURATION {
		return nil, newParseError(tokstr(tok, lit), []string{"DURATION"}, pos)
	}

	// Parse duration value
	d, err := p.parseDuration()
	if err != nil {
		return nil, err
	}
	stmt.Duration = d

	// Parse required REPLICATION token.
	if tok, pos, lit = p.scanIgnoreWhitespace(); tok != REPLICATION {
		return nil, newParseError(tokstr(tok, lit), []string{"REPLICATION"}, pos)
	}

	// Parse replication value.
	n, err := p.parseInt(1, math.MaxInt32)
	if err != nil {
		return nil, err
	}
	stmt.Replication = n

	// Parse optional DEFAULT token.
	if tok, pos, lit = p.scanIgnoreWhitespace(); tok == DEFAULT {
		stmt.Default = true
	} else {
		p.unscan()
	}

	return stmt, nil
}

// parseAlterRetentionPolicyStatement parses a string and returns an alter retention policy statement.
// This function assumes the ALTER RETENTION POLICY tokens have already been consumned.
func (p *Parser) parseAlterRetentionPolicyStatement() (*AlterRetentionPolicyStatement, error) {
	stmt := &AlterRetentionPolicyStatement{}

	// Parse the retention policy name.
	ident, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = ident

	// Consume the required ON token.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != ON {
		return nil, newParseError(tokstr(tok, lit), []string{"ON"}, pos)
	}

	// Parse the database name.
	ident, err = p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Database = ident

	// Loop through option tokens (DURATION, REPLICATION, DEFAULT, etc.).
	maxNumOptions := 3
Loop:
	for i := 0; i < maxNumOptions; i++ {
		tok, pos, lit := p.scanIgnoreWhitespace()
		switch tok {
		case DURATION:
			d, err := p.parseDuration()
			if err != nil {
				return nil, err
			}
			stmt.Duration = &d
		case REPLICATION:
			n, err := p.parseInt(1, math.MaxInt32)
			if err != nil {
				return nil, err
			}
			stmt.Replication = &n
		case DEFAULT:
			stmt.Default = true
		default:
			if i < 1 {
				return nil, newParseError(tokstr(tok, lit), []string{"DURATION", "RETENTION", "DEFAULT"}, pos)
			}
			p.unscan()
			break Loop
		}
	}

	return stmt, nil
}

// parseInt parses a string and returns an integer literal.
func (p *Parser) parseInt(min, max int) (int, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok != NUMBER {
		return 0, newParseError(tokstr(tok, lit), []string{"number"}, pos)
	}

	// Return an error if the number has a fractional part.
	if strings.Contains(lit, ".") {
		return 0, &ParseError{Message: "number must be an integer", Pos: pos}
	}

	// Convert string to int.
	n, err := strconv.Atoi(lit)
	if err != nil {
		return 0, &ParseError{Message: err.Error(), Pos: pos}
	} else if min > n || n > max {
		return 0, &ParseError{
			Message: fmt.Sprintf("invalid value %d: must be %d <= n <= %d", n, min, max),
			Pos:     pos,
		}
	}

	return n, nil
}

// parseUInt32 parses a string and returns a 32-bit unsigned integer literal.
func (p *Parser) parseUInt32() (uint32, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok != NUMBER {
		return 0, newParseError(tokstr(tok, lit), []string{"number"}, pos)
	}

	// Convert string to unsigned 32-bit integer
	n, err := strconv.ParseUint(lit, 10, 32)
	if err != nil {
		return 0, &ParseError{Message: err.Error(), Pos: pos}
	}

	return uint32(n), nil
}

// parseDuration parses a string and returns a duration literal.
// This function assumes the DURATION token has already been consumed.
func (p *Parser) parseDuration() (time.Duration, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok != DURATION_VAL {
		return 0, newParseError(tokstr(tok, lit), []string{"duration"}, pos)
	}
	d, err := ParseDuration(lit)
	if err != nil {
		return 0, &ParseError{Message: err.Error(), Pos: pos}
	}

	return d, nil
}

// parseIdent parses an identifier.
func (p *Parser) parseIdent() (string, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok != IDENT {
		return "", newParseError(tokstr(tok, lit), []string{"identifier"}, pos)
	}
	return lit, nil
}

// parseIdentList parses a comma delimited list of identifiers.
func (p *Parser) parseIdentList() ([]string, error) {
	// Parse first (required) identifier.
	ident, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	idents := []string{ident}

	// Parse remaining (optional) identifiers.
	for {
		if tok, _, _ := p.scanIgnoreWhitespace(); tok != COMMA {
			p.unscan()
			return idents, nil
		}

		if ident, err = p.parseIdent(); err != nil {
			return nil, err
		}

		idents = append(idents, ident)
	}
}

// parserString parses a string.
func (p *Parser) parseString() (string, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok != STRING {
		return "", newParseError(tokstr(tok, lit), []string{"string"}, pos)
	}
	return lit, nil
}

// parseRevokeStatement parses a string and returns a revoke statement.
// This function assumes the REVOKE token has already been consumend.
func (p *Parser) parseRevokeStatement() (*RevokeStatement, error) {
	stmt := &RevokeStatement{}

	// Parse the privilege to be revoked.
	priv, err := p.parsePrivilege()
	if err != nil {
		return nil, err
	}
	stmt.Privilege = priv

	// Parse ON clause.
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok == ON {
		// Parse the name of the thing we're revoking a privilege to use.
		lit, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		stmt.On = lit

		tok, pos, lit = p.scanIgnoreWhitespace()
	} else if priv != AllPrivileges {
		// ALL PRIVILEGES is the only privilege allowed cluster-wide.
		// No ON clause means query is requesting cluster-wide.
		return nil, newParseError(tokstr(tok, lit), []string{"ON"}, pos)
	}

	// Check for required FROM token.
	if tok != FROM {
		return nil, newParseError(tokstr(tok, lit), []string{"FROM"}, pos)
	}

	// Parse the name of the user we're revoking the privilege from.
	lit, err = p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.User = lit

	return stmt, nil
}

// parseGrantStatement parses a string and returns a grant statement.
// This function assumes the GRANT token has already been consumed.
func (p *Parser) parseGrantStatement() (*GrantStatement, error) {
	stmt := &GrantStatement{}

	// Parse the privilege to be granted.
	priv, err := p.parsePrivilege()
	if err != nil {
		return nil, err
	}
	stmt.Privilege = priv

	// Parse ON clause.
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok == ON {
		// Parse the name of the thing we're granting a privilege to use.
		lit, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		stmt.On = lit

		tok, pos, lit = p.scanIgnoreWhitespace()
	} else if priv != AllPrivileges {
		// ALL PRIVILEGES is the only privilege allowed cluster-wide.
		// No ON clause means query is requesting cluster-wide.
		return nil, newParseError(tokstr(tok, lit), []string{"ON"}, pos)
	}

	// Check for required TO token.
	if tok != TO {
		return nil, newParseError(tokstr(tok, lit), []string{"TO"}, pos)
	}

	// Parse the name of the user we're granting the privilege to.
	lit, err = p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.User = lit

	return stmt, nil
}

// parsePrivilege parses a string and returns a Privilege
func (p *Parser) parsePrivilege() (Privilege, error) {
	tok, pos, lit := p.scanIgnoreWhitespace()
	switch tok {
	case READ:
		return ReadPrivilege, nil
	case WRITE:
		return WritePrivilege, nil
	case ALL:
		// Consume optional PRIVILEGES token
		tok, pos, lit = p.scanIgnoreWhitespace()
		if tok != PRIVILEGES {
			p.unscan()
		}
		return AllPrivileges, nil
	}
	return 0, newParseError(tokstr(tok, lit), []string{"READ", "WRITE", "ALL [PRIVILEGES]"}, pos)
}

// parseSelectStatement parses a select string and returns a Statement AST object.
// This function assumes the SELECT token has already been consumed.
func (p *Parser) parseSelectStatement(tr targetRequirement) (*SelectStatement, error) {
	stmt := &SelectStatement{}
	var err error

	// Parse fields: "SELECT FIELD+".
	if stmt.Fields, err = p.parseFields(); err != nil {
		return nil, err
	}

	// Parse target: "INTO"
	if stmt.Target, err = p.parseTarget(tr); err != nil {
		return nil, err
	}

	// Parse source.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != FROM {
		return nil, newParseError(tokstr(tok, lit), []string{"FROM"}, pos)
	}
	if stmt.Source, err = p.parseSource(); err != nil {
		return nil, err
	}

	// Parse condition: "WHERE EXPR".
	if stmt.Condition, err = p.parseCondition(); err != nil {
		return nil, err
	}

	// Parse dimensions: "GROUP BY DIMENSION+".
	if stmt.Dimensions, err = p.parseDimensions(); err != nil {
		return nil, err
	}

	// Parse sort: "ORDER BY FIELD+".
	if stmt.SortFields, err = p.parseOrderBy(); err != nil {
		return nil, err
	}

	// Parse limit: "LIMIT <n>".
	if stmt.Limit, err = p.parseOptionalTokenAndInt(LIMIT); err != nil {
		return nil, err
	}

	// Parse offset: "OFFSET <n>".
	if stmt.Offset, err = p.parseOptionalTokenAndInt(OFFSET); err != nil {
		return nil, err
	}

	return stmt, nil
}

// targetRequirement specifies whether or not a target clause is required.
type targetRequirement int

const (
	targetRequired targetRequirement = iota
	targetNotRequired
)

// parseTarget parses a string and returns a Target.
func (p *Parser) parseTarget(tr targetRequirement) (*Target, error) {
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != INTO {
		if tr == targetRequired {
			return nil, newParseError(tokstr(tok, lit), []string{"INTO"}, pos)
		}
		p.unscan()
		return nil, nil
	}

	// Parse identifier.  Could be policy or measurement name.
	ident, err := p.parseIdent()
	if err != nil {
		return nil, err
	}

	target := &Target{}
	target.Measurement = ident

	// Parse optional ON.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok != ON {
		p.unscan()
		return target, nil
	}

	// Found an ON token so parse required identifier.
	if ident, err = p.parseIdent(); err != nil {
		return nil, err
	}
	target.Database = ident

	return target, nil
}

// parseDeleteStatement parses a delete string and returns a DeleteStatement.
// This function assumes the DELETE token has already been consumed.
func (p *Parser) parseDeleteStatement() (*DeleteStatement, error) {
	stmt := &DeleteStatement{}

	// Parse source
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != FROM {
		return nil, newParseError(tokstr(tok, lit), []string{"FROM"}, pos)
	}
	source, err := p.parseSource()
	if err != nil {
		return nil, err
	}
	stmt.Source = source

	// Parse condition: "WHERE EXPR".
	condition, err := p.parseCondition()
	if err != nil {
		return nil, err
	}
	stmt.Condition = condition

	return stmt, nil
}

// parseShowSeriesStatement parses a string and returns a ShowSeriesStatement.
// This function assumes the "SHOW SERIES" tokens have already been consumed.
func (p *Parser) parseShowSeriesStatement() (*ShowSeriesStatement, error) {
	stmt := &ShowSeriesStatement{}
	var err error

	// Parse optional FROM.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok == FROM {
		if stmt.Source, err = p.parseSource(); err != nil {
			return nil, err
		}
	} else {
		p.unscan()
	}

	// Parse condition: "WHERE EXPR".
	if stmt.Condition, err = p.parseCondition(); err != nil {
		return nil, err
	}

	// Parse sort: "ORDER BY FIELD+".
	if stmt.SortFields, err = p.parseOrderBy(); err != nil {
		return nil, err
	}

	// Parse limit: "LIMIT <n>".
	if stmt.Limit, err = p.parseOptionalTokenAndInt(LIMIT); err != nil {
		return nil, err
	}

	// Parse offset: "OFFSET <n>".
	if stmt.Offset, err = p.parseOptionalTokenAndInt(OFFSET); err != nil {
		return nil, err
	}

	return stmt, nil
}

// parseShowMeasurementsStatement parses a string and returns a ShowSeriesStatement.
// This function assumes the "SHOW MEASUREMENTS" tokens have already been consumed.
func (p *Parser) parseShowMeasurementsStatement() (*ShowMeasurementsStatement, error) {
	stmt := &ShowMeasurementsStatement{}
	var err error

	// Parse condition: "WHERE EXPR".
	if stmt.Condition, err = p.parseCondition(); err != nil {
		return nil, err
	}

	// Parse sort: "ORDER BY FIELD+".
	if stmt.SortFields, err = p.parseOrderBy(); err != nil {
		return nil, err
	}

	// Parse limit: "LIMIT <n>".
	if stmt.Limit, err = p.parseOptionalTokenAndInt(LIMIT); err != nil {
		return nil, err
	}

	// Parse offset: "OFFSET <n>".
	if stmt.Offset, err = p.parseOptionalTokenAndInt(OFFSET); err != nil {
		return nil, err
	}

	return stmt, nil
}

// parseShowRetentionPoliciesStatement parses a string and returns a ShowRetentionPoliciesStatement.
// This function assumes the "SHOW RETENTION POLICIES" tokens have been consumed.
func (p *Parser) parseShowRetentionPoliciesStatement() (*ShowRetentionPoliciesStatement, error) {
	stmt := &ShowRetentionPoliciesStatement{}

	ident, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Database = ident

	return stmt, nil
}

// parseShowTagKeysStatement parses a string and returns a ShowSeriesStatement.
// This function assumes the "SHOW TAG KEYS" tokens have already been consumed.
func (p *Parser) parseShowTagKeysStatement() (*ShowTagKeysStatement, error) {
	stmt := &ShowTagKeysStatement{}
	var err error

	// Parse optional source.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok == FROM {
		if stmt.Source, err = p.parseSource(); err != nil {
			return nil, err
		}
	} else {
		p.unscan()
	}

	// Parse condition: "WHERE EXPR".
	if stmt.Condition, err = p.parseCondition(); err != nil {
		return nil, err
	}

	// Parse sort: "ORDER BY FIELD+".
	if stmt.SortFields, err = p.parseOrderBy(); err != nil {
		return nil, err
	}

	// Parse limit: "LIMIT <n>".
	if stmt.Limit, err = p.parseOptionalTokenAndInt(LIMIT); err != nil {
		return nil, err
	}

	// Parse offset: "OFFSET <n>".
	if stmt.Offset, err = p.parseOptionalTokenAndInt(OFFSET); err != nil {
		return nil, err
	}

	return stmt, nil
}

// parseShowTagValuesStatement parses a string and returns a ShowSeriesStatement.
// This function assumes the "SHOW TAG VALUES" tokens have already been consumed.
func (p *Parser) parseShowTagValuesStatement() (*ShowTagValuesStatement, error) {
	stmt := &ShowTagValuesStatement{}
	var err error

	// Parse optional source.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok == FROM {
		if stmt.Source, err = p.parseSource(); err != nil {
			return nil, err
		}
	} else {
		p.unscan()
	}

	// Parse required WITH KEY.
	if stmt.TagKeys, err = p.parseTagKeys(); err != nil {
		return nil, err
	}

	// Parse condition: "WHERE EXPR".
	if stmt.Condition, err = p.parseCondition(); err != nil {
		return nil, err
	}

	// Parse sort: "ORDER BY FIELD+".
	if stmt.SortFields, err = p.parseOrderBy(); err != nil {
		return nil, err
	}

	// Parse limit: "LIMIT <n>".
	if stmt.Limit, err = p.parseOptionalTokenAndInt(LIMIT); err != nil {
		return nil, err
	}

	// Parse offset: "OFFSET <n>".
	if stmt.Offset, err = p.parseOptionalTokenAndInt(OFFSET); err != nil {
		return nil, err
	}

	return stmt, nil
}

// parseTagKeys parses a string and returns a list of tag keys.
func (p *Parser) parseTagKeys() ([]string, error) {
	var err error

	// Parse required WITH KEY tokens.
	if err := p.parseTokens([]Token{WITH, KEY}); err != nil {
		return nil, err
	}

	var tagKeys []string

	// Parse required IN or EQ token.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok == IN {
		// Parse required ( token.
		if tok, pos, lit = p.scanIgnoreWhitespace(); tok != LPAREN {
			return nil, newParseError(tokstr(tok, lit), []string{"("}, pos)
		}

		// Parse tag key list.
		if tagKeys, err = p.parseIdentList(); err != nil {
			return nil, err
		}

		// Parse required ) token.
		if tok, pos, lit = p.scanIgnoreWhitespace(); tok != RPAREN {
			return nil, newParseError(tokstr(tok, lit), []string{"("}, pos)
		}
	} else if tok == EQ {
		// Parse required tag key.
		ident, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		tagKeys = append(tagKeys, ident)
	} else {
		return nil, newParseError(tokstr(tok, lit), []string{"IN", "="}, pos)
	}

	return tagKeys, nil
}

// parseShowUsersStatement parses a string and returns a ShowUsersStatement.
// This function assumes the "SHOW USERS" tokens have been consumed.
func (p *Parser) parseShowUsersStatement() (*ShowUsersStatement, error) {
	return &ShowUsersStatement{}, nil
}

// parseShowFieldKeysStatement parses a string and returns a ShowSeriesStatement.
// This function assumes the "SHOW FIELD KEYS" tokens have already been consumed.
func (p *Parser) parseShowFieldKeysStatement() (*ShowFieldKeysStatement, error) {
	stmt := &ShowFieldKeysStatement{}
	var err error

	// Parse optional source.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok == FROM {
		if stmt.Source, err = p.parseSource(); err != nil {
			return nil, err
		}
	} else {
		p.unscan()
	}

	// Parse sort: "ORDER BY FIELD+".
	if stmt.SortFields, err = p.parseOrderBy(); err != nil {
		return nil, err
	}

	// Parse limit: "LIMIT <n>".
	if stmt.Limit, err = p.parseOptionalTokenAndInt(LIMIT); err != nil {
		return nil, err
	}

	// Parse offset: "OFFSET <n>".
	if stmt.Offset, err = p.parseOptionalTokenAndInt(OFFSET); err != nil {
		return nil, err
	}

	return stmt, nil
}

// parseDropMeasurementStatement parses a string and returns a DropMeasurementStatement.
// This function assumes the "DROP MEASUREMENT" tokens have already been consumed.
func (p *Parser) parseDropMeasurementStatement() (*DropMeasurementStatement, error) {
	stmt := &DropMeasurementStatement{}

	// Parse the name of the measurement to be dropped.
	lit, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = lit

	return stmt, nil
}

// parseDropSeriesStatement parses a string and returns a DropSeriesStatement.
// This function assumes the "DROP SERIES" tokens have already been consumed.
func (p *Parser) parseDropSeriesStatement() (*DropSeriesStatement, error) {
	stmt := &DropSeriesStatement{}
	var err error

	if tok, _, _ := p.scanIgnoreWhitespace(); tok == FROM {
		// Parse source.
		if stmt.Source, err = p.parseSource(); err != nil {
			return nil, err
		}
	} else {
		p.unscan()
	}

	// Parse condition: "WHERE EXPR".
	if stmt.Condition, err = p.parseCondition(); err != nil {
		return nil, err
	}

	// If they didn't provide a FROM or a WHERE, they need to provide the SeriesID
	if stmt.Condition == nil && stmt.Source == nil {
		id, err := p.parseUInt32()
		if err != nil {
			return nil, err
		}
		stmt.SeriesID = id
	}
	return stmt, nil
}

// parseShowContinuousQueriesStatement parses a string and returns a ShowContinuousQueriesStatement.
// This function assumes the "SHOW CONTINUOUS" tokens have already been consumed.
func (p *Parser) parseShowContinuousQueriesStatement() (*ShowContinuousQueriesStatement, error) {
	stmt := &ShowContinuousQueriesStatement{}

	// Expect a "QUERIES" token.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != QUERIES {
		return nil, newParseError(tokstr(tok, lit), []string{"QUERIES"}, pos)
	}

	return stmt, nil
}

// parseShowDatabasesStatement parses a string and returns a ShowDatabasesStatement.
// This function assumes the "SHOW DATABASE" tokens have already been consumed.
func (p *Parser) parseShowDatabasesStatement() (*ShowDatabasesStatement, error) {
	stmt := &ShowDatabasesStatement{}
	return stmt, nil
}

// parseCreateContinuousQueriesStatement parses a string and returns a CreateContinuousQueryStatement.
// This function assumes the "CREATE CONTINUOUS" tokens have already been consumed.
func (p *Parser) parseCreateContinuousQueryStatement() (*CreateContinuousQueryStatement, error) {
	stmt := &CreateContinuousQueryStatement{}

	// Expect a "QUERY" token.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != QUERY {
		return nil, newParseError(tokstr(tok, lit), []string{"QUERY"}, pos)
	}

	// Read the id of the query to create.
	ident, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = ident

	// Expect an "ON" keyword.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != ON {
		return nil, newParseError(tokstr(tok, lit), []string{"ON"}, pos)
	}

	// Read the name of the database to create the query on.
	if ident, err = p.parseIdent(); err != nil {
		return nil, err
	}
	stmt.Database = ident

	// Expect a "BEGIN SELECT" tokens.
	if err := p.parseTokens([]Token{BEGIN, SELECT}); err != nil {
		return nil, err
	}

	// Read the select statement to be used as the source.
	source, err := p.parseSelectStatement(targetRequired)
	if err != nil {
		return nil, err
	}
	stmt.Source = source

	// validate that the statement has a non-zero group by interval if it is aggregated
	if source.Aggregated() {
		d, err := source.GroupByInterval()
		if d == 0 || err != nil {
			// rewind so we can output an error with some info
			p.unscan() // unscan the whitespace
			p.unscan() // unscan the last token
			tok, pos, lit := p.scanIgnoreWhitespace()
			expected := []string{"GROUP BY time(...)"}
			if err != nil {
				expected = append(expected, err.Error())
			}
			return nil, newParseError(tokstr(tok, lit), expected, pos)
		}
	}

	// Expect a "END" keyword.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != END {
		return nil, newParseError(tokstr(tok, lit), []string{"END"}, pos)
	}

	return stmt, nil
}

// parseCreateDatabaseStatement parses a string and returns a CreateDatabaseStatement.
// This function assumes the "CREATE DATABASE" tokens have already been consumed.
func (p *Parser) parseCreateDatabaseStatement() (*CreateDatabaseStatement, error) {
	stmt := &CreateDatabaseStatement{}

	// Parse the name of the database to be created.
	lit, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = lit

	return stmt, nil
}

// parseDropDatabaseStatement parses a string and returns a DropDatabaseStatement.
// This function assumes the DROP DATABASE tokens have already been consumed.
func (p *Parser) parseDropDatabaseStatement() (*DropDatabaseStatement, error) {
	stmt := &DropDatabaseStatement{}

	// Parse the name of the database to be dropped.
	lit, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = lit

	return stmt, nil
}

// parseDropRetentionPolicyStatement parses a string and returns a DropRetentionPolicyStatement.
// This function assumes the DROP RETENTION POLICY tokens have been consumed.
func (p *Parser) parseDropRetentionPolicyStatement() (*DropRetentionPolicyStatement, error) {
	stmt := &DropRetentionPolicyStatement{}

	// Parse the policy name.
	ident, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = ident

	// Consume the required ON token.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != ON {
		return nil, newParseError(tokstr(tok, lit), []string{"ON"}, pos)
	}

	// Parse the database name.
	if stmt.Database, err = p.parseIdent(); err != nil {
		return nil, err
	}

	return stmt, nil
}

// parseCreateUserStatement parses a string and returns a CreateUserStatement.
// This function assumes the "CREATE USER" tokens have already been consumed.
func (p *Parser) parseCreateUserStatement() (*CreateUserStatement, error) {
	stmt := &CreateUserStatement{}

	// Parse name of the user to be created.
	ident, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = ident

	// Consume "WITH PASSWORD" tokens
	if err := p.parseTokens([]Token{WITH, PASSWORD}); err != nil {
		return nil, err
	}

	// Parse new user's password
	if ident, err = p.parseString(); err != nil {
		return nil, err
	}
	stmt.Password = ident

	// Check for option WITH clause.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok != WITH {
		p.unscan()
		return stmt, nil
	}

	// We only allow granting of "ALL PRIVILEGES" during CREATE USER.
	// All other privileges must be granted using a GRANT statement.
	if err := p.parseTokens([]Token{ALL, PRIVILEGES}); err != nil {
		return nil, err
	}
	stmt.Privilege = NewPrivilege(AllPrivileges)

	return stmt, nil
}

// parseDropUserStatement parses a string and returns a DropUserStatement.
// This function assumes the DROP USER tokens have already been consumed.
func (p *Parser) parseDropUserStatement() (*DropUserStatement, error) {
	stmt := &DropUserStatement{}

	// Parse the name of the user to be dropped.
	lit, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = lit

	return stmt, nil
}

// parseRetentionPolicy parses a string and returns a retention policy name.
// This function assumes the "WITH" token has already been consumed.
func (p *Parser) parseRetentionPolicy() (name string, dfault bool, err error) {
	// Check for optional DEFAULT token.
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok == DEFAULT {
		dfault = true
		tok, pos, lit = p.scanIgnoreWhitespace()
	}

	// Check for required RETENTION token.
	if tok != RETENTION {
		err = newParseError(tokstr(tok, lit), []string{"RETENTION"}, pos)
		return
	}

	// Check of required POLICY token.
	if tok, pos, lit = p.scanIgnoreWhitespace(); tok != POLICY {
		err = newParseError(tokstr(tok, lit), []string{"POLICY"}, pos)
		return
	}

	// Parse retention policy name.
	name, err = p.parseIdent()
	if err != nil {
		return
	}

	return
}

// parseDropContinuousQueriesStatement parses a string and returns a DropContinuousQueryStatement.
// This function assumes the "DROP CONTINUOUS" tokens have already been consumed.
func (p *Parser) parseDropContinuousQueryStatement() (*DropContinuousQueryStatement, error) {
	stmt := &DropContinuousQueryStatement{}

	// Expect a "QUERY" token.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != QUERY {
		return nil, newParseError(tokstr(tok, lit), []string{"QUERY"}, pos)
	}

	// Read the id of the query to drop.
	lit, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt.Name = lit

	return stmt, nil
}

// parseFields parses a list of one or more fields.
func (p *Parser) parseFields() (Fields, error) {
	var fields Fields

	// Check for "*" (i.e., "all fields")
	if tok, _, _ := p.scanIgnoreWhitespace(); tok == MUL {
		fields = append(fields, &Field{&Wildcard{}, ""})
		return fields, nil
	}
	p.unscan()

	for {
		// Parse the field.
		f, err := p.parseField()
		if err != nil {
			return nil, err
		}

		// Add new field.
		fields = append(fields, f)

		// If there's not a comma next then stop parsing fields.
		if tok, _, _ := p.scan(); tok != COMMA {
			p.unscan()
			break
		}
	}
	return fields, nil
}

// parseField parses a single field.
func (p *Parser) parseField() (*Field, error) {
	f := &Field{}

	// Parse the expression first.
	expr, err := p.ParseExpr()
	if err != nil {
		return nil, err
	}
	f.Expr = expr

	// Parse the alias if the current and next tokens are "WS AS".
	alias, err := p.parseAlias()
	if err != nil {
		return nil, err
	}
	f.Alias = alias

	// Consume all trailing whitespace.
	p.consumeWhitespace()

	return f, nil
}

// parseAlias parses the "AS (IDENT|STRING)" alias for fields and dimensions.
func (p *Parser) parseAlias() (string, error) {
	// Check if the next token is "AS". If not, then unscan and exit.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok != AS {
		p.unscan()
		return "", nil
	}

	// Then we should have the alias identifier.
	lit, err := p.parseIdent()
	if err != nil {
		return "", err
	}
	return lit, nil
}

// parseSource parses the "FROM" clause of the query.
func (p *Parser) parseSource() (Source, error) {
	// The first token can either be the series name or a join/merge call.
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok != IDENT {
		return nil, newParseError(tokstr(tok, lit), []string{"identifier"}, pos)
	}

	// If the token is a string or the next token is not an LPAREN then return a measurement.
	if next, _, _ := p.scan(); tok == STRING || (tok == IDENT && next != LPAREN) {
		p.unscan()
		return &Measurement{Name: lit}, nil
	}

	// Verify the source type is join/merge.
	sourceType := strings.ToLower(lit)
	if sourceType != "join" && sourceType != "merge" {
		return nil, &ParseError{Message: "unknown merge type: " + sourceType, Pos: pos}
	}

	// Parse measurement list.
	var measurements []*Measurement
	for {
		// Scan the measurement name.
		tok, pos, lit := p.scanIgnoreWhitespace()
		if tok != IDENT {
			return nil, newParseError(tokstr(tok, lit), []string{"measurement name"}, pos)
		}
		measurements = append(measurements, &Measurement{Name: lit})

		// If there's not a comma next then stop parsing measurements.
		if tok, _, _ := p.scan(); tok != COMMA {
			p.unscan()
			break
		}
	}

	// Expect a closing right paren.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != RPAREN {
		return nil, newParseError(tokstr(tok, lit), []string{")"}, pos)
	}

	// Return the appropriate source type.
	if sourceType == "join" {
		return &Join{Measurements: measurements}, nil
	}
	return &Merge{Measurements: measurements}, nil
}

// parseCondition parses the "WHERE" clause of the query, if it exists.
func (p *Parser) parseCondition() (Expr, error) {
	// Check if the WHERE token exists.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok != WHERE {
		p.unscan()
		return nil, nil
	}

	// Scan the identifier for the source.
	expr, err := p.ParseExpr()
	if err != nil {
		return nil, err
	}

	return expr, nil
}

// parseDimensions parses the "GROUP BY" clause of the query, if it exists.
func (p *Parser) parseDimensions() (Dimensions, error) {
	// If the next token is not GROUP then exit.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok != GROUP {
		p.unscan()
		return nil, nil
	}

	// Now the next token should be "BY".
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != BY {
		return nil, newParseError(tokstr(tok, lit), []string{"BY"}, pos)
	}

	var dimensions Dimensions
	for {
		// Parse the dimension.
		d, err := p.parseDimension()
		if err != nil {
			return nil, err
		}

		// Add new dimension.
		dimensions = append(dimensions, d)

		// If there's not a comma next then stop parsing dimensions.
		if tok, _, _ := p.scan(); tok != COMMA {
			p.unscan()
			break
		}
	}
	return dimensions, nil
}

// parseDimension parses a single dimension.
func (p *Parser) parseDimension() (*Dimension, error) {
	// Parse the expression first.
	expr, err := p.ParseExpr()
	if err != nil {
		return nil, err
	}

	// Consume all trailing whitespace.
	p.consumeWhitespace()

	return &Dimension{Expr: expr}, nil
}

// parseOptionalTokenAndInt parses the specified token followed
// by an int, if it exists.
func (p *Parser) parseOptionalTokenAndInt(t Token) (int, error) {
	// Check if the token exists.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok != t {
		p.unscan()
		return 0, nil
	}

	// Scan the number.
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok != NUMBER {
		return 0, newParseError(tokstr(tok, lit), []string{"number"}, pos)
	}

	// Return an error if the number has a fractional part.
	if strings.Contains(lit, ".") {
		msg := fmt.Sprintf("fractional parts not allowed in %s", t.String())
		return 0, &ParseError{Message: msg, Pos: pos}
	}

	// Parse number.
	n, _ := strconv.ParseInt(lit, 10, 64)

	if n < 1 {
		msg := fmt.Sprintf("%s must be > 0", t.String())
		return 0, &ParseError{Message: msg, Pos: pos}
	}

	return int(n), nil
}

// parseOrderBy parses the "ORDER BY" clause of a query, if it exists.
func (p *Parser) parseOrderBy() (SortFields, error) {
	// Return nil result and nil error if no ORDER token at this position.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok != ORDER {
		p.unscan()
		return nil, nil
	}

	// Parse the required BY token.
	if tok, pos, lit := p.scanIgnoreWhitespace(); tok != BY {
		return nil, newParseError(tokstr(tok, lit), []string{"BY"}, pos)
	}

	// Parse the ORDER BY fields.
	fields, err := p.parseSortFields()
	if err != nil {
		return nil, err
	}

	return fields, nil
}

// parseSortFields parses all fields of and ORDER BY clause.
func (p *Parser) parseSortFields() (SortFields, error) {
	var fields SortFields

	// At least one field is required.
	field, err := p.parseSortField()
	if err != nil {
		return nil, err
	}
	fields = append(fields, field)

	// Parse additional fields.
	for {
		tok, _, _ := p.scanIgnoreWhitespace()

		if tok != COMMA {
			p.unscan()
			break
		}

		field, err := p.parseSortField()
		if err != nil {
			return nil, err
		}

		fields = append(fields, field)
	}

	return fields, nil
}

// parseSortField parses one field of an ORDER BY clause.
func (p *Parser) parseSortField() (*SortField, error) {
	field := &SortField{}

	// Next token should be ASC, DESC, or IDENT | STRING.
	tok, pos, lit := p.scanIgnoreWhitespace()
	if tok == IDENT || tok == STRING {
		field.Name = lit
		// Check for optional ASC or DESC token.
		tok, pos, lit = p.scanIgnoreWhitespace()
		if tok != ASC && tok != DESC {
			p.unscan()
			return field, nil
		}
	} else if tok != ASC && tok != DESC {
		return nil, newParseError(tokstr(tok, lit), []string{"identifier, ASC, or DESC"}, pos)
	}

	field.Ascending = (tok == ASC)

	return field, nil
}

// ParseExpr parses an expression.
func (p *Parser) ParseExpr() (Expr, error) {
	// Parse a non-binary expression type to start.
	// This variable will always be the root of the expression tree.
	expr, err := p.parseUnaryExpr()
	if err != nil {
		return nil, err
	}

	// Loop over operations and unary exprs and build a tree based on precendence.
	for {
		// If the next token is NOT an operator then return the expression.
		op, _, _ := p.scanIgnoreWhitespace()
		if !op.isOperator() {
			p.unscan()
			return expr, nil
		}

		// Otherwise parse the next unary expression.
		rhs, err := p.parseUnaryExpr()
		if err != nil {
			return nil, err
		}

		// Assign the new root based on the precendence of the LHS and RHS operators.
		if lhs, ok := expr.(*BinaryExpr); ok && lhs.Op.Precedence() < op.Precedence() {
			expr = &BinaryExpr{
				LHS: lhs.LHS,
				RHS: &BinaryExpr{LHS: lhs.RHS, RHS: rhs, Op: op},
				Op:  lhs.Op,
			}
		} else {
			expr = &BinaryExpr{LHS: expr, RHS: rhs, Op: op}
		}
	}
}

// parseUnaryExpr parses an non-binary expression.
func (p *Parser) parseUnaryExpr() (Expr, error) {
	// If the first token is a LPAREN then parse it as its own grouped expression.
	if tok, _, _ := p.scanIgnoreWhitespace(); tok == LPAREN {
		expr, err := p.ParseExpr()
		if err != nil {
			return nil, err
		}

		// Expect an RPAREN at the end.
		if tok, pos, lit := p.scanIgnoreWhitespace(); tok != RPAREN {
			return nil, newParseError(tokstr(tok, lit), []string{")"}, pos)
		}

		return &ParenExpr{Expr: expr}, nil
	}
	p.unscan()

	// Read next token.
	tok, pos, lit := p.scanIgnoreWhitespace()
	switch tok {
	case IDENT:
		// If the next immediate token is a left parentheses, parse as function call.
		// Otherwise parse as a variable reference.
		if tok0, _, _ := p.scan(); tok0 == LPAREN {
			return p.parseCall(lit)
		}
		p.unscan()
		return &VarRef{Val: lit}, nil
	case STRING:
		// If literal looks like a date time then parse it as a time literal.
		if isDateTimeString(lit) {
			t, err := time.Parse(DateTimeFormat, lit)
			if err != nil {
				// try to parse it as an RFCNano time
				t, err := time.Parse(time.RFC3339Nano, lit)
				if err != nil {
					return nil, &ParseError{Message: "unable to parse datetime", Pos: pos}
				}
				return &TimeLiteral{Val: t}, nil
			}
			return &TimeLiteral{Val: t}, nil
		} else if isDateString(lit) {
			t, err := time.Parse(DateFormat, lit)
			if err != nil {
				return nil, &ParseError{Message: "unable to parse date", Pos: pos}
			}
			return &TimeLiteral{Val: t}, nil
		}
		return &StringLiteral{Val: lit}, nil
	case NUMBER:
		v, err := strconv.ParseFloat(lit, 64)
		if err != nil {
			return nil, &ParseError{Message: "unable to parse number", Pos: pos}
		}
		return &NumberLiteral{Val: v}, nil
	case TRUE, FALSE:
		return &BooleanLiteral{Val: (tok == TRUE)}, nil
	case DURATION_VAL:
		v, _ := ParseDuration(lit)
		return &DurationLiteral{Val: v}, nil
	case MUL:
		return &Wildcard{}, nil
	case REGEX:
		re, err := regexp.Compile(lit)
		if err != nil {
			return nil, &ParseError{Message: err.Error(), Pos: pos}
		}
		return &RegexLiteral{Val: re}, nil
	default:
		return nil, newParseError(tokstr(tok, lit), []string{"identifier", "string", "number", "bool"}, pos)
	}
}

// parseCall parses a function call.
// This function assumes the function name and LPAREN have been consumed.
func (p *Parser) parseCall(name string) (*Call, error) {
	// If there's a right paren then just return immediately.
	if tok, _, _ := p.scan(); tok == RPAREN {
		return &Call{Name: name}, nil
	}
	p.unscan()

	// Otherwise parse function call arguments.
	var args []Expr
	for {
		// Parse an expression argument.
		arg, err := p.ParseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)

		// If there's not a comma next then stop parsing arguments.
		if tok, _, _ := p.scan(); tok != COMMA {
			p.unscan()
			break
		}
	}

	// There should be a right parentheses at the end.
	if tok, pos, lit := p.scan(); tok != RPAREN {
		return nil, newParseError(tokstr(tok, lit), []string{")"}, pos)
	}

	return &Call{Name: name, Args: args}, nil
}

// scan returns the next token from the underlying scanner.
func (p *Parser) scan() (tok Token, pos Pos, lit string) { return p.s.Scan() }

// scanIgnoreWhitespace scans the next non-whitespace token.
func (p *Parser) scanIgnoreWhitespace() (tok Token, pos Pos, lit string) {
	tok, pos, lit = p.scan()
	if tok == WS {
		tok, pos, lit = p.scan()
	}
	return
}

// consumeWhitespace scans the next token if it's whitespace.
func (p *Parser) consumeWhitespace() {
	if tok, _, _ := p.scan(); tok != WS {
		p.unscan()
	}
}

// unscan pushes the previously read token back onto the buffer.
func (p *Parser) unscan() { p.s.Unscan() }

// ParseDuration parses a time duration from a string.
func ParseDuration(s string) (time.Duration, error) {
	// Return an error if the string is blank.
	if len(s) == 0 {
		return 0, ErrInvalidDuration
	}

	// If there's only character then it must be a digit (in microseconds).
	if len(s) == 1 {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Duration(n) * time.Microsecond, nil
		}
		return 0, ErrInvalidDuration
	}

	// Split string into individual runes.
	a := split(s)

	// Extract the unit of measure.
	// If the last character is a digit then parse the whole string as microseconds.
	// If the last two characters are "ms" the parse as milliseconds.
	// Otherwise just use the last character as the unit of measure.
	var num, uom string
	if isDigit(rune(a[len(a)-1])) {
		num, uom = s, "u"
	} else if len(s) > 2 && s[len(s)-2:] == "ms" {
		num, uom = string(a[:len(a)-2]), "ms"
	} else {
		num, uom = string(a[:len(a)-1]), string(a[len(a)-1:])
	}

	// Parse the numeric part.
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, ErrInvalidDuration
	}

	// Multiply by the unit of measure.
	switch uom {
	case "u", "µ":
		return time.Duration(n) * time.Microsecond, nil
	case "ms":
		return time.Duration(n) * time.Millisecond, nil
	case "s":
		return time.Duration(n) * time.Second, nil
	case "m":
		return time.Duration(n) * time.Minute, nil
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	case "w":
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	default:
		return 0, ErrInvalidDuration
	}
}

// FormatDuration formats a duration to a string.
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	} else if d%(7*24*time.Hour) == 0 {
		return fmt.Sprintf("%dw", d/(7*24*time.Hour))
	} else if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	} else if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	} else if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	} else if d%time.Second == 0 {
		return fmt.Sprintf("%ds", d/time.Second)
	} else if d%time.Millisecond == 0 {
		return fmt.Sprintf("%dms", d/time.Millisecond)
	}
	return fmt.Sprintf("%d", d/time.Microsecond)
}

// parseTokens consumes an expected sequence of tokens.
func (p *Parser) parseTokens(toks []Token) error {
	for _, expected := range toks {
		if tok, pos, lit := p.scanIgnoreWhitespace(); tok != expected {
			return newParseError(tokstr(tok, lit), []string{tokens[expected]}, pos)
		}
	}
	return nil
}

// QuoteString returns a quoted string.
func QuoteString(s string) string {
	return `'` + strings.NewReplacer("\n", `\n`, `\`, `\\`, `'`, `\'`).Replace(s) + `'`
}

// QuoteIdent returns a quoted identifier from multiple bare identifiers.
func QuoteIdent(segments []string) string {
	r := strings.NewReplacer("\n", `\n`, `\`, `\\`, `"`, `\"`)

	var buf bytes.Buffer
	for i, segment := range segments {
		_ = buf.WriteByte('"')
		_, _ = buf.WriteString(r.Replace(segment))
		_ = buf.WriteByte('"')
		if i < len(segments)-1 {
			_ = buf.WriteByte('.')
		}
	}
	return buf.String()
}

// split splits a string into a slice of runes.
func split(s string) (a []rune) {
	for _, ch := range s {
		a = append(a, ch)
	}
	return
}

// isDateString returns true if the string looks like a date-only time literal.
func isDateString(s string) bool { return dateStringRegexp.MatchString(s) }

// isDateTimeString returns true if the string looks like a date+time time literal.
func isDateTimeString(s string) bool { return dateTimeStringRegexp.MatchString(s) }

var dateStringRegexp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var dateTimeStringRegexp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}.+`)

// ErrInvalidDuration is returned when parsing a malformatted duration.
var ErrInvalidDuration = errors.New("invalid duration")

// ParseError represents an error that occurred during parsing.
type ParseError struct {
	Message  string
	Found    string
	Expected []string
	Pos      Pos
}

// newParseError returns a new instance of ParseError.
func newParseError(found string, expected []string, pos Pos) *ParseError {
	return &ParseError{Found: found, Expected: expected, Pos: pos}
}

// Error returns the string representation of the error.
func (e *ParseError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s at line %d, char %d", e.Message, e.Pos.Line+1, e.Pos.Char+1)
	}
	return fmt.Sprintf("found %s, expected %s at line %d, char %d", e.Found, strings.Join(e.Expected, ", "), e.Pos.Line+1, e.Pos.Char+1)
}

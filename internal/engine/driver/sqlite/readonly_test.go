package sqlite

// White-box (package sqlite) on purpose: statementKeywords is the guard's
// whole defence against a lexical evasion, and the cases below are the ones
// that cannot be driven through Conn.Query — an unterminated quote or block
// comment is a syntax error SQLite rejects for its own reasons, so a test
// going through Query would pass whether the tokenizer got them right or
// not. What matters here is that the tokenizer never UNDER-counts
// statements: every one of these inputs must still read as one statement, so
// that the same text with a `; CREATE TABLE …` appended reads as two.
//
// The attacks that can be driven through a real connection are in
// sqlite_test.go, where they belong.

import (
	"reflect"
	"testing"
)

func TestStatementKeywordsKeepsQuotingAndCommentsWhole(t *testing.T) {
	cases := map[string]struct {
		sql  string
		want []string
	}{
		"nothing at all":                       {"", nil},
		"only whitespace":                      {"   \n\t ", nil},
		"only a comment":                       {"-- nothing here", nil},
		"empty statements":                     {";;;", nil},
		"trailing semicolon":                   {"SELECT 1;", []string{"select"}},
		"two statements":                       {"SELECT 1; PRAGMA query_only=0", []string{"select", "pragma"}},
		"semicolon in a literal":               {"SELECT ';'", []string{"select"}},
		"doubled quote in a literal":           {"SELECT 'it''s; fine'", []string{"select"}},
		"unterminated literal":                 {"SELECT 'oops; PRAGMA query_only=0", []string{"select"}},
		"semicolon in an identifier":           {`SELECT "a;b" FROM t`, []string{"select"}},
		"semicolon in a backquoted identifier": {"SELECT `a;b` FROM t", []string{"select"}},
		"semicolon in a bracketed identifier":  {"SELECT [a;b] FROM t", []string{"select"}},
		"unterminated bracket":                 {"SELECT [a;b", []string{"select"}},
		"unterminated block comment":           {"SELECT 1 /* ; PRAGMA query_only=0", []string{"select"}},
		"comment splits a word":                {"PRAG/*x*/MA", []string{"prag"}},
		"statement opening with punctuation":   {"(SELECT 1)", []string{"select"}},
		"minus that is not a comment":          {"SELECT 1-1", []string{"select"}},
		"slash that is not a comment":          {"SELECT 4/2", []string{"select"}},
		"non-ASCII identifier":                 {"SELECT ünïcode FROM t", []string{"select"}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := statementKeywords(tc.sql); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("statementKeywords(%q) = %#v, want %#v", tc.sql, got, tc.want)
			}
		})
	}
}

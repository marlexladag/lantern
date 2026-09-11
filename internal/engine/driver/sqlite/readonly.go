package sqlite

import (
	"strings"

	"github.com/marlexladag/lantern/internal/engine/dberr"
)

// guardReadOnly is the second half of this driver's read-only enforcement,
// in front of the PRAGMA query_only that dsn sets.
//
// query_only is enforced by SQLite against statements, and `PRAGMA
// query_only=0` is a statement: a read-only connection that will run
// arbitrary SQL can be told to stop being read-only, and the cleared flag
// then persists for the whole life of that pooled connection. Worse, one
// call is enough — modernc.org/sqlite runs every statement in the string it
// is handed, so `PRAGMA query_only=0; CREATE TABLE t (…)` both clears the
// flag and takes the write before returning. Verified against the real
// driver, not reasoned about: see
// TestReadOnlyRefusesAPragmaAndAWriteSmuggledIntoOneCall.
//
// So the guard refuses two statement shapes on a read-only connection:
//
//   - Any PRAGMA. Not only an assigning one: `PRAGMA name(arg)` is the same
//     syntax whether arg is a setting being written or an argument being
//     read, so telling the two apart needs a list of which pragma names take
//     a setting — a per-SQLite-version list whose omissions would be silent
//     holes. Refusing all of them costs a user the ability to type an
//     introspection pragma into a read-only query tab; this driver's own
//     introspection (Tables, Columns) issues its queries against c.db
//     directly and is unaffected. A future caller that routes user SQL
//     through anything but Conn.Query reopens the hole this closes.
//   - More than one statement in a single call, which is how a write rides
//     along behind an innocuous read.
//
// What this is NOT: a boundary against someone who can author arbitrary SQL.
// modernc.org/sqlite@v1.39.0 exports no authorizer — sqlite3_set_authorizer
// is compiled into the vendored amalgamation but the raw connection handle
// is never exposed, so there is no hook to build a real one from — and a
// tokenizer-based guard is only ever as good as its author's imagination.
// It is a guard against the realistic failure: an UPDATE, a DELETE or a
// paste that clears query_only, run by accident against a connection the
// user marked production. Spec section 4 says exactly this, and must keep
// saying it.
func guardReadOnly(sql string) error {
	keywords := statementKeywords(sql)
	if len(keywords) > 1 {
		return dberr.New(dberr.KindReadOnly,
			"a read-only connection runs one statement at a time").WithQuery(sql)
	}
	if len(keywords) == 1 && keywords[0] == "pragma" {
		return dberr.New(dberr.KindReadOnly,
			"a read-only connection does not run PRAGMA statements").WithQuery(sql)
	}
	return nil
}

// statementKeywords splits sql at every semicolon that is not inside a
// comment, a string literal or a quoted identifier, and returns the leading
// word of each statement it finds — lowercased, and empty for a statement
// that opens with something other than a word.
//
// It is a tokenizer rather than a set of string matches because every
// evasion worth the name is a lexical one: case, whitespace, a comment
// wedged between PRAGMA and its argument, a semicolon inside a string
// literal that a naive split would read as a statement boundary. Each of
// those is an attack in TestReadOnlyRefusesEveryPragmaSpelling or a read in
// TestReadOnlyStillAllowsOrdinaryReads.
//
// Text that is only whitespace and comments yields no statement, so a
// trailing semicolon or a trailing comment does not read as a second one —
// the difference between refusing `SELECT 1;` and refusing a smuggled
// write.
//
// Byte-wise, not rune-wise, on purpose: every byte it dispatches on is
// ASCII, and every byte of a multi-byte UTF-8 rune is >= 0x80, so a rune can
// never be mistaken for a quote, a comment marker or a semicolon. isWordByte
// treats those continuation bytes as word bytes, which keeps an identifier
// written in a non-Latin script one word rather than several.
func statementKeywords(sql string) []string {
	var out []string
	word, hasContent, wordClosed := "", false, false
	// A word ends at the first byte that cannot be part of it. Called from
	// every branch that is not itself a word byte, including the comment
	// branches — `PRAGMA/*x*/query_only` is two words, not one.
	closeWord := func() { wordClosed = wordClosed || word != "" }

	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == ';':
			if hasContent {
				out = append(out, word)
			}
			word, hasContent, wordClosed = "", false, false
			i++
		case isSpaceByte(c):
			closeWord()
			i++
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			closeWord()
			i = skipLineComment(sql, i)
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			closeWord()
			i = skipBlockComment(sql, i)
		case c == '\'' || c == '"' || c == '`' || c == '[':
			closeWord()
			hasContent = true
			i = skipQuoted(sql, i)
		case isWordByte(c):
			hasContent = true
			if !wordClosed {
				word += string(lowerByte(c))
			}
			i++
		default:
			closeWord()
			hasContent = true
			i++
		}
	}
	if hasContent {
		out = append(out, word)
	}
	return out
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// isWordByte reports whether c can appear in an unquoted SQLite identifier
// or keyword. `$` is in the set because SQLite accepts it in identifiers,
// and every byte >= 0x80 is because they are UTF-8 continuation and lead
// bytes (see statementKeywords' doc comment).
func isWordByte(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		c >= 0x80
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// skipLineComment returns the index just past a `--` comment. An unterminated
// one runs to the end of the input, which is what SQLite's own tokenizer
// does with it.
func skipLineComment(sql string, i int) int {
	if n := strings.IndexByte(sql[i:], '\n'); n >= 0 {
		return i + n + 1
	}
	return len(sql)
}

// skipBlockComment returns the index just past a `/* */` comment. SQLite
// accepts an unterminated one as running to the end of the input rather
// than rejecting it, so this does the same — reading it as ordinary text
// instead would let `SELECT 1 /*; PRAGMA query_only=0` split into two
// statements here and one there.
func skipBlockComment(sql string, i int) int {
	if n := strings.Index(sql[i+2:], "*/"); n >= 0 {
		return i + 2 + n + 2
	}
	return len(sql)
}

// skipQuoted returns the index just past the quoted run starting at i.
// SQLite closes ' " and ` with their own character, doubled to escape one
// inside the run, and closes [ with ] with no escape at all. An unterminated
// run swallows the rest of the input for the same reason skipBlockComment
// does.
func skipQuoted(sql string, i int) int {
	if sql[i] == '[' {
		if n := strings.IndexByte(sql[i+1:], ']'); n >= 0 {
			return i + 1 + n + 1
		}
		return len(sql)
	}
	q := sql[i]
	for j := i + 1; j < len(sql); j++ {
		if sql[j] != q {
			continue
		}
		if j+1 < len(sql) && sql[j+1] == q {
			j++
			continue
		}
		return j + 1
	}
	return len(sql)
}

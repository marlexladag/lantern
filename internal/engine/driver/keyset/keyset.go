// Package keyset renders the dialect-neutral half of keyset pagination.
//
// Everything here is shared by every SQL engine this project will ever
// speak to: the lexicographic "strictly after this row" chain, the ORDER BY
// it must agree with, and the token that names the ordering a cursor was
// issued under. None of it quotes an identifier or screens a value — those
// are the two things engines genuinely disagree about, so the driver renders
// its own identifiers into Term.Expr and passes its own Bind in.
//
// It lives here rather than in one driver because the alternative is a
// second driver copy-pasting it, and the review that checked this chain
// returns every row exactly once was a check of THIS code, not of a copy.
package keyset

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
)

// Term is one ORDER BY term with its column already rendered as SQL by the
// driver, so this package never quotes an identifier and never needs to know
// how a given engine does it.
type Term struct {
	Expr string
	Desc bool
}

// Bind turns one cursor value into a bound parameter. It is a function rather
// than an interface because exactly one behaviour varies between engines —
// which values a cursor can carry — and a one-method interface with one
// implementation is a hypothesis, not a design.
type Bind func(driver.Value) (any, error)

// Token fingerprints the ordering a page was actually produced under, so
// a caller's cursor can be checked against the sort that is CURRENTLY being
// requested and not just trusted to still apply.
//
// It is built from order — the RESOLVED ordering the driver's planner returns,
// complete with whatever tiebreaker it appended — rather than from req.Sort.
// req.Sort is empty on the ordinary default-sort path, so hashing it directly
// would produce the same token for every table's default sort; the resolved
// ordering is the thing that actually determines what "after" means for a
// given cursor, and it is what has to match.
//
// table is folded in for the same reason: two tables can share a column
// name and produce identical order terms, and a token that could not tell
// them apart would let a cursor from one page the other.
//
// database is folded in for the same reason as table, and comes from the
// REQUEST rather than from a driver's own single-database constant. The two
// are equivalent for SQLite, which has exactly one database — but a driver
// for an engine that has many would otherwise hash every schema's tables
// identically, and two same-named tables in different schemas would accept
// each other's cursors. The defect would be introduced by the second driver
// and invisible in the first.
//
// crypto/sha256 is used here only for its collision resistance across the
// handful of orderings one table can produce — this is a consistency check
// against an accidental mismatch (the caller's own stale cursor), not a
// security boundary, so the digest is truncated: nothing is lost by a
// shorter token that a legitimate caller could still not have predicted, and
// nothing would be gained by a longer one that only an adversary deliberately
// searching for a collision would care about.
//
// Every field is LENGTH-PREFIXED rather than separated by a byte, so the
// encoding is injective for any field content whatsoever. A separator has
// to argue that it cannot appear inside a field, and the argument this
// function used to make was both wrong and load-bearing: it claimed the
// table name is "only ever hashed, never executed", when the SQLite driver
// quotes it into the page query and into the rowid probe. The separator
// scheme was sound only because SQLite refuses a NUL in an identifier —
// executed, table "a" sorted by a column "b" hashed identically to a table
// named "a\0b\0a" with no sort — and that premise is exactly what a MySQL
// or Postgres copy of this function would have inherited without rechecking.
// A length prefix needs no premise.
func Token(database, table string, order []Term) string {
	var b strings.Builder
	// Folded on the way in, not at the call site, so every caller of this
	// function gets the case-insensitivity the engine itself applies.
	hashField(&b, FoldIdent(database))
	hashField(&b, FoldIdent(table))
	for _, t := range order {
		// Expr is not folded: it is the driver's own quoting applied to the
		// CATALOG's spelling of the column, or one of a driver's fixed
		// tiebreaker spellings, so it is already canonical however the caller
		// spelled the column.
		hashField(&b, t.Expr)
		if t.Desc {
			hashField(&b, "desc")
		} else {
			hashField(&b, "asc")
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// hashField writes one field of a token's input as its byte length, a
// colon, and then the field. Reading it back is unambiguous — the length
// says exactly how far the field runs — so no two different field sequences
// can produce the same bytes, whatever the fields contain.
func hashField(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}

// FoldIdent renders an identifier in the case the engine compares it in.
//
// ASCII only, which is SQLite's own rule: its built-in identifier
// comparison folds A-Z and leaves every other byte alone, so "Ä" and "ä"
// name DIFFERENT tables there. strings.ToLower would fold them together and
// let one table's cursor page the other — the opposite of the false
// refusal this exists to fix, and a worse one, since it corrupts rather
// than refuses.
func FoldIdent(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// Predicate renders "strictly after the row this cursor names", in the
// ordering order describes.
//
// It is an explicit lexicographic chain rather than SQL's row-value
// comparison, `(a, b) > (?, ?)`, which is the obvious way to write it and is
// wrong here. A row value comparison yields NULL, not true or false, as soon
// as it meets a NULL it cannot decide on — so WHERE drops the row, and a
// sort on a nullable column loses every row past the first page boundary
// that lands in the NULLs. Verified rather than assumed: on a table whose
// sort column is entirely NULL, `WHERE (note, id) > (NULL, 0)` matches zero
// of three rows. The chain below says what row values cannot, that NULL
// sorts before every value ascending and after every value descending, which
// is exactly how SQLite's own ORDER BY treats it.
//
// The chain is the standard one — after the first key, or tied on it and
// after the second, or tied on both and after the third — which costs
// O(n²) terms in the number of sort keys. n is the caller's sort plus one
// tiebreaker, so it is two or three in practice.
func Predicate(order []Term, after []driver.Value, bind Bind) (string, []any, error) {
	// "?" is SQLite's and MySQL's placeholder. PostgreSQL needs $1, $2, ...
	// which is positional and so cannot be a simple string swap — the
	// predicate would have to know each parameter's index. Named here so the
	// first PostgreSQL implementer reads it before starting rather than after.
	clauses := make([]string, len(order))
	var args []any
	for i := range order {
		terms := make([]string, 0, i+1)
		for j := 0; j < i; j++ {
			term, arg := equalTerm(order[j], after[j], bind)
			terms, args = append(terms, term), appendArg(args, arg)
		}
		term, arg, err := afterTerm(order[i], after[i], bind)
		if err != nil {
			return "", nil, err
		}
		terms, args = append(terms, term), appendArg(args, arg)
		clauses[i] = "(" + strings.Join(terms, " AND ") + ")"
	}
	return strings.Join(clauses, " OR "), args, nil
}

// equalTerm renders "this column holds exactly the cursor's value".
//
// It cannot fail: every value reaching it was already accepted by afterTerm
// for an earlier position in this same cursor.
func equalTerm(t Term, v driver.Value, bind Bind) (string, any) {
	if v.Kind == driver.ValueNull {
		return t.Expr + " IS NULL", nil
	}
	arg, _ := bind(v)
	return t.Expr + " = ?", arg
}

// afterTerm renders "this column sorts strictly after the cursor's value",
// in the direction this term is ordered.
func afterTerm(t Term, v driver.Value, bind Bind) (string, any, error) {
	// NULL ORDERING IS ASSUMED HERE, NOT DECLARED. This code places NULLs
	// FIRST ascending and LAST descending, which is what SQLite and MySQL both
	// do. PostgreSQL is the opposite — NULLS LAST ascending by default — so
	// the first PostgreSQL driver must not reuse these two branches unchanged.
	// No seam exists for a dialect to declare it, deliberately: SQLite and
	// MySQL would fill that seam identically, so it would ship untested, and
	// an untested abstraction is how this project got an interface with one
	// implementation calling itself a design. Add the seam when a driver
	// actually disagrees.
	if v.Kind == driver.ValueNull {
		if t.Desc {
			// Descending, NULL sorts last, so no row follows it on this term
			// alone. The chain's later clauses still carry the rows tied at
			// NULL, matched by equalTerm's IS NULL and separated by the
			// tiebreaker.
			return "0", nil, nil
		}
		return t.Expr + " IS NOT NULL", nil, nil
	}
	arg, err := bind(v)
	if err != nil {
		return "", nil, err
	}
	if t.Desc {
		// OR IS NULL because descending puts the NULLs after every value, and
		// `col < ?` alone evaluates to NULL for them and drops them.
		return "(" + t.Expr + " < ? OR " + t.Expr + " IS NULL)", arg, nil
	}
	return t.Expr + " > ?", arg, nil
}

// appendArg keeps the bound parameters in step with the placeholders: a term
// built for a NULL has none.
func appendArg(args []any, arg any) []any {
	if arg == nil {
		return args
	}
	return append(args, arg)
}

// OrderBy renders the ORDER BY the predicate above is built to agree with.
func OrderBy(order []Term) string {
	if len(order) == 0 {
		return ""
	}
	parts := make([]string, len(order))
	for i, t := range order {
		parts[i] = t.Expr + " ASC"
		if t.Desc {
			parts[i] = t.Expr + " DESC"
		}
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

// ColumnNamed finds the column a caller named, folded the way the engine
// compares identifiers — which is FoldIdent's rule, not strings.EqualFold's.
//
// The difference is the one FoldIdent's own doc comment argues, applied here
// because this is where it bites. EqualFold applies Unicode simple case
// folding, which merges "s" with U+017F LATIN SMALL LETTER LONG S; SQLite
// folds A-Z and leaves every other byte alone, so a table may hold both
// columns at once. Folding them together does not produce a false refusal,
// which would at least be loud — it returns the OTHER column, and the caller
// gets a page sorted by a column it did not name, with no error. Verified:
// on a table with both, sorting by either name returned the same order.
//
// A driver whose engine folds identifiers differently must not assume this
// rule. It is deliberately the stricter one: refusing a name this engine
// would have resolved is recoverable, answering about the wrong object is
// not.
func ColumnNamed(cols []schema.Column, name string) (schema.Column, bool) {
	folded := FoldIdent(name)
	for _, col := range cols {
		if FoldIdent(col.Name) == folded {
			return col, true
		}
	}
	return schema.Column{}, false
}

// PrimaryKey returns the columns a table's primary key is made of, in
// catalog order.
func PrimaryKey(cols []schema.Column) []schema.Column {
	var pk []schema.Column
	for _, col := range cols {
		if col.PrimaryKey {
			pk = append(pk, col)
		}
	}
	return pk
}

// HasTerm reports whether order already sorts by expr, compared as RENDERED
// SQL rather than as a column name: that is the form a driver has in hand
// when it is deciding whether appending a tiebreaker would duplicate a term.
func HasTerm(order []Term, expr string) bool {
	for _, t := range order {
		if t.Expr == expr {
			return true
		}
	}
	return false
}

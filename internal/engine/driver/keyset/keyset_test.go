package keyset

import (
	"errors"
	"strings"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
)

func bindText(v driver.Value) (any, error) { return v.Text, nil }

func TestPredicateChainsEqualityBeforeTheStrictTerm(t *testing.T) {
	order := []Term{{Expr: `"a"`}, {Expr: `"b"`}}
	after := []driver.Value{{Kind: driver.ValueText, Text: "x"}, {Kind: driver.ValueInt, Text: "7"}}
	sql, args, err := Predicate(order, after, bindText)
	if err != nil {
		t.Fatalf("predicate: %v", err)
	}
	want := `("a" > ?) OR ("a" = ? AND "b" > ?)`
	if sql != want {
		t.Errorf("sql  = %s\nwant = %s", sql, want)
	}
	if len(args) != 3 {
		t.Errorf("args = %v, want three", args)
	}
}

// Adversarial: a NULL binds no parameter, so the placeholders and the args
// must stay in step. An off-by-one here produces a silently wrong page rather
// than an error.
func TestPredicateBindsNoParameterForANull(t *testing.T) {
	order := []Term{{Expr: `"a"`}, {Expr: `"b"`}}
	after := []driver.Value{{Kind: driver.ValueNull}, {Kind: driver.ValueInt, Text: "7"}}
	sql, args, err := Predicate(order, after, bindText)
	if err != nil {
		t.Fatalf("predicate: %v", err)
	}
	if got, want := strings.Count(sql, "?"), len(args); got != want {
		t.Errorf("%d placeholders but %d args: %s", got, want, sql)
	}
	if !strings.Contains(sql, `"a" IS NOT NULL`) {
		t.Errorf("ascending NULL did not render IS NOT NULL: %s", sql)
	}
}

func TestPredicateDescendingCarriesNullsAfterEveryValue(t *testing.T) {
	order := []Term{{Expr: `"a"`, Desc: true}}
	after := []driver.Value{{Kind: driver.ValueText, Text: "m"}}
	sql, _, err := Predicate(order, after, bindText)
	if err != nil {
		t.Fatalf("predicate: %v", err)
	}
	if !strings.Contains(sql, "IS NULL") {
		t.Errorf("descending term dropped its NULLs: %s", sql)
	}
}

func TestTokenDiffersByDirection(t *testing.T) {
	asc := Token("main", "t", []Term{{Expr: `"v"`}})
	desc := Token("main", "t", []Term{{Expr: `"v"`, Desc: true}})
	if asc == desc {
		t.Error("ascending and descending share a token; a cursor from one would page the other")
	}
}

func TestTokenDiffersByDatabase(t *testing.T) {
	if Token("one", "t", []Term{{Expr: `"v"`}}) == Token("two", "t", []Term{{Expr: `"v"`}}) {
		t.Error("same-named tables in different databases share a token")
	}
}

// Adversarial: the length-prefixed field encoding exists so no two different
// field sequences can produce the same bytes. Prove it on inputs designed to
// collide under a naive separator.
func TestTokenIsUnambiguousAcrossFieldBoundaries(t *testing.T) {
	a := Token("t", `"v"`, []Term{{Expr: "x"}})
	b := Token("t", "", []Term{{Expr: `"v"`}, {Expr: "x"}})
	if a == b {
		t.Error("two different field sequences produced one token")
	}
}

// The same property against an input built to collide EXACTLY, rather than
// merely to look like it should. The separator scheme this replaced wrote
// db, table, then expr and direction per term, all NUL-joined — so a table
// literally named "a\0b\0asc" with no sort encodes to the same bytes as
// table "a" sorted ascending by a column "b". The pre-existing version of
// this check (in the SQLite suite, where this function used to live) is one
// field short of that: its table name stops before the direction, so the
// two encodings differ by an "asc" and it passes under a plain separator
// too. Length-prefixing needs no argument about what a field may contain,
// and this is the input that shows the difference.
func TestTokenCannotBeCollidedByMovingASeparator(t *testing.T) {
	sorted := Token("main", "a", []Term{{Expr: "b"}})
	named := Token("main", "a\x00b\x00asc", nil)
	if sorted == named {
		t.Errorf("two different (table, sort) pairs hash to the same token %q", sorted)
	}
}

// -- the branches the six tests above do not reach -----------------------

// Descending, a NULL cursor value is the last row in the ordering on this
// term alone, so the term is unsatisfiable AND binds nothing. Both halves
// matter: a term that bound a parameter here would leave the args one ahead
// of the placeholders for every later clause in the chain.
func TestPredicateDescendingNullEndsTheTermAndBindsNothing(t *testing.T) {
	order := []Term{{Expr: `"a"`, Desc: true}, {Expr: `"b"`, Desc: true}}
	after := []driver.Value{{Kind: driver.ValueNull}, {Kind: driver.ValueInt, Text: "7"}}
	sql, args, err := Predicate(order, after, bindText)
	if err != nil {
		t.Fatalf("predicate: %v", err)
	}
	if !strings.Contains(sql, "(0)") {
		t.Errorf("descending NULL did not render an unsatisfiable term: %s", sql)
	}
	if got, want := strings.Count(sql, "?"), len(args); got != want {
		t.Errorf("%d placeholders but %d args: %s", got, want, sql)
	}
}

// A cursor value the driver refuses to bind stops the whole predicate: a
// partial chain would page from a boundary nothing described.
func TestPredicateReportsWhatTheDriverRefusesToBind(t *testing.T) {
	refused := errors.New("this cursor cannot be paged by key")
	bind := func(driver.Value) (any, error) { return nil, refused }
	_, _, err := Predicate([]Term{{Expr: `"a"`}},
		[]driver.Value{{Kind: driver.ValueBytes, Text: "3 bytes"}}, bind)
	if !errors.Is(err, refused) {
		t.Errorf("err = %v, want the bind's own error", err)
	}
}

func TestOrderByIsEmptyWithoutTerms(t *testing.T) {
	if got := OrderBy(nil); got != "" {
		t.Errorf("OrderBy(nil) = %q, want empty", got)
	}
}

func TestOrderByNamesEveryTermsDirection(t *testing.T) {
	got := OrderBy([]Term{{Expr: `"a"`}, {Expr: `"b"`, Desc: true}})
	want := ` ORDER BY "a" ASC, "b" DESC`
	if got != want {
		t.Errorf("OrderBy = %q, want %q", got, want)
	}
}

func TestColumnNamedMatchesCaseInsensitively(t *testing.T) {
	cols := []schema.Column{{Name: "Email"}, {Name: "id"}}
	col, ok := ColumnNamed(cols, "EMAIL")
	if !ok || col.Name != "Email" {
		t.Errorf("ColumnNamed = %+v, %v; want the Email column", col, ok)
	}
	if _, ok := ColumnNamed(cols, "absent"); ok {
		t.Error("a column that is not there was found")
	}
}

func TestPrimaryKeyReturnsOnlyTheFlaggedColumns(t *testing.T) {
	cols := []schema.Column{{Name: "a", PrimaryKey: true}, {Name: "b"}, {Name: "c", PrimaryKey: true}}
	pk := PrimaryKey(cols)
	if len(pk) != 2 || pk[0].Name != "a" || pk[1].Name != "c" {
		t.Errorf("PrimaryKey = %+v, want a and c", pk)
	}
	if pk := PrimaryKey([]schema.Column{{Name: "a"}}); pk != nil {
		t.Errorf("PrimaryKey = %+v, want none", pk)
	}
}

func TestHasTermMatchesTheRenderedExpression(t *testing.T) {
	order := []Term{{Expr: `"a"`}, {Expr: `"b"`}}
	if !HasTerm(order, `"b"`) {
		t.Error(`"b" is in the order and was not found`)
	}
	if HasTerm(order, "b") {
		t.Error("an unquoted spelling matched a quoted term; the comparison is on rendered SQL")
	}
}

// Adversarial: folding beyond ASCII is corruption, not tidiness. SQLite's own
// identifier comparison folds A-Z and leaves every other byte alone, so "Ä"
// and "ä" name different tables; strings.ToLower would fold them together and
// let one table's cursor page the other.
func TestFoldIdentFoldsASCIIOnly(t *testing.T) {
	if got := FoldIdent("MaIn"); got != "main" {
		t.Errorf("FoldIdent(%q) = %q, want %q", "MaIn", got, "main")
	}
	if got := FoldIdent("Ä"); got != "Ä" {
		t.Errorf("FoldIdent(%q) = %q; a non-ASCII letter must not be folded", "Ä", got)
	}
	if Token("main", "Ä", nil) == Token("main", "ä", nil) {
		t.Error("two tables SQLite tells apart share a token")
	}
}

// Adversarial: strings.EqualFold applies Unicode SIMPLE CASE FOLDING, which
// merges "s" with U+017F LATIN SMALL LETTER LONG S. SQLite's identifier
// comparison folds A-Z and nothing else, so a table may hold both columns at
// once — and a lookup that merged them would answer about the WRONG column
// rather than refuse, which is the failure FoldIdent's own doc comment
// argues against four functions further up this file.
//
// The first assertion is the guard against pinning an encoding whose inputs
// do not actually collide: if Go's folding ever stopped merging these two,
// every check below would pass without testing anything.
func TestColumnNamedDoesNotMergeColumnsUnicodeFoldingWould(t *testing.T) {
	const longS = "ſ"
	if !strings.EqualFold("s", longS) {
		t.Fatal("the fixture no longer collides under Unicode folding, so this test proves nothing")
	}
	cols := []schema.Column{{Name: "s", Position: 0}, {Name: longS, Position: 1}}

	if col, ok := ColumnNamed(cols, longS); !ok || col.Name != longS {
		t.Errorf("ColumnNamed(%q) = %+v, %v; it answered about a different column", longS, col, ok)
	}
	if col, ok := ColumnNamed(cols, "s"); !ok || col.Name != "s" {
		t.Errorf("ColumnNamed(%q) = %+v, %v; it answered about a different column", "s", col, ok)
	}
	// And the ASCII folding SQLite does apply is still applied.
	if col, ok := ColumnNamed(cols, "S"); !ok || col.Name != "s" {
		t.Errorf("ColumnNamed(%q) = %+v, %v; SQLite folds A-Z", "S", col, ok)
	}
}

// Adversarial: Predicate indexes after[i] once per ordering term, and the
// only width guard lived in the SQLite driver. Shared code that panics
// because a caller forgot a check is a panic in the SECOND driver's request
// path — rpc.dispatch turns it into a bare CodeInternal with no kind and no
// statement — so the precondition is validated where the indexing is.
func TestPredicateRefusesACursorNarrowerThanTheOrdering(t *testing.T) {
	order := []Term{{Expr: `"a"`}, {Expr: `"b"`}}
	after := []driver.Value{{Kind: driver.ValueInt, Text: "1"}}
	_, _, err := Predicate(order, after, bindText)
	if err == nil {
		t.Fatal("a cursor one value short of the ordering was accepted")
	}
	if got := dberr.From(err).Kind; got != dberr.KindInvalid {
		t.Errorf("kind = %q, want %q", got, dberr.KindInvalid)
	}
}

// The other direction is not a panic, which is why it needs saying: extra
// cursor values are simply never read, so the predicate would page from a
// boundary the caller did not describe and report nothing.
func TestPredicateRefusesACursorWiderThanTheOrdering(t *testing.T) {
	order := []Term{{Expr: `"a"`}}
	after := []driver.Value{
		{Kind: driver.ValueInt, Text: "1"},
		{Kind: driver.ValueInt, Text: "2"},
	}
	if _, _, err := Predicate(order, after, bindText); err == nil {
		t.Fatal("a cursor wider than the ordering was accepted")
	}
}

// Adversarial: appendArg drops exactly a nil argument, because a term built
// for a NULL binds none. A Bind that answers (nil, nil) for a value that DID
// render a placeholder therefore leaves the arguments one short of the
// placeholders, with no error — executed on a Bind returning nil for an
// empty text value, which produced a predicate with one argument for three
// "?". database/sql reports that much later, as a statement fault, with
// nothing pointing back at the Bind.
//
// SQLite's keysetArg never does this, so it is latent for the only shipped
// driver. Bind is the seam a second driver implements.
func TestPredicateRefusesABindThatReturnsNoParameterAndNoError(t *testing.T) {
	nilBind := func(driver.Value) (any, error) { return nil, nil }
	order := []Term{{Expr: `"a"`}, {Expr: `"b"`}}
	// Non-NULL, so each renders a placeholder; the empty text is what a
	// careless Bind is most likely to answer nil for.
	after := []driver.Value{
		{Kind: driver.ValueText, Text: ""},
		{Kind: driver.ValueText, Text: "x"},
	}
	sql, args, err := Predicate(order, after, nilBind)
	if err == nil {
		t.Fatalf("a Bind returning (nil, nil) was accepted: %q carries %d placeholders "+
			"and %d arguments", sql, strings.Count(sql, "?"), len(args))
	}
}

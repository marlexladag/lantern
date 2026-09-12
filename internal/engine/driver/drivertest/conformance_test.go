package drivertest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/schema"
)

// A conformance suite that cannot fail is worse than none: it certifies
// everything. brokenDriver deliberately violates one invariant at a time, and
// each subtest asserts the suite REPORTS that violation. If you add a check to
// the suite, add its break here.
//
// The want strings are chosen to name the VIOLATION rather than the shape of
// the failure. Two breaks that both end in "the driver returned an error"
// would be indistinguishable, and a suite whose every failure reads the same
// is one step from a suite that passes for the wrong reason.
func TestSuiteFailsADriverThatViolatesEachInvariant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breakIt func(*brokenDriver)
		want    string
	}{
		{"Open fails", func(d *brokenDriver) { d.failOpen = true }, "open"},
		{"a second connection cannot be opened", func(d *brokenDriver) { d.failSecondOpen = true }, "second connection"},
		{"the fixtures cannot be seeded", func(d *brokenDriver) { d.failSeed = true }, "seeding"},

		{"Introspect fails", func(d *brokenDriver) { d.failIntrospect = true }, "introspect"},
		{"Introspect returns no databases", func(d *brokenDriver) { d.noDatabases = true }, "at least one"},
		{"a database has an empty name", func(d *brokenDriver) { d.emptyDatabaseName = true }, "empty name"},
		{"tables are read eagerly by Introspect", func(d *brokenDriver) { d.eagerTables = true }, "tier one"},
		{"a single-database driver returns several", func(d *brokenDriver) { d.manyDatabases = true }, "multipledatabases"},

		{"Tables fails", func(d *brokenDriver) { d.failTables = true }, "tables"},
		{"Tables returns nil for an empty database", func(d *brokenDriver) { d.nilTables = true }, "nil"},
		{"Tables omits a seeded table", func(d *brokenDriver) { d.hideTables = true }, "lantern_conf_something_else"},
		{"columns are read eagerly by Tables", func(d *brokenDriver) { d.eagerColumns = true }, "tier two"},
		// Both wants name the METHOD and the call, not just the word
		// "database". They used to be the same string, so either break
		// satisfied either case — and because the flag behind the first also
		// reached Columns, deleting the suite's own check on Tables (the
		// method this whole branch exists to make honest) left the package
		// green.
		{"Tables ignores its database argument", func(d *brokenDriver) { d.tablesIgnoreDatabase = true }, `tables("` + unknownDatabase + `")`},
		{"Columns ignores its database argument", func(d *brokenDriver) { d.ignoreColumnsDatabase = true }, `columns("` + unknownDatabase + `"`},
		{"an unknown database is refused with the wrong kind", func(d *brokenDriver) { d.wrongKindNotFound = true }, "not_found"},

		{"Columns fails", func(d *brokenDriver) { d.failColumns = true }, "columns"},
		{"Columns returns no columns", func(d *brokenDriver) { d.noColumns = true }, "no columns"},
		{"a column has an empty name", func(d *brokenDriver) { d.emptyColumnName = true }, "empty name"},
		{"an unknown table is accepted", func(d *brokenDriver) { d.acceptUnknownTable = true }, "table"},

		{"Quote returns the identifier unchanged", func(d *brokenDriver) { d.identityQuote = true }, "unchanged"},
		{"the quoted form is not usable in a statement", func(d *brokenDriver) { d.failQuotedStatement = true }, "usable"},
		{"Quote does not round-trip", func(d *brokenDriver) { d.badQuote = true }, "round-trip"},

		{"Ping fails on an open connection", func(d *brokenDriver) { d.failPing = true }, "ping"},
		{"Ping succeeds after Close", func(d *brokenDriver) { d.pingAfterClose = true }, "after close"},

		{"the seeded table cannot be read back", func(d *brokenDriver) { d.failSelect = true }, "back with select"},
		{"Cursor.Next signals exhaustion with io.EOF", func(d *brokenDriver) { d.cursorEOF = true }, "never io.eof"},
		{"Cursor.Next fails once the result is exhausted", func(d *brokenDriver) { d.cursorFailsPastTheEnd = true }, "already-exhausted cursor reported"},
		{"Cursor.Next returns more rows than it was asked for", func(d *brokenDriver) { d.cursorOverreads = true }, "n is a ceiling"},
		{"Cursor.Next returns rows after exhaustion", func(d *brokenDriver) { d.cursorRowsPastTheEnd = true }, "already exhausted"},

		{"RequiredFields names a field the working config supplies", func(d *brokenDriver) { d.requireAWorkingField = true }, "connects with"},
		{"RequiredFields answers outside ConnConfig's vocabulary", func(d *brokenDriver) { d.requireAnUnknownField = true }, "lowercased name"},

		{"a write is accepted on a read-only connection", func(d *brokenDriver) { d.acceptWritesWhenReadOnly = true }, "worse than no flag"},
		{"a read-only write is refused to the caller and taken anyway", func(d *brokenDriver) { d.readOnlyRefusesButWrites = true }, "independent read-write"},
		{"a read-only write is refused with the wrong kind", func(d *brokenDriver) { d.wrongKindReadOnly = true }, "read_only"},
		{"a read-write connection cannot create the control table", func(d *brokenDriver) { d.failReadWriteProbe = true }, "over a read-write connection"},
		{"a table that was just created is invisible to Columns", func(d *brokenDriver) { d.hideCreatedTables = true }, "proves nothing"},

		{"Browse fails", func(d *brokenDriver) { d.failBrowse = true }, "browse"},
		{"Browse returns a nil page and no error", func(d *brokenDriver) { d.nilPage = true }, "nil page"},
		{"a page drops the key column", func(d *brokenDriver) { d.dropKeyColumn = true }, "has no"},
		{"a page's rows are narrower than its columns", func(d *brokenDriver) { d.raggedRows = true }, "row widths"},
		{"a page returns more rows than the limit", func(d *brokenDriver) { d.overLimit = true }, "for a limit of"},
		{"a keyset is issued with no sort token", func(d *brokenDriver) { d.keysetNoToken = true }, "issued a keyset"},
		{"paging never reports exhaustion", func(d *brokenDriver) { d.neverExhaust = true }, "did not terminate"},
		{"a page repeats a row across a boundary", func(d *brokenDriver) { d.repeatRow = true }, "exactly once"},
		{"a page skips a row across a boundary", func(d *brokenDriver) { d.skipRow = true }, "exactly once"},
		{"a page repeats one row and skips another, keeping the count", func(d *brokenDriver) { d.repeatAndSkipRow = true }, "exactly once"},

		{"a cursor is accepted under a different sort", func(d *brokenDriver) { d.acceptAnyToken = true }, "sort"},
		{"a cursor is accepted with no sort token", func(d *brokenDriver) { d.acceptNoToken = true }, "no sort token"},
		{"a cursor is refused under the sort it was issued for", func(d *brokenDriver) { d.refuseOwnCursor = true }, "replaying"},
		{"a bad cursor is refused with the wrong kind", func(d *brokenDriver) { d.wrongKindInvalid = true }, "invalid"},

		{"an empty table returns no columns", func(d *brokenDriver) { d.emptyHasNoColumns = true }, "still has a shape"},
		// Distinct from the case above, and the distinction is the whole
		// point: a non-nil empty slice marshals to [] and is merely the wrong
		// answer, a nil one marshals to null and is the answer that blanks
		// the window. A Go-side len() cannot tell them apart.
		{"an empty table's columns marshal as null", func(d *brokenDriver) { d.emptyHasNullColumns = true }, "never as null"},
		{"an empty table returns rows", func(d *brokenDriver) { d.emptyHasRows = true }, "there are none"},
		{"an empty table does not report exhaustion", func(d *brokenDriver) { d.emptyNotExhausted = true }, "exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newBrokenDriver()
			tc.breakIt(d)
			fake := runSuite(d)
			if !fake.failed {
				t.Fatal("the suite passed a driver that violates this invariant")
			}
			if !strings.Contains(strings.ToLower(fake.text()), tc.want) {
				t.Errorf("failure message did not mention %q:\n%s", tc.want, fake.text())
			}
		})
	}
}

// The negative control for the table above. If the suite failed every driver
// it would "catch" all thirty-odd violations while testing nothing, which is
// the same vacuous pass in a different costume.
func TestSuitePassesADriverThatViolatesNothing(t *testing.T) {
	fake := runSuite(newBrokenDriver())
	if fake.failed {
		t.Fatalf("the suite failed a driver that honours every invariant:\n%s", fake.text())
	}
}

// Browse is an optional interface (spec section 4), so a driver that cannot
// page a table must reach the end of the suite rather than fail it.
func TestSuiteSkipsPagingForADriverThatCannotBrowse(t *testing.T) {
	d := newBrokenDriver()
	d.noBrowser = true
	fake := runSuite(d)
	if fake.failed {
		t.Fatalf("the suite failed a driver that simply does not implement Browser:\n%s", fake.text())
	}
}

// Keyset and offset paging are alternatives, and a driver that answers with
// an Offset and no Keyset has no cursor to replay — so the two cursor
// invariants have nothing to say about it. Every row still has to come back
// exactly once.
func TestSuiteStillCountsRowsForAnOffsetPagingDriver(t *testing.T) {
	d := newBrokenDriver()
	d.offsetPaging = true
	fake := runSuite(d)
	if fake.failed {
		t.Fatalf("the suite failed a correct offset-paging driver:\n%s", fake.text())
	}

	// And the skip is a skip, not a blind spot: the same driver paging by
	// offset WRONGLY is still caught.
	d = newBrokenDriver()
	d.offsetPaging, d.skipRow = true, true
	if fake := runSuite(d); !fake.failed {
		t.Fatal("the suite passed an offset-paging driver that loses a row")
	}
}

// Spec section 4 gives a driver with no engine-enforced read-only mechanism
// exactly one permitted answer: fail Open. So a driver that refuses the flag
// must reach the end of the suite rather than fail it — the refusal IS the
// safety mechanism, and a suite that punished it would push the next driver
// toward accepting a flag it cannot honour.
func TestSuiteAcceptsADriverThatRefusesAReadOnlyConnection(t *testing.T) {
	d := newBrokenDriver()
	d.refuseReadOnlyOpen = true
	fake := runSuite(d)
	if fake.failed {
		t.Fatalf("the suite failed a driver that refuses read-only at Open, which is what "+
			"section 4 requires of a driver that cannot enforce it:\n%s", fake.text())
	}
}

// Observe is how a driver's own test proves its offset fallback is REACHED
// through the suite rather than merely implemented. It has to tell the two
// paginations apart, so both directions are asserted: a keyset driver must
// never report an offset continuation and an offset driver must never report
// a keyset one, or an assertion built on it certifies whichever path the
// driver did not take.
func TestObserveNamesThePaginationTheDriverChose(t *testing.T) {
	run := func(offsetPaging bool) map[PagingPath]int {
		t.Helper()
		d := newBrokenDriver()
		d.offsetPaging = offsetPaging
		seen := map[PagingPath]int{}
		r := &recordingT{}
		func() {
			defer catchFatal()
			Run(r, Config{Driver: d, Open: d.open, Observe: func(p PagingPath) { seen[p]++ }})
		}()
		if r.failed {
			t.Fatalf("the suite failed a correct driver:\n%s", r.text())
		}
		return seen
	}

	keyset := run(false)
	if keyset[PathKeyset] == 0 {
		t.Error("a keyset-paging driver reported no keyset continuation")
	}
	if keyset[PathOffset] != 0 {
		t.Errorf("a keyset-paging driver reported %d offset continuations", keyset[PathOffset])
	}

	offset := run(true)
	if offset[PathOffset] == 0 {
		t.Error("an offset-paging driver reported no offset continuation")
	}
	if offset[PathKeyset] != 0 {
		t.Errorf("an offset-paging driver reported %d keyset continuations", offset[PathKeyset])
	}
}

// CREATE TABLE differs enough between engines that a shared literal would be
// a lie, so a driver may supply its own. This asserts the supplied statement
// is the one actually issued — a Fixtures field that were quietly ignored
// would leave the second driver seeding nothing and every paging invariant
// passing over an empty table.
func TestFixturesFromTheConfigAreIssued(t *testing.T) {
	d := newBrokenDriver()
	mine := "CREATE TABLE " + TableRows + " (id INT, val VARCHAR(8)) ENGINE=InnoDB"
	fake := &recordingT{}
	Run(fake, Config{
		Driver: d,
		Open:   d.open,
		DDL:    Fixtures{Create: map[string]string{TableRows: mine}},
	})
	if fake.failed {
		t.Fatalf("suite failed:\n%s", fake.text())
	}
	if !containsString(d.stmts, mine) {
		t.Errorf("the driver's own CREATE TABLE was never issued; got:\n%s", strings.Join(d.stmts, "\n"))
	}
	for _, s := range d.stmts {
		if strings.HasPrefix(s, "CREATE TABLE "+TableRows+" (") && s != mine {
			t.Errorf("the default CREATE TABLE was issued alongside the driver's own: %q", s)
		}
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// recordingT

// errFatal unwinds a Fatalf. *testing.T uses runtime.Goexit, which needs a
// goroutine per subtest to survive; a panic recovered at the same boundary
// testing.T's Goexit is recovered at — the subtest, and the top-level Run —
// gets the same "Fatalf does not return" semantics without one, which is
// what lets these tests run the suite inline.
var errFatal = errors.New("drivertest: fatal")

// recordingT stands in for *testing.T so the suite itself can be tested. It
// satisfies exactly what Run's type parameter requires; that it can is the
// reason Run is defined against an interface at all.
type recordingT struct {
	failed bool
	sb     strings.Builder
}

func (r *recordingT) Helper() {}

func (r *recordingT) Errorf(format string, args ...any) {
	r.failed = true
	r.sb.WriteString(strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
	r.sb.WriteByte('\n')
}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	panic(errFatal)
}

func (r *recordingT) Run(name string, f func(*recordingT)) bool {
	before := r.failed
	func() {
		defer catchFatal()
		f(r)
	}()
	return r.failed == before
}

func (r *recordingT) text() string { return r.sb.String() }

// catchFatal swallows exactly the sentinel Fatalf raises and nothing else, so
// a genuine panic inside the suite still reaches the test framework instead
// of being reported as an ordinary conformance failure.
func catchFatal() {
	if v := recover(); v != nil && v != errFatal {
		panic(v)
	}
}

// runSuite drives the whole suite against d and returns what it reported.
func runSuite(d *brokenDriver) *recordingT {
	r := &recordingT{}
	func() {
		defer catchFatal()
		Run(r, Config{Driver: d, Open: d.open})
	}()
	return r
}

// ---------------------------------------------------------------------------
// brokenDriver

const brokenDatabase = "fake"

// brokenDriver is an in-memory driver that honours every invariant until one
// of its fields is set. Its rows are the suite's OWN fixture data rather than
// a second copy of it: a fake seeded from a transcription would drift from
// what the suite seeds, and every paging assertion would then be checking the
// transcription.
type brokenDriver struct {
	failOpen          bool
	failSecondOpen    bool
	opens             int
	failSeed          bool
	failIntrospect    bool
	noDatabases       bool
	emptyDatabaseName bool
	eagerTables       bool
	manyDatabases     bool

	// databases, when non-empty, replaces the fake's single database: the
	// driver reports exactly these names, in this order, and the fixtures
	// live in fixtureDatabase rather than in whichever one comes first.
	// Together they model the shape every multi-database engine has and
	// SQLite does not — MySQL lists schemas in NAME order, so
	// information_schema leads and the application's own schema sits
	// somewhere in the middle.
	databases       []string
	fixtureDatabase string

	failTables   bool
	nilTables    bool
	hideTables   bool
	eagerColumns bool
	// tablesIgnoreDatabase is scoped to Tables, and the scope is the point:
	// it used to flip knownDatabase, which Columns consults too, so the
	// break produced two failures and the suite's own check on Tables was
	// pinned only by the one belonging to Columns.
	tablesIgnoreDatabase  bool
	ignoreColumnsDatabase bool
	wrongKindNotFound     bool

	failColumns        bool
	noColumns          bool
	emptyColumnName    bool
	acceptUnknownTable bool

	identityQuote       bool
	failQuotedStatement bool
	badQuote            bool

	failPing       bool
	pingAfterClose bool

	failSelect            bool
	cursorEOF             bool
	cursorFailsPastTheEnd bool
	cursorOverreads       bool
	cursorRowsPastTheEnd  bool

	requireAWorkingField  bool
	requireAnUnknownField bool

	// The read-only breaks. acceptWritesWhenReadOnly is a driver that takes
	// the flag and ignores it; readOnlyRefusesButWrites is the subtler one
	// this suite reads back over an independent connection to catch — the
	// caller sees a refusal and the engine takes the write anyway.
	refuseReadOnlyOpen       bool
	acceptWritesWhenReadOnly bool
	readOnlyRefusesButWrites bool
	wrongKindReadOnly        bool
	failReadWriteProbe       bool
	hideCreatedTables        bool

	noBrowser    bool
	offsetPaging bool

	failBrowse       bool
	nilPage          bool
	dropKeyColumn    bool
	raggedRows       bool
	repeatAndSkipRow bool
	overLimit        bool
	keysetNoToken    bool
	neverExhaust     bool
	repeatRow        bool
	skipRow          bool

	acceptAnyToken   bool
	acceptNoToken    bool
	refuseOwnCursor  bool
	wrongKindInvalid bool

	emptyHasNoColumns   bool
	emptyHasNullColumns bool
	emptyHasRows        bool
	emptyNotExhausted   bool

	// stmts records every statement the suite issued, so a test can assert
	// which DDL was used.
	stmts []string
	// created records the tables a CREATE TABLE actually LANDED for, on the
	// driver rather than the connection, so a write taken through one
	// connection is visible through another. Modelling where the write lands
	// — not merely whether the caller was refused — is what lets the
	// read-only checks be read back independently.
	created map[string]bool
}

func newBrokenDriver() *brokenDriver {
	return &brokenDriver{created: map[string]bool{}}
}

// createdTable reports the table name a CREATE TABLE statement names.
func createdTable(stmt string) (string, bool) {
	rest, ok := strings.CutPrefix(stmt, "CREATE TABLE ")
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(rest, " ")
	return name, name != ""
}

func (d *brokenDriver) ID() string { return "broken" }

func (d *brokenDriver) Capabilities() driver.Capabilities {
	// From d.databases alone, never from databaseNames: manyDatabases is the
	// break where a driver CLAIMS one database and reports several, so its
	// capability bit has to stay false while its catalog grows.
	return driver.Capabilities{MultipleDatabases: len(d.databases) > 1}
}

// databaseNames is every database this fake admits to having, in the order
// Introspect reports them.
func (d *brokenDriver) databaseNames() []string {
	switch {
	case len(d.databases) > 0:
		return d.databases
	case d.manyDatabases:
		return []string{brokenDatabase, "other"}
	}
	return []string{brokenDatabase}
}

// fixtureDB is the database the seeded tables actually live in — the one
// Config.Database has to name, and deliberately not always the first one
// databaseNames reports.
func (d *brokenDriver) fixtureDB() string {
	switch {
	case d.emptyDatabaseName:
		// The fake's one database is unnamed, so that is where its fixtures
		// are. Saying otherwise would add a second, unrelated failure to a
		// break whose subject is the empty name.
		return ""
	case d.fixtureDatabase != "":
		return d.fixtureDatabase
	}
	return brokenDatabase
}

func (d *brokenDriver) RequiredFields(cfg driver.ConnConfig) []string {
	switch {
	case d.requireAWorkingField:
		// Named for every config, the one the suite connects with included.
		return []string{"file"}
	case d.requireAnUnknownField && cfg.Driver == "":
		// Only for the empty config, so this break is about the VOCABULARY
		// alone and does not also trip the over-reporting check above.
		return []string{"SQLite file path"}
	}
	return nil
}

func (d *brokenDriver) open(TestingT) driver.ConnConfig {
	return driver.ConnConfig{Driver: d.ID()}
}

func (d *brokenDriver) Open(_ context.Context, cfg driver.ConnConfig) (driver.Conn, error) {
	d.opens++
	if d.failOpen || (d.failSecondOpen && d.opens > 1) {
		return nil, dberr.New(dberr.KindNetwork, "broken: refusing to open")
	}
	if cfg.ReadOnly && d.refuseReadOnlyOpen {
		// The permitted answer for an engine with no enforcement mechanism:
		// refuse the flag rather than accept it and allow writes.
		return nil, dberr.New(dberr.KindUnsupported,
			"broken: this engine cannot enforce a read-only connection")
	}
	c := &brokenConn{d: d, readOnly: cfg.ReadOnly}
	if d.noBrowser {
		return noBrowseConn{c}, nil
	}
	return c, nil
}

func (d *brokenDriver) notFound(msg string) error {
	if d.wrongKindNotFound {
		return dberr.New(dberr.KindUnknown, msg)
	}
	return dberr.New(dberr.KindNotFound, msg)
}

func (d *brokenDriver) invalid(msg string) error {
	if d.wrongKindInvalid {
		return dberr.New(dberr.KindUnknown, msg)
	}
	return dberr.New(dberr.KindInvalid, msg)
}

func (d *brokenDriver) readOnlyErr(msg string) error {
	if d.wrongKindReadOnly {
		return dberr.New(dberr.KindUnknown, msg)
	}
	return dberr.New(dberr.KindReadOnly, msg)
}

type brokenConn struct {
	d        *brokenDriver
	readOnly bool
	closed   bool
}

func (c *brokenConn) Ping(context.Context) error {
	switch {
	case c.d.failPing:
		return dberr.New(dberr.KindNetwork, "broken: ping")
	case c.closed && !c.d.pingAfterClose:
		return dberr.New(dberr.KindNetwork, "broken: connection is closed")
	}
	return nil
}

func (c *brokenConn) Close() error {
	c.closed = true
	return nil
}

// Quote doubles an embedded quote, the way SQLite and PostgreSQL do.
func (c *brokenConn) Quote(ident string) string {
	if c.d.identityQuote {
		return ident
	}
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func unquote(s string) string {
	s = strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
	return strings.ReplaceAll(s, `""`, `"`)
}

func (c *brokenConn) Introspect(context.Context) (*schema.Catalog, error) {
	if c.d.failIntrospect {
		return nil, dberr.New(dberr.KindNetwork, "broken: introspect")
	}
	if c.d.noDatabases {
		return &schema.Catalog{}, nil
	}
	cat := &schema.Catalog{}
	for i, name := range c.d.databaseNames() {
		db := schema.Database{Name: name}
		if i == 0 && c.d.emptyDatabaseName {
			db.Name = ""
		}
		if i == 0 && c.d.eagerTables {
			db.Tables = []schema.Table{}
		}
		cat.Databases = append(cat.Databases, db)
	}
	return cat, nil
}

func (c *brokenConn) knownDatabase(name string) bool {
	for _, db := range c.d.databaseNames() {
		if strings.EqualFold(name, db) || (c.d.emptyDatabaseName && name == "") {
			return true
		}
	}
	return false
}

// holdsFixtures reports whether name is the database the seeded tables are
// in. It is separate from knownDatabase because a multi-database engine has
// databases that exist and are empty — answering about the fixtures for all
// of them is the very defect Config.Database exists to stop the suite from
// hiding.
func (c *brokenConn) holdsFixtures(name string) bool {
	return strings.EqualFold(name, c.d.fixtureDB())
}

func (c *brokenConn) Tables(_ context.Context, database string) ([]schema.Table, error) {
	if c.d.failTables {
		return nil, dberr.New(dberr.KindNetwork, "broken: tables")
	}
	if !c.d.tablesIgnoreDatabase && !c.knownDatabase(database) {
		return nil, c.d.notFound("broken: no such database: " + database)
	}
	if c.d.nilTables {
		return nil, nil
	}
	out := []schema.Table{}
	if !c.d.tablesIgnoreDatabase && !c.holdsFixtures(database) {
		return out, nil
	}
	if c.d.hideTables {
		// A table, just not the seeded ones: a driver that returned nothing
		// at all would be caught by the nil check instead.
		return append(out, schema.Table{Name: "lantern_conf_something_else"}), nil
	}
	for _, fx := range fixtures {
		t := schema.Table{Name: fx.name, Kind: schema.TableKindTable}
		if fx.viewOf != "" {
			t.Kind = schema.TableKindView
		}
		if c.d.eagerColumns {
			t.Columns = brokenColumns()
		}
		out = append(out, t)
	}
	return out, nil
}

func brokenColumns() []schema.Column {
	return []schema.Column{
		{Name: KeyColumn, DataType: "INTEGER", PrimaryKey: true, Position: 0},
		{Name: ValueColumn, DataType: "TEXT", Nullable: true, Position: 1},
	}
}

func (c *brokenConn) Columns(_ context.Context, database, table string) ([]schema.Column, error) {
	if c.d.failColumns {
		return nil, dberr.New(dberr.KindNetwork, "broken: columns")
	}
	// Honoured unless the driver is broken in exactly this way: the parameter
	// is declared and ignored, which is invariant 12's whole subject.
	if !c.d.ignoreColumnsDatabase && !c.knownDatabase(database) {
		return nil, c.d.notFound("broken: no such database: " + database)
	}
	// A fixture table is only in the fixture database. ignoreColumnsDatabase
	// is what makes this fake answer about it from anywhere, which is
	// invariant 12's subject.
	here := c.d.ignoreColumnsDatabase || c.holdsFixtures(database)
	_, isFixture := fixtureNamed(table)
	// A table a CREATE TABLE landed for is there too — that is what the
	// read-only checks read back. hideCreatedTables breaks exactly that, so
	// their positive control has something to fail against.
	exists := here && (isFixture || (c.d.created[table] && !c.d.hideCreatedTables))
	if !exists && !c.d.acceptUnknownTable {
		return nil, c.d.notFound("broken: no such table: " + table)
	}
	if c.d.noColumns {
		return []schema.Column{}, nil
	}
	cols := brokenColumns()
	if c.d.emptyColumnName {
		cols[0].Name = ""
	}
	return cols, nil
}

// Query records what it was asked to run. The fixture statements are not
// executed — the tables are already in memory — but a driver that refuses
// them still has to look like one, which is what failSeed models.
func (c *brokenConn) Query(_ context.Context, stmt string, _ ...any) (driver.Cursor, error) {
	c.d.stmts = append(c.d.stmts, stmt)
	if alias, ok := strings.CutPrefix(stmt, "SELECT 1 AS "); ok {
		if c.d.failQuotedStatement {
			return nil, dberr.New(dberr.KindSyntax, "broken: cannot parse "+alias)
		}
		name := unquote(alias)
		if c.d.badQuote {
			name = alias
		}
		return &brokenCursor{d: c.d, meta: []driver.ColumnMeta{{Name: name}}}, nil
	}
	if c.d.failSeed {
		return nil, dberr.New(dberr.KindSyntax, "broken: refusing "+stmt)
	}
	if c.d.failSelect && strings.HasPrefix(stmt, "SELECT ") {
		// The quoting probe is handled above, so this is only ever the
		// cursor check's read-back of a seeded table.
		return nil, dberr.New(dberr.KindNetwork, "broken: refusing "+stmt)
	}
	if name, ok := createdTable(stmt); ok {
		switch {
		case c.readOnly && c.d.acceptWritesWhenReadOnly:
			// The flag taken and not honoured: the write simply lands.
		case c.readOnly && c.d.readOnlyRefusesButWrites:
			// Refused where the caller can see it, taken where it cannot.
			c.d.created[name] = true
			return nil, c.d.readOnlyErr("broken: the connection is read-only")
		case c.readOnly:
			return nil, c.d.readOnlyErr("broken: the connection is read-only")
		case c.d.failReadWriteProbe && name == readOnlyControl:
			// Scoped to the control table: refusing every CREATE TABLE would
			// abort the run during seeding and never reach the check.
			return nil, dberr.New(dberr.KindSyntax, "broken: refusing "+stmt)
		}
		c.d.created[name] = true
	}
	return &brokenCursor{d: c.d}, nil
}

type brokenCursor struct {
	d     *brokenDriver
	meta  []driver.ColumnMeta
	calls int
}

func (c *brokenCursor) Columns() []driver.ColumnMeta { return c.meta }
func (c *brokenCursor) Close() error                 { return nil }

// Next returns nothing, which is a legitimate exhausted read: the fake's rows
// live in memory and the suite's cursor check asserts the SIGNAL, not the
// contents. The breaks are the three ways a driver can get that signal wrong.
func (c *brokenCursor) Next(_ context.Context, n int) ([]driver.Row, error) {
	c.calls++
	switch {
	case c.d.cursorEOF:
		return nil, io.EOF
	case c.d.cursorFailsPastTheEnd && c.calls > 1:
		return nil, dberr.New(dberr.KindNetwork, "broken: reading past the end")
	case c.d.cursorOverreads:
		return make([]driver.Row, n+1), nil
	case c.d.cursorRowsPastTheEnd && c.calls > 1:
		return make([]driver.Row, 1), nil
	}
	return nil, nil
}

// noBrowseConn forwards Conn and deliberately does not promote Browse, so the
// suite sees a driver whose type assertion to driver.Browser fails.
type noBrowseConn struct{ c *brokenConn }

func (n noBrowseConn) Ping(ctx context.Context) error { return n.c.Ping(ctx) }
func (n noBrowseConn) Close() error                   { return n.c.Close() }
func (n noBrowseConn) Quote(ident string) string      { return n.c.Quote(ident) }
func (n noBrowseConn) Introspect(ctx context.Context) (*schema.Catalog, error) {
	return n.c.Introspect(ctx)
}
func (n noBrowseConn) Tables(ctx context.Context, db string) ([]schema.Table, error) {
	return n.c.Tables(ctx, db)
}
func (n noBrowseConn) Columns(ctx context.Context, db, t string) ([]schema.Column, error) {
	return n.c.Columns(ctx, db, t)
}
func (n noBrowseConn) Query(ctx context.Context, s string, args ...any) (driver.Cursor, error) {
	return n.c.Query(ctx, s, args...)
}

// ---------------------------------------------------------------------------
// brokenConn.Browse

func (c *brokenConn) Browse(_ context.Context, req driver.BrowseRequest) (*driver.BrowsePage, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if c.d.failBrowse {
		return nil, dberr.New(dberr.KindNetwork, "broken: browse")
	}
	if c.d.nilPage {
		return nil, nil
	}
	fx, ok := fixtureNamed(req.Table)
	if !ok {
		return nil, c.d.notFound("broken: no such table: " + req.Table)
	}
	if !c.knownDatabase(req.Database) {
		return nil, c.d.notFound("broken: no such database: " + req.Database)
	}
	if !c.holdsFixtures(req.Database) {
		return nil, c.d.notFound("broken: no such table: " + req.Table)
	}

	order := brokenOrder(req.Sort)
	token := brokenToken(req.Database, req.Table, order)
	if len(req.After) > 0 {
		switch {
		case req.SortToken == "":
			if !c.d.acceptNoToken {
				return nil, c.d.invalid("broken: the cursor carries no sort token")
			}
		case req.SortToken != token && !c.d.acceptAnyToken:
			return nil, c.d.invalid("broken: the cursor was issued for a different sort")
		}
		if c.d.refuseOwnCursor {
			return nil, c.d.invalid("broken: refusing every cursor")
		}
	}

	rows := append([]fixtureRow(nil), fx.rows...)
	sort.SliceStable(rows, func(i, j int) bool { return brokenLess(rows[i], rows[j], order) })

	start, continuing := 0, false
	if c.d.offsetPaging {
		start, continuing = req.Offset, req.Offset > 0
	} else if len(req.After) > 0 {
		start, continuing = brokenSeek(rows, order, req.After, c.d.repeatRow), true
	}
	// Losing a row is a property of the BOUNDARY, so it only bites on a
	// continuation — which is as true of an offset page as of a keyset one.
	if c.d.skipRow && continuing {
		start++
	}
	end := min(start+req.Limit, len(rows))
	if c.d.overLimit {
		end = min(end+1, len(rows))
	}
	taken := rows[min(start, len(rows)):max(end, min(start, len(rows)))]

	page := &driver.BrowsePage{Columns: []driver.ColumnMeta{
		{Name: KeyColumn, DataType: "INTEGER"},
		{Name: ValueColumn, DataType: "TEXT"},
	}}
	if c.d.dropKeyColumn {
		page.Columns = page.Columns[1:]
	}
	if c.d.repeatAndSkipRow && continuing && len(taken) > 0 {
		// The boundary row comes back AND the row after it is lost, so the
		// count still adds up and only the multiset gives it away.
		taken[0] = rows[start-1]
	}
	for _, r := range taken {
		cells := []driver.Value{brokenValue(r, KeyColumn), brokenValue(r, ValueColumn)}
		switch {
		case c.d.dropKeyColumn:
			cells = cells[1:]
		case c.d.raggedRows:
			// Narrower than the page says it is, with the columns left alone.
			cells = cells[:1]
		}
		page.Rows = append(page.Rows, cells)
	}
	page.Exhausted = len(taken) < req.Limit

	if len(fx.rows) == 0 {
		if c.d.emptyHasNoColumns {
			// Non-nil, so this break is the shape defect alone: it marshals
			// to [] and only the len() check has anything to say about it.
			page.Columns = []driver.ColumnMeta{}
		}
		if c.d.emptyHasNullColumns {
			page.Columns = nil
		}
		if c.d.emptyHasRows {
			page.Rows = [][]driver.Value{{brokenValue(fixtureRow{key: 1}, KeyColumn), brokenValue(fixtureRow{null: true}, ValueColumn)}}
		}
		if c.d.emptyNotExhausted {
			page.Exhausted = false
		}
	}
	if c.d.neverExhaust {
		page.Exhausted = false
	}

	switch {
	case c.d.offsetPaging:
		page.Offset = start + len(taken)
	case !page.Exhausted && len(taken) > 0:
		last := taken[len(taken)-1]
		for _, col := range order {
			page.Keyset = append(page.Keyset, brokenValue(last, col.Column))
		}
		page.SortToken = token
		if c.d.keysetNoToken {
			page.SortToken = ""
		}
	}
	return page, nil
}

// brokenOrder resolves a request's sort the way a real driver must: the
// caller's terms, then the unique key appended so the ordering is TOTAL.
func brokenOrder(req []driver.SortKey) []driver.SortKey {
	order := append([]driver.SortKey(nil), req...)
	for _, k := range order {
		if k.Column == KeyColumn {
			return order
		}
	}
	return append(order, driver.SortKey{Column: KeyColumn})
}

func brokenToken(database, table string, order []driver.SortKey) string {
	var b strings.Builder
	b.WriteString(database + "\x00" + table)
	for _, k := range order {
		b.WriteString("\x00" + k.Column + strconv.FormatBool(k.Desc))
	}
	return b.String()
}

func brokenValue(r fixtureRow, column string) driver.Value {
	if column == KeyColumn {
		return driver.Value{Kind: driver.ValueInt, Text: strconv.Itoa(r.key)}
	}
	if r.null {
		return driver.Value{Kind: driver.ValueNull}
	}
	return driver.Value{Kind: driver.ValueText, Text: r.val}
}

// brokenCmp orders two rows on one term. NULL sorts before every value
// ascending and after every value descending, which is what SQLite and MySQL
// both do and what keyset.Predicate is written against.
func brokenCmp(a, b fixtureRow, k driver.SortKey) int {
	var c int
	switch {
	case k.Column == KeyColumn:
		c = a.key - b.key
	case a.null && b.null:
		c = 0
	case a.null:
		c = -1
	case b.null:
		c = 1
	default:
		c = strings.Compare(a.val, b.val)
	}
	if k.Desc {
		return -c
	}
	return c
}

func brokenLess(a, b fixtureRow, order []driver.SortKey) bool {
	for _, k := range order {
		if c := brokenCmp(a, b, k); c != 0 {
			return c < 0
		}
	}
	return false
}

// brokenSeek finds the first row strictly after the cursor. inclusive is the
// classic off-by-one — ">=" where the predicate must say ">" — and is what
// repeats the boundary row on every page.
func brokenSeek(rows []fixtureRow, order []driver.SortKey, after []driver.Value, inclusive bool) int {
	cursor := fixtureRow{}
	for i, k := range order {
		if i >= len(after) {
			break
		}
		switch {
		case k.Column == KeyColumn:
			cursor.key, _ = strconv.Atoi(after[i].Text)
		case after[i].Kind == driver.ValueNull:
			cursor.null = true
		default:
			cursor.val = after[i].Text
		}
	}
	for i, r := range rows {
		c := 0
		for _, k := range order {
			if c = brokenCmp(r, cursor, k); c != 0 {
				break
			}
		}
		if c > 0 || (inclusive && c == 0) {
			return i
		}
	}
	return len(rows)
}

// E-1 repro: an engine that reports several databases reports them in its
// own order, and for MySQL that is NAME order — information_schema first,
// the fixtures nowhere near it. The suite used to take Databases[0] as "the
// database the fixtures live in", which is true only of an engine that has
// exactly one.
func TestSuitePassesAMultiDatabaseDriverWhoseFixturesAreNotFirst(t *testing.T) {
	d := newBrokenDriver()
	d.databases = []string{"information_schema", "lantern_app", "mysql", "performance_schema"}
	d.fixtureDatabase = "lantern_app"

	r := &recordingT{}
	func() {
		defer catchFatal()
		Run(r, Config{Driver: d, Open: d.open, Database: d.fixtureDatabase})
	}()
	if r.failed {
		t.Fatalf("the suite failed a correct driver whose fixtures are not in Databases[0]:\n%s", r.text())
	}
}

// runMultiDB drives the suite against the four-schema fake, with whatever
// Config.Database the caller wants to test, and returns what it reported.
func runMultiDB(database string) *recordingT {
	d := newBrokenDriver()
	d.databases = []string{"information_schema", "lantern_app", "mysql", "performance_schema"}
	d.fixtureDatabase = "lantern_app"
	r := &recordingT{}
	func() {
		defer catchFatal()
		Run(r, Config{Driver: d, Open: d.open, Database: database})
	}()
	return r
}

// The control for the test above: the fake does not pass by answering the
// same for every database. Point the suite at a schema that exists and is
// empty — which is exactly what Databases[0] used to resolve to — and the
// missing fixtures come back, the reviewer's original thirty failures in
// one.
func TestSuiteReportsFixturesMissingFromTheDatabaseItWasPointedAt(t *testing.T) {
	r := runMultiDB("information_schema")
	if !r.failed {
		t.Fatal("the suite found its fixtures in a database that does not hold them; " +
			"the fake answers the same for every database and proves nothing")
	}
	if !strings.Contains(r.text(), "did not list the seeded table") {
		t.Errorf("failure did not name the missing fixtures:\n%s", r.text())
	}
}

// A driver that reports several databases and names none leaves the suite
// with nothing to look in, and guessing at the first is how this defect
// shipped. Refusing says so once instead of thirty times.
func TestSuiteRefusesAMultiDatabaseDriverThatNamesNoDatabase(t *testing.T) {
	r := runMultiDB("")
	if !r.failed {
		t.Fatal("the suite guessed at a database for a driver that reports four")
	}
	if !strings.Contains(r.text(), "Config.Database names none") {
		t.Errorf("failure did not say what is missing from the config:\n%s", r.text())
	}
}

// And a Config.Database the driver does not report is a config error too —
// the same thirty-failure cascade, from the other direction.
func TestSuiteRefusesAConfigDatabaseTheDriverDoesNotReport(t *testing.T) {
	r := runMultiDB("lantern_typo")
	if !r.failed {
		t.Fatal("the suite accepted a fixture database the driver never reported")
	}
	if !strings.Contains(r.text(), "does not admit to having") {
		t.Errorf("failure did not name the mismatch:\n%s", r.text())
	}
}

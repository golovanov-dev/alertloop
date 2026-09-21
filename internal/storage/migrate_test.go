package storage

import (
	"reflect"
	"strings"
	"testing"
)

// The splitter used to cut on every `;` in the file, including ones inside
// comments — which turned an explanatory sentence into a statement and failed
// the migration at run time with a syntax error pointing at English prose.
// Migrations are the one thing that runs against a customer's data on upgrade,
// so this is a regression test, not a style test.
func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "semicolon inside a line comment does not split",
			body: "-- reads through this index; the lookup is hot.\nCREATE INDEX a ON t (c);",
			want: []string{"-- reads through this index; the lookup is hot.\nCREATE INDEX a ON t (c)"},
		},
		{
			name: "semicolon inside a string literal does not split",
			body: "UPDATE t SET c = 'a;b' WHERE d = 1;",
			want: []string{"UPDATE t SET c = 'a;b' WHERE d = 1"},
		},
		{
			name: "escaped quote inside a literal",
			body: "UPDATE t SET c = 'it''s; fine';\nSELECT 1;",
			want: []string{"UPDATE t SET c = 'it''s; fine'", "SELECT 1"},
		},
		{
			name: "ordinary statements still split",
			body: "ALTER TABLE t ADD COLUMN a TEXT;\nALTER TABLE t ADD COLUMN b TEXT;\n",
			want: []string{"ALTER TABLE t ADD COLUMN a TEXT", "ALTER TABLE t ADD COLUMN b TEXT"},
		},
		{
			name: "trailing statement without a semicolon",
			body: "SELECT 1",
			want: []string{"SELECT 1"},
		},
		{
			name: "comment-only body yields no statements",
			body: "-- nothing here; really nothing\n",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitStatements(c.body)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v\nwant %#v", got, c.want)
			}
		})
	}
}

// Every shipped migration must survive the splitter. A `;` in a comment made
// this fail before the splitter was fixed, and nothing would have caught it
// until an upgrade ran.
func TestShippedMigrationsSplitIntoValidStatements(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		entries, err := migrationFS.ReadDir("migrations/" + dialect)
		if err != nil {
			t.Fatalf("read %s migrations: %v", dialect, err)
		}
		for _, e := range entries {
			body, err := migrationFS.ReadFile("migrations/" + dialect + "/" + e.Name())
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			for i, stmt := range splitStatements(string(body)) {
				// Strip leading comment lines; what remains must start with SQL.
				var code []string
				for _, line := range strings.Split(stmt, "\n") {
					if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "--") {
						code = append(code, s)
					}
				}
				if len(code) == 0 {
					t.Errorf("%s/%s statement %d is comment-only; the splitter cut inside a comment",
						dialect, e.Name(), i)
					continue
				}
				switch first := strings.ToUpper(strings.Fields(code[0])[0]); first {
				case "CREATE", "ALTER", "DROP", "UPDATE", "INSERT", "DELETE":
				default:
					t.Errorf("%s/%s statement %d starts with %q, which is not SQL:\n%s",
						dialect, e.Name(), i, first, stmt)
				}
			}
		}
	}
}

package ignores

import (
	"reflect"
	"strings"
	"testing"
)

func TestStripComments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "no comments", input: `a = 1`, want: `a = 1`},
		{name: "line comment", input: "a // note\nb", want: "a \nb"},
		{name: "line comment at end", input: "a // note", want: "a "},
		{name: "block comment", input: "a /* x\ny */ b", want: "a   b"},
		{name: "double slash in single quotes", input: `'//' // c`, want: `'//' `},
		{name: "double slash in double quotes", input: `"a//b" /* c */`, want: `"a//b"  `},
		{name: "block opener in string", input: `'/*' + "*/"`, want: `'/*' + "*/"`},
		{name: "escaped quote", input: `'it\'s // not' // c`, want: `'it\'s // not' `},
		{name: "template", input: "`a // b /* c */` // c", want: "`a // b /* c */` "},
		{name: "template with substitution", input: "`${'//'} // x` // c", want: "`${'//'} // x` "},
		{name: "comment inside substitution", input: "`${a /* c */ + b}`", want: "`${a   + b}`"},
		{name: "nested template", input: "`a${`b${'//'}`}c//` // d", want: "`a${`b${'//'}`}c//` "},
		{name: "object braces inside substitution", input: "`${ {a: 1}.a } // x` // c", want: "`${ {a: 1}.a } // x` "},
		{name: "regex with escaped slashes", input: `x = /\/\// // c`, want: `x = /\/\// `},
		{name: "regex with quote", input: `x = /'/ // c`, want: `x = /'/ `},
		{name: "regex after typeof", input: `typeof /x/ // c`, want: `typeof /x/ `},
		{name: "regex with slash in class", input: `x = /[/]/ // c`, want: `x = /[/]/ `},
		{name: "regex with flags", input: `x = /a/gi // c`, want: `x = /a/gi `},
		{name: "regex at start", input: `/'/.test(s) // c`, want: `/'/.test(s) `},
		{name: "regex after return", input: `return /"/ // c`, want: `return /"/ `},
		{name: "regex after open paren", input: `f(/"/) // c`, want: `f(/"/) `},
		{name: "regex after comma", input: `[1, /"/] // c`, want: `[1, /"/] `},
		{name: "regex after colon", input: `{a: /"/} // c`, want: `{a: /"/} `},
		{name: "division after brace", input: `a } / b / c // d`, want: `a } / b / c `},
		{name: "division after paren", input: `(x) / 2 // d`, want: `(x) / 2 `},
		{name: "division after bracket", input: `a[0] / 2 / 3 // d`, want: `a[0] / 2 / 3 `},
		{name: "division after identifier", input: `a / b / c // d`, want: `a / b / c `},
		{name: "division after number", input: `4 / 2 / 1 // d`, want: `4 / 2 / 1 `},
		{name: "keyword as property is an identifier", input: `a.return / 2 / 3 // d`, want: `a.return / 2 / 3 `},
		{name: "quote inside block comment", input: "'a' /* it's */ b", want: "'a'   b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := stripComments(tt.input)
			if err != nil {
				t.Fatalf("stripComments(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("stripComments(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStripCommentsErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "single-quoted string", input: `a = 'open`, want: "unterminated string"},
		{name: "double-quoted string", input: `a = "open`, want: "unterminated string"},
		{name: "string broken by newline", input: "a = 'open\nb'", want: "unterminated string"},
		{name: "string ends in escape", input: `a = 'open\`, want: "unterminated string"},
		{name: "template", input: "a = `open", want: "unterminated template"},
		{name: "template substitution", input: "a = `x${b", want: "unterminated template"},
		{name: "template substitution under an open object", input: "a = `x${ { a: 1", want: "unterminated template"},
		{name: "block comment", input: "a /* open", want: "unterminated block comment"},
		{name: "regex", input: `a = /open`, want: "unterminated regex"},
		{name: "regex broken by newline", input: "a = /open\n/", want: "unterminated regex"},
		{name: "regex open class", input: `a = /[/`, want: "unterminated regex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := stripComments(tt.input)
			if err == nil {
				t.Fatalf("stripComments(%q) succeeded, want an error", tt.input)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("stripComments(%q) error = %q, want it to contain %q", tt.input, err, tt.want)
			}
		})
	}
}

func TestStringLiterals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "none", input: `a = 1`, want: nil},
		{name: "quote kinds", input: "['a', \"b\", `c`]", want: []string{"a", "b", "c"}},
		{name: "escapes decoded", input: `['it\'s', "a\\b", 'x\ny']`, want: []string{"it's", `a\b`, "x\ny"}},
		{name: "template substitution", input: "`a${'.b'}c`", want: []string{"a", ".b", "c"}},
		{name: "regex is not a literal", input: `x = /'a'/`, want: nil},
		{name: "batuta literal", input: `ignores: ['.batuta/**']`, want: []string{".batuta/**"}},
		{name: "mention only in line comment", input: "a = 1 // .batuta", want: nil},
		{name: "mention only in block comment", input: "/* '.batuta/**' */ a = 1", want: nil},
		{name: "quote inside comment", input: "// it's\n'x'", want: []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stripped, err := stripComments(tt.input)
			if err != nil {
				t.Fatalf("stripComments(%q): %v", tt.input, err)
			}
			got, err := stringLiterals(stripped)
			if err != nil {
				t.Fatalf("stringLiterals(%q): %v", stripped, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("stringLiterals(%q) = %q, want %q", stripped, got, tt.want)
			}
		})
	}
}

func TestKeyValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		key         string
		wantValue   string
		wantLiteral bool
		wantFound   bool
	}{
		{name: "unquoted key", input: `{ testDir: './e2e' }`, key: "testDir", wantValue: "./e2e", wantLiteral: true, wantFound: true},
		{name: "single-quoted key", input: `{ 'testDir': "e2e", other: 1 }`, key: "testDir", wantValue: "e2e", wantLiteral: true, wantFound: true},
		{name: "double-quoted key", input: `{ "testDir": 'e2e' }`, key: "testDir", wantValue: "e2e", wantLiteral: true, wantFound: true},
		{name: "plain template value", input: "{ testDir: `e2e` }", key: "testDir", wantValue: "e2e", wantLiteral: true, wantFound: true},
		{name: "second key", input: `{ a: 1, testDir: 'e2e' }`, key: "testDir", wantValue: "e2e", wantLiteral: true, wantFound: true},
		{name: "expression value", input: `{ testDir: path.join(__dirname, 'e2e') }`, key: "testDir", wantFound: true},
		{name: "identifier value", input: `{ testDir: dir }`, key: "testDir", wantFound: true},
		{name: "concatenation value", input: `{ testDir: 'e2e' + suffix }`, key: "testDir", wantFound: true},
		{name: "template with substitution", input: "{ testDir: `e2e${suffix}` }", key: "testDir", wantFound: true},
		{name: "absent", input: `{ testMatch: 'e2e' }`, key: "testDir"},
		{name: "name inside string", input: `{ a: 'testDir: "x"' }`, key: "testDir"},
		{name: "name inside line comment", input: "{ // testDir: 'x'\n a: 1 }", key: "testDir"},
		{name: "name inside block comment", input: `{ /* testDir: 'x' */ a: 1 }`, key: "testDir"},
		{name: "name as value is not a key", input: `{ a: testDir }`, key: "testDir"},
		{name: "name in ternary is not a key", input: `{ a: x ? testDir : 'y' }`, key: "testDir"},
		{name: "name inside regex", input: `{ a: /testDir: 'x'/ }`, key: "testDir"},
		{name: "longer name does not match", input: `{ testDirs: 'x' }`, key: "testDir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stripped, err := stripComments(tt.input)
			if err != nil {
				t.Fatalf("stripComments(%q): %v", tt.input, err)
			}
			value, literal, found, err := keyValue(stripped, tt.key)
			if err != nil {
				t.Fatalf("keyValue(%q, %q): %v", stripped, tt.key, err)
			}
			if value != tt.wantValue || literal != tt.wantLiteral || found != tt.wantFound {
				t.Fatalf("keyValue(%q, %q) = (%q, %v, %v), want (%q, %v, %v)",
					stripped, tt.key, value, literal, found, tt.wantValue, tt.wantLiteral, tt.wantFound)
			}
		})
	}
}

func TestKeyLiterals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		key   string
		want  []keyOccurrence
	}{
		{
			name:  "array value",
			input: `export default { test: { exclude: ['a', '**/.batuta/**'] } }`,
			key:   "exclude",
			want:  []keyOccurrence{{Literals: []string{"a", "**/.batuta/**"}}},
		},
		{
			name:  "sibling include before is not read",
			input: `{ include: ['.batuta/**'], exclude: ['dist'] }`,
			key:   "exclude",
			want:  []keyOccurrence{{Literals: []string{"dist"}, Siblings: []string{"include"}}},
		},
		{
			name:  "sibling include after is not read",
			input: `{ exclude: ['dist'], include: ['.batuta/**'] }`,
			key:   "exclude",
			want:  []keyOccurrence{{Literals: []string{"dist"}, Siblings: []string{"include"}}},
		},
		{
			name:  "bracketed value runs to its matching bracket",
			input: `{ ignores: [['a', ['b']], f('c', [d])], files: ['e'] }`,
			key:   "ignores",
			want:  []keyOccurrence{{Literals: []string{"a", "b", "c"}, Siblings: []string{"files"}}},
		},
		{
			name:  "spread inside the value",
			input: `{ exclude: [...configDefaults.exclude, '**/.batuta/**'] }`,
			key:   "exclude",
			want:  []keyOccurrence{{Literals: []string{"**/.batuta/**"}}},
		},
		{
			name:  "object with name",
			input: `[{ name: 'ignore', ignores: ['.batuta/**'] }]`,
			key:   "ignores",
			want:  []keyOccurrence{{Literals: []string{".batuta/**"}, Siblings: []string{"name"}}},
		},
		{
			name:  "one occurrence per element",
			input: `[{ ignores: ['dist'] }, { files: ['*.js'], ignores: ['.batuta/**'] }]`,
			key:   "ignores",
			want: []keyOccurrence{
				{Literals: []string{"dist"}},
				{Literals: []string{".batuta/**"}, Siblings: []string{"files"}},
			},
		},
		{
			name:  "nested object with the same key",
			input: `{ ignores: { ignores: ['a'] }, files: [] }`,
			key:   "ignores",
			want: []keyOccurrence{
				{Literals: []string{"a"}, Siblings: []string{"files"}},
				{Literals: []string{"a"}},
			},
		},
		{
			name:  "string value",
			input: `{ testIgnore: '**/.batuta/**', retries: 2 }`,
			key:   "testIgnore",
			want:  []keyOccurrence{{Literals: []string{"**/.batuta/**"}, Siblings: []string{"retries"}}},
		},
		{
			name:  "quoted key",
			input: `{ 'exclude': ["x"], "include": ["y"] }`,
			key:   "exclude",
			want:  []keyOccurrence{{Literals: []string{"x"}, Siblings: []string{"include"}}},
		},
		{
			name:  "spread, shorthand and method siblings",
			input: `{ ...base, files, ignores: ['a'], run() {} }`,
			key:   "ignores",
			want:  []keyOccurrence{{Literals: []string{"a"}, Siblings: []string{"...", "files", "run"}}},
		},
		{
			name:  "key without literals",
			input: `{ exclude: defaults }`,
			key:   "exclude",
			want:  []keyOccurrence{{}},
		},
		{
			name:  "name only in comment",
			input: "{ // exclude: ['.batuta']\n include: [] }",
			key:   "exclude",
		},
		{
			name:  "name only in string",
			input: `{ a: 'exclude: ["x"]' }`,
			key:   "exclude",
		},
		{
			name:  "template value",
			input: "{ exclude: [`a${'.batuta'}`] }",
			key:   "exclude",
			want:  []keyOccurrence{{Literals: []string{"a", ".batuta", ""}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stripped, err := stripComments(tt.input)
			if err != nil {
				t.Fatalf("stripComments(%q): %v", tt.input, err)
			}
			got, err := keyLiterals(stripped, tt.key)
			if err != nil {
				t.Fatalf("keyLiterals(%q, %q): %v", stripped, tt.key, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("keyLiterals(%q, %q) = %#v, want %#v", stripped, tt.key, got, tt.want)
			}
		})
	}
}

func TestCallLiterals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		call  string
		want  []string
	}{
		{name: "array argument", input: `globalIgnores(['.batuta/**'])`, call: "globalIgnores", want: []string{".batuta/**"}},
		{name: "several arguments", input: `globalIgnores(['a'], 'b', ["c"])`, call: "globalIgnores", want: []string{"a", "b", "c"}},
		{name: "several calls", input: `[globalIgnores(['a']), globalIgnores(['b'])]`, call: "globalIgnores", want: []string{"a", "b"}},
		{name: "nested call", input: `globalIgnores([f('a'), g(['b'])])`, call: "globalIgnores", want: []string{"a", "b"}},
		{name: "literals outside the call", input: `x('z'); globalIgnores(['a']); y('w')`, call: "globalIgnores", want: []string{"a"}},
		{name: "other call", input: `defineConfig([{ ignores: ['.batuta/**'] }])`, call: "globalIgnores"},
		{name: "name without call", input: `const globalIgnores = ['.batuta']`, call: "globalIgnores"},
		{name: "longer name does not match", input: `myglobalIgnores(['.batuta'])`, call: "globalIgnores"},
		{name: "name in string", input: `x("globalIgnores(['.batuta'])")`, call: "globalIgnores"},
		{name: "name in comment", input: "// globalIgnores(['.batuta'])\nx()", call: "globalIgnores"},
		{name: "empty call", input: `globalIgnores()`, call: "globalIgnores"},
		{name: "member call", input: `eslint.globalIgnores(['a'])`, call: "globalIgnores", want: []string{"a"}},
		{name: "template argument", input: "globalIgnores([`a${'b'}`])", call: "globalIgnores", want: []string{"a", "b", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stripped, err := stripComments(tt.input)
			if err != nil {
				t.Fatalf("stripComments(%q): %v", tt.input, err)
			}
			got, err := callLiterals(stripped, tt.call)
			if err != nil {
				t.Fatalf("callLiterals(%q, %q): %v", stripped, tt.call, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("callLiterals(%q, %q) = %q, want %q", stripped, tt.call, got, tt.want)
			}
		})
	}
}

func TestStringLiteralEscapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "plain", input: `'.batuta'`, want: []string{".batuta"}},
		{name: "hex", input: `'\x2ebatuta'`, want: []string{".batuta"}},
		{name: "unicode four digits", input: `'\u002ebatuta'`, want: []string{".batuta"}},
		{name: "unicode braces", input: `'\u{2e}batuta'`, want: []string{".batuta"}},
		{name: "unicode braces astral", input: `'\u{1F600}'`, want: []string{"\U0001F600"}},
		{name: "in template", input: "`\\x2ebatuta`", want: []string{".batuta"}},
		{name: "single-character escapes kept", input: `'a\nb\tc\rd\\e'`, want: []string{"a\nb\tc\rd\\e"}},
		{name: "malformed hex keeps the letter", input: `'\xzz'`, want: []string{"xzz"}},
		{name: "short unicode keeps the letter", input: `'\u12'`, want: []string{"u12"}},
		{name: "unclosed braces keep the letter", input: `'\u{2e'`, want: []string{"u{2e"}},
		{name: "out of range braces keep the letter", input: `'\u{110000}'`, want: []string{"u{110000}"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := stringLiterals(tt.input)
			if err != nil {
				t.Fatalf("stringLiterals(%q): %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("stringLiterals(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestKeyLiteralsComputedSibling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []keyOccurrence
	}{
		{
			name:  "computed key before",
			input: `{ [name]: x, ignores: ['a'] }`,
			want:  []keyOccurrence{{Literals: []string{"a"}, Siblings: []string{"[computed]"}}},
		},
		{
			name:  "computed key after",
			input: `{ ignores: ['a'], ['x' + y]: 1 }`,
			want:  []keyOccurrence{{Literals: []string{"a"}, Siblings: []string{"[computed]"}}},
		},
		{
			name:  "computed method",
			input: `{ [name]() {}, ignores: ['a'] }`,
			want:  []keyOccurrence{{Literals: []string{"a"}, Siblings: []string{"[computed]"}}},
		},
		{
			name:  "computed key with a comma",
			input: `{ [0, 'files']: x, ignores: ['a'] }`,
			want:  []keyOccurrence{{Literals: []string{"a"}, Siblings: []string{"[computed]"}}},
		},
		{
			name:  "array value is not a key",
			input: `{ list: [1, 2], ignores: ['a'] }`,
			want:  []keyOccurrence{{Literals: []string{"a"}, Siblings: []string{"list"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := keyLiterals(tt.input, "ignores")
			if err != nil {
				t.Fatalf("keyLiterals(%q): %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("keyLiterals(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCallLiteralsSkipsDeclarations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "function declaration", input: `function globalIgnores(a = '.batuta') {}`},
		{name: "method definition", input: `class A { globalIgnores(a = '.batuta') { return 1 } }`},
		{name: "object method", input: `x = { globalIgnores(a = '.batuta') {} }`},
		{name: "class method with return type", input: `class A { globalIgnores(a = '.batuta'): string[] { return [] } }`},
		{name: "async object method with return type", input: `x = { async globalIgnores(a = '.batuta'): Promise<string[]> { return [] } }`},
		{name: "call in ternary kept", input: `x = c ? globalIgnores(['a']) : {}`, want: []string{"a"}},
		{name: "call after declaration", input: `function globalIgnores(a) {} globalIgnores(['b'])`, want: []string{"b"}},
		{name: "call kept", input: `x = globalIgnores(['a'])`, want: []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := callLiterals(tt.input, "globalIgnores")
			if err != nil {
				t.Fatalf("callLiterals(%q): %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("callLiterals(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

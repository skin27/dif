package impl

import (
	"strings"
	"testing"
	"time"

	"dif/message"
)

// simpleMessage is the message the cases below run on.
func simpleMessage() message.Message {
	m := message.New("you nique it")
	m["inputHeader1"] = "capital"
	m["words"] = "you nique it"
	m["list"] = "A,B,C"
	m["padded"] = "   Hello great big   "
	m["n"] = "11"
	m["Condition"] = "this"
	m["empty"] = ""
	m["json"] = `{"a":[{"b":1},{"b":2}],"name":"x"}`
	return m
}

func evalTemplate(t *testing.T, expr string, m message.Message) (string, error) {
	t.Helper()
	x, err := compileTemplate(expr)
	if err != nil {
		t.Fatalf("compile %q: %v", expr, err)
	}
	return x.eval(m)
}

func TestSimpleLanguageFunctions(t *testing.T) {
	for expr, want := range map[string]string{
		// The requests of the Simple collection.
		"${capitalize(${header.inputHeader1})}":                     "Capital",
		"${capitalize(${body})}":                                    "You Nique It",
		"${concat('Hello','World','|')}":                            "Hello|World",
		"${concat(${body},${header.inputHeader1},'-')}":             "you nique it-capital",
		"${concat('${body}','${header.inputHeader1}','+')}":         "you nique it+capital",
		"${hash(${body},SHA-256)}":                                  "0c1ee2d4fa8807bc67f0f162613855e4899bda9fc2e0846f9a36cf3d542f6a6a",
		"${join(&,id=,${header.list})}":                             "id=A&id=B&id=C",
		"${join(&,word=,${header.list})}":                           "word=A&word=B&word=C",
		"${length('${body}')}":                                      "12",
		"${lowercase('HELLO WORLD')}":                               "hello world",
		"${uppercase(${body})}":                                     "YOU NIQUE IT",
		"${normalizeWhitespace('Hello                World')}":      "Hello World",
		"${pad('Hello world',20,'+')}":                              "Hello world+++++++++",
		"${pad(${body},20,'=')}":                                    "you nique it========",
		"${pad(${body},-20,'=')}":                                   "========you nique it",
		"${replace('Hello','Hi','Hello world')}":                    "Hi world",
		"${replace('you nique','Unique',${body})}":                  "Unique it",
		"${safeQuote('Double Quotes')}":                             `"Double Quotes"`,
		"${safeQuote(${body})}":                                     `"you nique it"`,
		"${substring(0,-2,'Hello World')}":                          "Hello Wor",
		"${substring(2,-2,'Hello World')}":                          "llo Wor",
		"${substring(2,0,'Hello World')}":                           "llo World",
		"${header.words.substring(2)}":                              "u nique it",
		"${sum(10,20,30)}":                                          "60",
		"${sum(${header.n},-${header.n},5)}":                        "5",
		"${trim(${header.padded})}":                                 "Hello great big",
		"${header.padded.trim()}":                                   "Hello great big",
		"${header.padded.trim(${substringBetween('Hello','big')})}": "Hello great big",
		"${body.trim()}":                                            "you nique it",

		// The other functions of Camel's simple language.
		"${substringBefore('a-b-c','-')}":        "a",
		"${substringAfter('a-b-c','-')}":         "b-c",
		"${substringBetween('x[abc]y','[',']')}": "abc",
		"${contains('Hello','ELL')}":             "true",
		"${min(4,2,9)}":                          "2",
		"${max(4,2,9)}":                          "9",
		"${average(2,4,9)}":                      "5",
		"${abs(-5)}":                             "5",
		"${ceil(1.2)}":                           "2",
		"${floor(1.8)}":                          "1",
		"${quote(abc)}":                          `"abc"`,
		"${unquote('abc')}":                      "abc",
		"${size(${header.list})}":                "1",
		"${split(',')}":                          "[you nique it]",
		"${split(${header.list},',')}":           "[A, B, C]",
		"${distinct(${header.list},'B,D')}":      "[A, B, C, D]",
		"${reverse(${header.list})}":             "[C, B, A]",
		"${sort(${header.list},true)}":           "[C, B, A]",
		"${range(4)}":                            "[1, 2, 3]",
		"${hash(abc,MD5)}":                       "900150983cd24fb0d6963f7d28e17f72",
		"${hash(abc,SHA-1)}":                     "a9993e364706816aba3e25717850c26c9cd0d89d",
		"${hash(abc)}":                           "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		"${hash(abc,SHA3-256)}":                  "3a985da74fe225b2045c172d6bd390bd855f086e3e9d525b46bfe24511431532",
		"${empty(String)}":                       "",
		"${empty(list)}":                         "[]",
		"${iif(${header.n} > 10,'big','small')}": "big",
		"${not(${header.n} > 10)}":               "false",
		"${isEmpty(${header.empty})}":            "true",
		"${isNumeric(${header.n})}":              "true",
		"${val('x')}":                            "x",
		"${null}":                                "",
		"${int:header.n}":                        "11",
		"${string:header.n}":                     "11",
		"${boolean:header.n}":                    "",

		// The body, headers and OGNL.
		"${body}":                             "you nique it",
		"${bodyAs(String)}":                   "you nique it",
		"${bodyAs(String).length}":            "12",
		"${body.length()}":                    "12",
		"${in.body}":                          "you nique it",
		"${header.words.toUpperCase()}":       "YOU NIQUE IT",
		"${header.words.replaceAll(\\s+,'')}": "youniqueit",
		"${header.words.replaceAll(' ','-')}": "you-nique-it",
		"${header.words.split(' ')[1]}":       "nique",
		"${header.words.split(' ').size()}":   "3",
		"${header['words']}":                  "you nique it",
		"${header:words}":                     "you nique it",
		"${headers.words}":                    "you nique it",
		"${header.${header.inputHeader1}}":    "",
		"${header.condition}":                 "this", // names are not told apart by case
		"${header.missing}":                   "",
		"${header.missing?.trim()}":           "",
		"${jsonpath($.name)}":                 "",
		"${exchangeProperty.x}":               "", // replaced below by the unsupported case
	} {
		if strings.HasPrefix(expr, "${exchangeProperty") {
			continue
		}
		m := simpleMessage()
		if strings.HasPrefix(expr, "${jsonpath") {
			m[message.Body] = m["json"]
			want = "x"
		}
		got, err := evalTemplate(t, expr, m)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
		} else if got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}

func TestSimpleJSONPathFunction(t *testing.T) {
	m := simpleMessage()
	m[message.Body] = m["json"]
	for expr, want := range map[string]string{
		"${jsonpath($.a.[*].b)}":                "[1, 2]",
		"${empty(String)}${jsonpath($.a[*].b)}": "[1, 2]",
		"result=${jsonpath('$.a[*].b')}":        "result=[1, 2]",
		`${jsonpath("$.name")}`:                 "x",
		"${jsonpath($.missing)}":                "",
		"${jsonpath($.a[*].b, Integer)}":        "",
		"${jsonpath($.a[0].b, Integer)}":        "1",
		"${jsonpath($.a[0].b).toString()}":      "1",
		"${jsonpath($.a[*].b).size()}":          "2",
		"${jsonpath($.name)} ?: 'Geen naam'":    "x",
		"${jsonpath($.nothing)} ?: 'Geen naam'": "Geen naam",
	} {
		got, err := evalTemplate(t, expr, m)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
		} else if got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}

func TestSimpleOperatorsAndTemplates(t *testing.T) {
	for expr, want := range map[string]string{
		// Elvis, chain and the increment of a number.
		"${header.missing} ?: 'fallback'":  "fallback",
		"${header.empty} ?: 'fallback'":    "fallback",
		"${header.n} ?: 'fallback'":        "11",
		"${header.missing} ?: ${header.n}": "11",
		"${header.n}++":                    "12",
		"${header.n}--":                    "10",
		"${header.n}++ and more":           "12 and more",
		"${header.n}+":                     "11+",
		"${normalizeWhitespace(${header.padded})} ~> ${substring(-4)}": "Hello great",
		"${header.words} ~> ${uppercase()} ~> ${substring(4)}":         "NIQUE IT",
		"${header.missing} ?~> ${uppercase()}":                         "",

		// A condition inside a function.
		"${header.n > 10 ? 'Gold' : 'Silver'}":                 "Gold",
		"${header.n > 100 ? 'Gold' : 'Silver'}":                "Silver",
		"${${header.n} > 10 ? 'Gold' : 'Silver'}":              "Gold",
		"${header.n == 11 ? 'a' : header.n > 100 ? 'b' : 'c'}": "a",

		// Text, comparisons are not operators of a template.
		"${int:header.n}  > 10":         "11  > 10",
		"100 > 10":                      "100 > 10",
		"plain text":                    "plain text",
		"a } b":                         "a } b",
		"$notAReference":                "$notAReference",
		"${body}${body}":                "you nique ityou nique it",
		`\n stays`:                      `\n stays`,
		"${replace('\\}','x','a\\}b')}": "axb",
		"${concat('a','b')}":            "ab",
		"${uppercase('Hello ${body}')}": "HELLO YOU NIQUE IT",
	} {
		got, err := evalTemplate(t, expr, simpleMessage())
		if err != nil {
			t.Errorf("%s: %v", expr, err)
		} else if got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}

func TestSimpleConditions(t *testing.T) {
	for expr, want := range map[string]bool{
		"${header.n} == 11":                                               true,
		"${header.n} == '11'":                                             true,
		"${header.n} == 11.0":                                             true,
		"${header.n} != 12":                                               true,
		"${header.n} > 9":                                                 true, // by value, not as text
		"${header.n} >= 11":                                               true,
		"${header.n} < 100":                                               true,
		"${header.n} <= 10":                                               false,
		"${header.words} > 'a'":                                           true,
		"${header.words} =~ 'YOU NIQUE IT'":                               true,
		"${header.words} !=~ 'YOU NIQUE IT'":                              false,
		"${header.words} contains 'nique'":                                true,
		"${header.words} !contains 'nique'":                               false,
		"${header.words} ~~ 'NIQUE'":                                      true,
		"${header.words} !~~ 'NIQUE'":                                     false,
		"${header.words} regex 'you.*'":                                   true,
		"${header.words} regex 'nique'":                                   false, // all of it must match
		"${header.words} !regex 'nique'":                                  true,
		"${header.words} startsWith 'you'":                                true,
		"${header.words} endsWith 'it'":                                   true,
		"${header.words} !startsWith 'it'":                                true,
		"${header.words} !endsWith 'it'":                                  false,
		"${header.n} in '10,11,12'":                                       true,
		"${header.n} !in '1,2'":                                           true,
		"${header.n} range '10..20'":                                      true,
		"${header.n} !range 10..20":                                       false,
		"${header.n} is String":                                           true,
		"${header.n} !is Integer":                                         true,
		"${header.missing} == null":                                       true,
		"${header.empty} == null":                                         false, // an empty header is not nothing
		"${header.missing} == ''":                                         true,
		"${header.missing} != null":                                       false,
		"${header.condition} == 'this'":                                   true,
		"${header.n} > 10 && ${header.words} contains 'you'":              true,
		"${header.n} > 100 && ${header.words} contains 'you'":             false,
		"${header.n} > 100 || ${header.words} contains 'you'":             true,
		"${header.n} > 100 || ${header.n} > 50 || ${header.n} == 11":      true,
		"${header.n} == 1 || ${header.n} == 11 && ${header.words} == 'x'": false, // && binds tighter
		"${header.words} == 'a && b' || true":                             true,
		"${header.words} == 'a || b'":                                     false,
		"${isNumeric(${header.n})}":                                       true,
		"true":                                                            true,
		"false":                                                           false,
		"${iif(${header.n} > 10,true,false)}":                             true,
		"${body} == 'you nique it' && ${header.n}++ == 12":                true,
	} {
		p, err := compilePredicate("simple", expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		got, err := p(simpleMessage())
		if err != nil {
			t.Errorf("%s: %v", expr, err)
		} else if got != want {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
}

func TestSimpleErrors(t *testing.T) {
	for expr, want := range map[string]string{
		"${body":              "unclosed ${",
		"${exchangeId}":       "unsupported simple expression ${exchangeId}",
		"${nosuch(1)}":        "unsupported simple expression ${nosuch(1)}",
		"${bodyAs(String}":    "unsupported simple expression ${bodyAs(String}",
		"${header.}":          "unsupported simple expression",
		"${concat()}":         "valid syntax: ${concat(exp)}",
		"${pad(x)}":           "valid syntax: ${pad(exp,len)}",
		"${substring()}":      "valid syntax: ${substring(num)}",
		"${replace('a')}":     "valid syntax: ${replace(from,to)}",
		"${replace('(','x')}": "regular expression",
		"${hash(x,CRC32)}":    `hash algorithm "CRC32" is not supported`,
		"${empty(thing)}":     "valid syntax: ${empty(<type>)}",
		"${n} ?:":             "",
		"${header.n} ?: ":     "",
		"x \x00 y":            "control character",
	} {
		_, err := compileTemplate(expr)
		if want == "" {
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want containing %q", expr, err, want)
		}
	}

	// Errors when it runs.
	for expr, want := range map[string]string{
		"${header.missing.trim()}":      "there is no value",
		"${substring(x)}":               `"x" is no number`,
		"${header.words.substring(20)}": "outside the text",
		"${header.words.nosuch()}":      "not supported",
		"${bodyAs(Integer)}":            "only String is supported",
	} {
		_, err := evalTemplate(t, expr, simpleMessage())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want containing %q", expr, err, want)
		}
	}
}

func TestFlowExpressionsAreTrimmed(t *testing.T) {
	m := simpleMessage()
	x, err := compileExpression("constant", " you nique it \n")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := x.eval(m); got != "you nique it" {
		t.Errorf("constant = %q", got)
	}
	x, err = compileExpression("simple", "  ${body}  ")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := x.eval(m); got != "you nique it" {
		t.Errorf("simple = %q", got)
	}
	// The body of a message is a template as it is.
	if got, err := evalTemplate(t, "  ${body}  ", m); err != nil || got != "  you nique it  " {
		t.Errorf("template = %q, %v", got, err)
	}
}

func TestSimpleListsRenderAsJava(t *testing.T) {
	if got := render(jlist{"a", int64(2), jlist{"b"}, map[string]any{"k": "v", "a": 1.0}}); got != "[a, 2, [b], {a=1, k=v}]" {
		t.Errorf("render = %q", got)
	}
	if got := render([]any{"a", 1.0}); got != `["a",1]` {
		t.Errorf("a list of the message is JSON: %q", got)
	}
}

const journal = `{"FinancialPeriod":11,"GeneralJournalEntryLines":[` +
	`{"GLAccountCode":801001,"Description":null,"Amount":-92.64},{"GLAccountCode":122001,"Description":"SW230","Amount":10},` +
	`{"GLAccountCode":130000,"Description":null},{"GLAccountCode":122001,"Description":"SW217"}]}`

func TestSimpleJQ(t *testing.T) {
	m := message.New(journal)
	m["Greeting"] = "hello"
	m["person"] = `{"person":{"name":"John Doe","address":{"city":"Anytown"}}}`
	for expr, want := range map[string]string{
		// From the SetHeaders Simple request.
		`${jq('[.GeneralJournalEntryLines[].GLAccountCode] | join(",")')}`:               "801001,122001,130000,122001",
		`${jq('[.GeneralJournalEntryLines[].Description] | join(",")')}`:                 ",SW230,,SW217",
		`${empty(String)}${jq('[.GeneralJournalEntryLines[].Description] | join(",")')}`: ",SW230,,SW217",
		"${jq('.FinancialPeriod')}":                                                          "11",
		"${jq('.FinancialPeriod + 1')}":                                                      "12",
		"${jq('.GeneralJournalEntryLines[0].Amount')}":                                       "-92.64",
		"${jq('.GeneralJournalEntryLines | length')}":                                        "4",
		"${jq('.GeneralJournalEntryLines[1].Description')}":                                  "SW230",
		"${jq('.nothing')}":                                                                  "",
		"${jq('.GeneralJournalEntryLines[] | select(.Amount == 10) | .GLAccountCode')}":      "122001",
		"${jq('.GeneralJournalEntryLines[].GLAccountCode')}":                                 "[801001, 122001, 130000, 122001]",
		"${jq('{count: (.GeneralJournalEntryLines | length), p: .FinancialPeriod\\}')}":      `{"count":4,"p":11}`,
		"${jq('[.GeneralJournalEntryLines[].GLAccountCode]')}":                               "[801001,122001,130000,122001]",
		"${jq('header(\"Greeting\")')}":                                                      "hello",
		"${jq('$headers.Greeting')}":                                                         "hello",
		"${jq('.FinancialPeriod', String)}":                                                  "11",
		"${jq('.FinancialPeriod', Integer)} ok":                                              "11 ok",
		"${jq('\n((.p_11 // (((.p_11A // 0) - (.p_11Vat // 0)) * 100 | round) / 100 ))\n')}": "0",
		"${jq('{wrapper: [.FinancialPeriod, (.fakeKey // {fakeKey: null\\})]\\}')}":          `{"wrapper":[11,{"fakeKey":null}]}`,
	} {
		got, err := evalTemplate(t, expr, m)
		if err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", expr, got, err, want)
		}
	}
	for expr, want := range map[string]string{
		"${jq('.[')}":           "jq program",
		"${jq('error(\"x\")')}": "jq: error: x",
		"${jq()}":               "valid syntax",
	} {
		if x, err := compileTemplate(expr); err == nil {
			if _, err := x.eval(m); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: err = %v, want containing %q", expr, err, want)
			}
		} else if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want containing %q", expr, err, want)
		}
	}
	if _, err := evalTemplate(t, "${jq('.a')}", message.New("not json")); err == nil || !strings.Contains(err.Error(), "body is not JSON") {
		t.Errorf("err = %v", err)
	}
	// A program that never ends is stopped.
	defer func(d time.Duration) { jqTimeout = d }(jqTimeout)
	jqTimeout = 50 * time.Millisecond
	if _, err := evalTemplate(t, "${jq('[repeat(1)]')}", message.New("{}")); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("an endless program: err = %v, want the deadline", err)
	}
}

func TestSimpleFileFunctions(t *testing.T) {
	m := message.New("x")
	m["CamelFileName"] = "in/box/image.tar.gz"
	m["CamelFileParent"] = "/data/in/box"
	m["CamelFileLength"] = int64(2048)
	for expr, want := range map[string]string{
		"${file:name}":                  "in/box/image.tar.gz",
		"${file:name.ext}":              "tar.gz",
		"${file:ext}":                   "tar.gz",
		"${file:name.ext.single}":       "gz",
		"${file:name.noext}":            "in/box/image",
		"${file:name.noext.single}":     "in/box/image.tar",
		"${file:onlyname}":              "image.tar.gz",
		"${file:onlyname.noext}":        "image",
		"${file:onlyname.noext.single}": "image.tar",
		"${file:parent}":                "/data/in/box",
		"${file:length}":                "2048",
		"${file:size}":                  "2048",
		"${file:path}":                  "",
		"${file:absolute}":              "",
		"${file:onlyname.noext}.jpg":    "image.jpg",
	} {
		if got, err := evalTemplate(t, expr, m); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", expr, got, err, want)
		}
	}
	// DIF's file source sets file.name; a message without a file has no file name.
	d := message.New("x")
	d["file.name"] = "sub/a.csv"
	if got, _ := evalTemplate(t, "${file:name} ${file:onlyname} ${file:ext}", d); got != "sub/a.csv a.csv csv" {
		t.Errorf("file.name: %q", got)
	}
	if got, _ := evalTemplate(t, "[${file:name}][${file:onlyname.noext}]", message.New("x")); got != "[][]" {
		t.Errorf("no file: %q", got)
	}
	if _, err := compileTemplate("${file:nothing}"); err == nil || !strings.Contains(err.Error(), "unknown file language syntax") {
		t.Errorf("err = %v", err)
	}
}

// Expressions of the SetHeaders_simple flow that must not make it fail.
func TestSimpleShowcase(t *testing.T) {
	m := message.New(`<products><product><price>500</price></product></products>`)
	m["Amount"] = "1"
	m["code"] = "200"
	m["number"] = "1234567890"
	for expr, want := range map[string]string{
		"${header.Amount * 0.9}":                  "", // not a header, not an operator of the language
		"${iif(${header.code} == 200, OK, NOK)}":  "OK",
		"${iif(${header.code} == 201, OK, NOK)}":  "NOK",
		"${iif(${header.code} == 200, 'Y', 'N')}": "Y",
		"${hash($body,SHA-256)}":                  "18488eabc0e516fe30242e7c16a6b67b897e75bc421dbaedc8534614ae3d6a6e",
		`${xpath("//product[price < 1000]")}`:     "500",
		"${header.number.substring(3,6)}":         "456",
		"${random(100)}":                          "", // only that it compiles
	} {
		got, err := evalTemplate(t, expr, m)
		if err != nil || (want != "" && got != want) {
			t.Errorf("%s = %q, %v; want %q", expr, got, err, want)
		}
	}
}

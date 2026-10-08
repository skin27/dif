package impl

import (
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestFormToXML(t *testing.T) {
	for body, want := range map[string]string{
		// testdata/examples/formToXml.json
		"first-name=Joe&last-name=Foo&age=21": "<form><first-name>Joe</first-name><last-name>Foo</last-name><age>21</age></form>",
		"":                                    "<form></form>",
		"  \n":                                "<form></form>",
		"a=1&&b=2&":                           "<form><a>1</a><b>2</b></form>",
		"a=1&a=2&a=3":                         "<form><a>1</a><a>2</a><a>3</a></form>",
		"empty=&flag":                         "<form><empty/><flag/></form>",
		"q=a+b%26c%3Dd&city=K%C3%B6ln":        "<form><q>a b&amp;c=d</q><city>Köln</city></form>",
		"x=%3Cb%3E&y=a%20%22b%22":             `<form><x>&lt;b&gt;</x><y>a "b"</y></form>`,
		"user name=1&2nd=2&a:b=3":             "<form><user_name>1</user_name><_2nd>2</_2nd><a_b>3</a_b></form>",
		"a%5B0%5D=1&a%5B1%5D=2":               "<form><a_0_>1</a_0_><a_1_>2</a_1_></form>",
	} {
		m := process(t, "formtoxml", nil, message.New(body))
		if m[message.Body] != want || m[message.ContentType] != "application/xml" {
			t.Errorf("%q:\n got %v (%v)\nwant %s", body, m[message.Body], m[message.ContentType], want)
		}
	}

	p := mustProcessor(t, stepdef.Action, "formtoxml", nil).(stepdef.ActionProcessor)
	for body, want := range map[string]string{
		"a=%zz":     `body is not form data: "a=%zz": invalid URL escape "%zz"`,
		"=1":        `body is not form data: "=1": field with no name`,
		"a=1&b=%4":  `body is not form data: "b=%4": invalid URL escape "%4"`,
		"%GG=1&a=1": `body is not form data: "%GG=1": invalid URL escape "%GG"`,
	} {
		if _, err := p.Process(t.Context(), message.New(body)); err == nil || err.Error() != want {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	wantInvalid(t, stepdef.Action, "formtoxml", map[string]any{"rootName": "x"}, "unknown option")
}

package impl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func velocityRun(t *testing.T, tpl string, m message.Message) string {
	t.Helper()
	out := process(t, "velocity", map[string]any{"resource": tpl}, m)
	return out[message.Body].(string)
}

func TestVelocityTemplates(t *testing.T) {
	headers := message.Message{
		"name": "World", "dup": "exact", "DUP": "other", "count": 3, "flag": true, "ratio": 1.5, "raw": []byte("bytes"),
		"list": []any{"a", "b", "c"}, "obj": map[string]any{"city": "Gent", "zip": 9000.0},
	}
	cases := []struct{ name, tpl, want string }{
		{"text", "Hello World", "Hello World"},
		{"reference", "Hello $name!", "Hello World!"},
		{"braces", "${name}s and $name.", "Worlds and World."},
		{"undefined", "$nope ${nope} $!nope|$!{nope}|", "$nope ${nope} ||"},
		{"header property", "$headers.name $headers.count $headers.flag $headers.ratio $headers.raw", "World 3 true 1.5 bytes"},
		{"header get", `$headers.get("name") $headers["name"]`, "World World"},
		{"header names are not told apart by case", `$headers.NAME $headers.get("Name") $headers["nAME"] $headers.containsKey("NAME") $headers.containsKey("none")`, "World World World true false"},
		{"the header as written wins", `$headers.dup`, "exact"},
		{"body", "[$body]", "[the body]"},
		{"in and request", "$in.body $request.headers.name", "the body World"},
		{"nested property", "$headers.obj.city $headers.obj.zip", "Gent 9000.0"},
		{"list", "$headers.list $headers.list[1] $headers.list.size()", "[a, b, c] b 3"},
		{"literals that are not references", "cost: $5, #EEE #endif #foo # $ x", "cost: $5, #EEE #endif #foo # $ x"},
		{"escape", `\$name \#if`, "$name #if"},

		{"set", "#set($x = 5)$x", "5"},
		{"set string", `#set($greeting = "Hi $name")$greeting`, "Hi World"},
		{"set single quotes", `#set($g = 'Hi $name')$g`, "Hi $name"},
		{"set null keeps the value", `#set($x = 1)#set($x = $undefined)$x`, "1"},
		{"arithmetic", "#set($n = 1 + 2 * 3)#set($m = (1 + 2) * 3)#set($d = 10 / 4)#set($r = 10 % 4)$n $m $d $r", "7 9 2 2"},
		{"float arithmetic", "#set($f = 1.5 + 1)#set($g = 7 / 2.0)$f $g", "2.5 3.5"},
		{"negative", "#set($n = -3)#set($m = 5 - -2)$n $m", "-3 7"},
		{"concat", `#set($s = "a" + "b" + 1)$s`, "ab1"},
		{"count plus one", "#set($c = $headers.count + 1)$c", "4"},

		{"if", `#if($headers.count > 2)big#{else}small#end`, "big"},
		{"if false", `#if($headers.count > 5)big#else small#end`, " small"},
		{"elseif", `#if($headers.count == 1)one#elseif($headers.count == 3)three#else other#end`, "three"},
		{"and or not", `#if(!$nope && ($headers.flag || $nope))ok#end`, "ok"},
		{"equals text", `#if($headers.name == "World")same#end`, "same"},
		{"equals number and text", `#if($headers.count == "3")same#end`, "same"},
		{"not equals", `#if($headers.name != "x")different#end`, "different"},
		{"undefined is false", `#if($nope)yes#else no#end`, " no"},
		{"empty string is true", `#if("")yes#end`, "yes"},
		{"false", `#if(false)yes#else no#end`, " no"},
		{"less or equal", `#if(2 <= 2 && 3 >= 3 && 1 < 2)ok#end`, "ok"},
		{"string compare", `#if("a" < "b")ok#end`, "ok"},

		{"foreach range", "#foreach($i in [1..3])$i#if($foreach.hasNext),#end#end", "1,2,3"},
		{"foreach descending", "#foreach($i in [3..1])$i#end", "321"},
		{"foreach list", "#foreach($x in $headers.list)$velocityCount:$x;#end", "1:a;2:b;3:c;"},
		{"foreach literal list", `#foreach($x in ["p", 2, $name])$x.#end`, "p.2.World."},
		{"foreach first last", "#foreach($x in $headers.list)#if($foreach.first)[#end$x#if($foreach.last)]#end#end", "[abc]"},
		{"foreach index count", "#foreach($x in $headers.list)$foreach.index/$foreach.count #end", "0/1 1/2 2/3 "},
		{"foreach map", "#foreach($k in $headers.obj.keySet())$k=$headers.obj.get($k);#end", "city=Gent;zip=9000.0;"},
		{"foreach nested", "#foreach($i in [1..2])#foreach($j in [1..2])$i$j $velocityCount #end#end", "11 1 12 2 21 1 22 2 "},
		{"foreach restores the variable", "#set($i = \"before\")#foreach($i in [1])#end$i", "before"},
		{"foreach over nothing", "#foreach($i in $nope)x#end|", "|"},

		{"methods", `$name.toUpperCase() $name.toLowerCase() $name.length() ${name.substring(1,3)} $name.contains("orl") $name.startsWith("W") $name.endsWith("x") $name.replace("o","0") $name.indexOf("r")`,
			"WORLD world 5 or true true false W0rld 2"},
		{"equals method", `$name.equals("World") $name.equalsIgnoreCase("WORLD") $name.equals("x")`, "true true false"},
		{"trim", `[$padded.trim()]`, "[]"},
		{"is empty", `#if($headers.list.isEmpty())empty#else full#end`, " full"},

		{"line comment", "a\n## nothing here\nb", "a\nb"},
		{"trailing line comment", "a ## c\nb", "a b"},
		{"block comment", "a#* x\n y *#b", "ab"},
		{"unparsed", "#[[ $x #if ]]#", " $x #if "},

		// Lines that hold only a directive leave nothing behind.
		{"directive lines", "#set($a = 1)\n  #if($a == 1)\nyes\n  #else\nno\n  #end\ndone\n", "yes\ndone\n"},
		{"foreach lines", "#foreach($i in [1..2])\nrow $i\n#end\n", "row 1\nrow 2\n"},
		{"indented", "<ul>\n  #foreach($i in [1..2])\n  <li>$i</li>\n  #end\n</ul>", "<ul>\n  <li>1</li>\n  <li>2</li>\n</ul>"},
		{"directive after text keeps the line", "x #set($a = 1)\ny", "x \ny"},
		{"crlf", "#if(true)\r\nyes\r\n#end\r\n", "yes\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := message.New("the body")
			for k, v := range headers {
				m[k] = v
			}
			m["padded"] = "   "
			// $name and $padded are the headers of the same names.
			tpl := c.tpl
			if strings.Contains(tpl, "$name") || strings.Contains(tpl, "{name") || strings.Contains(tpl, "$padded") {
				tpl = "#set($name = $headers.name)#set($padded = $headers.padded)" + tpl
			}
			if got := velocityRun(t, tpl, m); got != c.want {
				t.Errorf("%q\n got %q\nwant %q", c.tpl, got, c.want)
			}
		})
	}
}

// The templates of the platform's flows.
func TestVelocityFixtureTemplate(t *testing.T) {
	const tpl = "#if($headers.YourHeaderName == \"A\")\n    #set($messageBody = \"A\")\n#else\n    #set($messageBody = \"B\")\n#end\n$messageBody"
	for header, want := range map[any]string{"A": "A", "B": "B", nil: "B", 7: "B"} {
		m := message.New("<element><id>abc</id></element>")
		if header != nil {
			m["YourHeaderName"] = header
		}
		if got := velocityRun(t, tpl, m); got != want {
			t.Errorf("header %v: %q, want %q", header, got, want)
		}
	}
}

func TestVelocityRefusesWhatItDoesNotKnow(t *testing.T) {
	for tpl, want := range map[string]string{
		"#macro(x)y#end":                "#macro is not supported",
		"#parse(\"a\")":                 "#parse is not supported",
		"#include(\"a\")":               "#include is not supported",
		"#foreach($i in [1])#break#end": "#break is not supported",
		"#stop":                         "#stop is not supported",
		"$a.getClass()":                 "method getClass() is not supported",
		"#if($a)x":                      "#if is not closed",
		"#if($a)x#else y":               "#else is not closed",
		"#foreach($i in [1])x":          "#foreach is not closed",
		"#end":                          "#end without #if or #foreach",
		"#else":                         "#else without #if or #foreach",
		"#if($a":                        "missing )",
		"#set($a.b = 1)":                "setting a property is not supported",
		"#set(a = 1)":                   "#set wants a reference",
		"#set($a = )":                   "is not an expression",
		"#set($a = \"x)":                "string is not closed",
		"#if(maybe)x#end":               "a word is a reference only with a $",
		"#* open":                       "comment #* is not closed",
		"#[[ open":                      "text #[[ is not closed",
		"#foreach($i of [1])#end":       "#foreach wants",
		"a\nb\n$x.y.length(":            "an expression is missing",
		"$a[1":                          "missing ]",
		"#set($a = [1..])":              "is not an expression",
	} {
		_, err := compileVelocity(tpl)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want containing %q", tpl, err, want)
		}
	}
	_, err := compileVelocity("a\nb\n#macro(x)")
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("err = %v, want the line of the problem", err)
	}
	deep := strings.Repeat("#if(true)", 100) + strings.Repeat("#end", 100)
	if _, err := compileVelocity(deep); err == nil || !strings.Contains(err.Error(), "nested deeper") {
		t.Errorf("deep template: err = %v", err)
	}
}

func TestVelocityStopsRunawayTemplates(t *testing.T) {
	tpl := "#foreach($i in [1..1000000])" + strings.Repeat("0123456789", 10) + "#end"
	p := mustProcessor(t, stepdef.Action, "velocity", map[string]any{"resource": tpl}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("")); err == nil || !strings.Contains(err.Error(), "the result is more than") {
		t.Errorf("err = %v, want the result to be refused", err)
	}
	// A range too large to make is nothing.
	if got := velocityRun(t, "#foreach($i in [1..99999999])x#end|", message.New("")); got != "|" {
		t.Errorf("got %q", got)
	}
}

func TestVelocityOptions(t *testing.T) {
	wantInvalid(t, stepdef.Action, "velocity", nil, "set one of the options resource")
	wantInvalid(t, stepdef.Action, "velocity", map[string]any{"resource": "x", "path": "y"}, "set one of the options resource")
	wantInvalid(t, stepdef.Action, "velocity", map[string]any{"path": filepath.Join(t.TempDir(), "missing.vm")}, "option path")
	wantInvalid(t, stepdef.Action, "velocity", map[string]any{"resource": "#macro(x)#end"}, "#macro is not supported")

	path := filepath.Join(t.TempDir(), "t.vm")
	if err := os.WriteFile(path, []byte("Dear $headers.who"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := message.New("x")
	m["who"] = "me"
	if got := process(t, "velocity", map[string]any{"path": path}, m)[message.Body]; got != "Dear me" {
		t.Errorf("body = %q", got)
	}
}

func TestVelocityConcurrent(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "velocity", map[string]any{"resource": "#foreach($i in [1..3])$headers.who$i#end"}).(stepdef.ActionProcessor)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				m := message.New("")
				m["who"] = "x"
				out, err := p.Process(context.Background(), m)
				if err != nil || out[message.Body] != "x1x2x3" {
					t.Errorf("out = %v, err = %v", out[message.Body], err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

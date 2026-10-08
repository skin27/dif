package impl

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// exampleEDIFACTXML returns the XML body that examples/xmltoedifact.json sets.
func exampleEDIFACTXML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../examples/xmltoedifact.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		DIL struct {
			Integrations struct {
				Integration struct {
					Flows struct {
						Flow struct {
							Steps struct {
								Step []struct {
									URI     string
									Options map[string]any
								}
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, s := range doc.DIL.Integrations.Integration.Flows.Flow.Steps.Step {
		if s.URI == "setbody" {
			return s.Options["expression"].(string)
		}
	}
	t.Fatal("no setbody step in the example")
	return ""
}

func TestXMLToEDIFACTExample(t *testing.T) {
	m := process(t, "xmltoedifact", map[string]any{"edifactType": "d96a"}, message.New(exampleEDIFACTXML(t)))
	got, _ := m[message.Body].(string)
	if m[message.ContentType] != "application/edifact" {
		t.Errorf("Content-Type = %v, want application/edifact", m[message.ContentType])
	}

	segments := strings.Split(strings.TrimSuffix(got, "'"), "'")
	for _, want := range []string{
		"UNB+UNOA:2+8714252014808:14+8714252011517:14+130402:1219+24535",
		"UNH+24546+IFTMIN:D:96A:UN",
		"BGM+340+347605+9",
		"DTM+137:20130402:102",
		"TSR+11+N",
		"FTX+DEL+EXACT DONDERDAG 4.4 TUSSEN 8.15-12.00 UUR:AFLEVEREN",
		"TOD+6+CIP",
		"NAD+DP+DUMMY B.V.+VERLENGDE VOORBEELDWEG 123+AMSTERDAM+1234AB+670",
		"NAD+CZ+DUMMY WIRE & CABLE B.V.+INDUSTRIEGEBIED?: 1234:VOORBEELDWEG 10+AMSTERDAM+1234 AB+670",
		"CTA+IC+JANE DOE",
		"COM+?+31(0)12 3456789:TE", // the + of the number is escaped
	} {
		found := false
		for _, s := range segments {
			found = found || s == want
		}
		if !found {
			t.Errorf("no segment %q in\n%s", want, strings.Join(segments, "\n"))
		}
	}
	if !strings.HasPrefix(got, "UNB+") || strings.ContainsAny(got, "\n\t") {
		t.Errorf("output starts %q, want UNB first and no whitespace between segments", got[:min(20, len(got))])
	}
}

func TestXMLToEDIFACT(t *testing.T) {
	for name, tc := range map[string]struct{ xml, want string }{
		"segments in order": {
			`<m><BGM><a>1</a><b>2</b></BGM><DTM><c><x>3</x><y>4</y></c></DTM></m>`,
			"BGM+1+2'DTM+3:4'",
		},
		"namespaces and wrappers": {
			`<env:u xmlns:env="e"><env:interchangeMessage><env:UNH><env:ref>1</env:ref></env:UNH><x:Message xmlns:x="x"><x:Segment_group_1><x:NAD><c:e1 xmlns:c="c">DP</c:e1></x:NAD></x:Segment_group_1></x:Message></env:interchangeMessage></env:u>`,
			"UNH+1'NAD+DP'",
		},
		"the root may be a segment": {`<BGM><a>1</a></BGM>`, "BGM+1'"},
		"empty elements keep their place": {
			`<m><BGM><a>1</a><b/><c>3</c><d></d></BGM></m>`,
			"BGM+1++3'", // the trailing empty element is dropped
		},
		"empty components": {
			`<m><DTM><c><x>1</x><y/><z>3</z></c><d><x/><y/></d></DTM></m>`,
			"DTM+1::3'", // an element of only empty components is empty, and trailing
		},
		"a segment with no elements": {`<m><UNA/><XYZ/></m>`, "UNA'XYZ'"},
		"escapes": {
			`<m><FTX><a>it's</a><c><x>a:b</x><y>c+d?e</y></c></FTX></m>`,
			"FTX+it?'s+a?:b:c?+d??e'",
		},
		"text is kept as it is": {`<m><FTX><a> a  b </a></FTX></m>`, "FTX+ a  b '"},
		"digits in a tag":       {`<m><X12><a>1</a></X12></m>`, "X12+1'"},
	} {
		m := process(t, "xmltoedifact", nil, message.New(tc.xml))
		if m[message.Body] != tc.want {
			t.Errorf("%s:\n got %v\nwant %s", name, m[message.Body], tc.want)
		}
	}
}

func TestXMLToEDIFACTErrors(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "xmltoedifact", nil).(stepdef.ActionProcessor)
	for body, want := range map[string]string{
		"UNB+UNOA'": "body is not XML",
		"<m><Segment_group_1><bgm>1</bgm><Xy>2</Xy></Segment_group_1></m>": "no EDIFACT segment in the XML",
		"<m/>": "no EDIFACT segment in the XML",
		"<m><BGM><c><x><deep>1</deep></x></c></BGM></m>": "segment BGM: element <c>: component <x> has children",
	} {
		if _, err := p.Process(t.Context(), message.New(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want containing %q", body, err, want)
		}
	}
	wantInvalid(t, stepdef.Action, "xmltoedifact", map[string]any{"unknown": "x"}, "unknown option")
}

func TestIsSegmentTag(t *testing.T) {
	for tag, want := range map[string]bool{"BGM": true, "UNB": true, "X12": true, "": false, "BG": false, "BGMM": false, "bgm": false, "1GM": false, "B_M": false, "IFTMIN": false} {
		if got := isSegmentTag(tag); got != want {
			t.Errorf("isSegmentTag(%q) = %v, want %v", tag, got, want)
		}
	}
}

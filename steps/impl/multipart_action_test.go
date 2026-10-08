package impl

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestMultipart(t *testing.T) {
	m := message.New("<a/>")
	m[message.ContentType] = "application/xml"
	m[FileName] = "order.xml"
	m = process(t, "multipart", map[string]any{"fname": "file", "formFields": `{"b": "two", "a": 1}`}, m)

	mt, params, err := mime.ParseMediaType(m[message.ContentType].(string))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %v, %v", m[message.ContentType], err)
	}
	r := multipart.NewReader(bytes.NewReader(m[message.Body].([]byte)), params["boundary"])
	var got []string
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		got = append(got, p.FormName()+"|"+p.FileName()+"|"+p.Header.Get("Content-Type")+"|"+string(data))
	}
	want := []string{"file|order.xml|application/xml|<a/>", "b|||two", "a|||1"}
	if len(got) != len(want) {
		t.Fatalf("parts = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %q, want %q", i, got[i], want[i])
		}
	}

	// A multipart Content-Type from before (as in examples/multipart.json) does not describe the body.
	m = message.New("x")
	m[message.ContentType] = "multipart/form-data"
	m = process(t, "multipart", map[string]any{"fname": "SecondPart"}, m)
	if !bytes.Contains(m[message.Body].([]byte), []byte("Content-Type: application/octet-stream")) {
		t.Errorf("body = %s", m[message.Body])
	}

	wantInvalid(t, stepdef.Action, "multipart", nil, "missing required option fname")
	wantInvalid(t, stepdef.Action, "multipart", map[string]any{"fname": "f", "formFields": "[1]"}, "option formFields: want a JSON object")
}

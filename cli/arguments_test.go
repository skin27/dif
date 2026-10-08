package cli

import (
	"reflect"
	"testing"
)

func TestUtilityArguments(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{`"C:\flows with spaces\hello.json" --output json`, []string{`C:\flows with spaces\hello.json`, "--output", "json"}},
		{`'D:\flows\hello.json' --id='my flow'`, []string{`D:\flows\hello.json`, "--id=my flow"}},
		{`"" --template timer`, []string{"", "--template", "timer"}},
	} {
		got, err := utilityArguments(tc.line)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: %v, %v; want %v", tc.line, got, err, tc.want)
		}
	}
	if _, err := utilityArguments(`"unfinished`); err == nil {
		t.Fatal("accepted unclosed quote")
	}
}

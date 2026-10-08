package impl

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The rows a statement returns are written as the platform writes them:
//
//	<ResultSet><ResultSize>2</ResultSize><Results>
//	  <Result><id>1</id><name>Kees</name></Result>
//	  <Result>...</Result>
//	</Results></ResultSet>
//
// with an element for each column, named by the column, and an empty element
// for NULL. The platform's component is not open: this is what its flows' expected
// answers show (ResultSet/ResultSize, ResultSet/Results/Result/<column>).

// sqlResultXML writes the rows of rs, and returns the number of them. It stops
// with an error when the result is more than maxBodySize.
func sqlResultXML(rs *sql.Rows) (string, int, error) {
	cols, err := rs.ColumnTypes()
	if err != nil {
		return "", 0, err
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = sqlElementName(c.Name())
	}
	var rows strings.Builder
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	n := 0
	for rs.Next() {
		if err := rs.Scan(ptrs...); err != nil {
			return "", 0, err
		}
		rows.WriteString("<Result>")
		for i, v := range values {
			if v == nil {
				rows.WriteString("<" + names[i] + "/>")
				continue
			}
			rows.WriteString("<" + names[i] + ">" + xmlEscaper.Replace(xmlSafe(sqlText(v, cols[i].DatabaseTypeName()))) + "</" + names[i] + ">")
		}
		rows.WriteString("</Result>")
		if n++; rows.Len() > maxBodySize {
			return "", 0, fmt.Errorf("the result is more than %d bytes", maxBodySize)
		}
	}
	if err := rs.Err(); err != nil {
		return "", 0, err
	}
	results := "<Results/>"
	if n > 0 {
		results = "<Results>" + rows.String() + "</Results>"
	}
	return "<ResultSet><ResultSize>" + strconv.Itoa(n) + "</ResultSize>" + results + "</ResultSet>", n, nil
}

// sqlElementName makes an element name of a column name: what an XML name
// cannot hold is left out (so version() is version), and a name that is empty or
// starts with a digit, a hyphen or a dot gets an underscore in front.
func sqlElementName(col string) string {
	var b strings.Builder
	for _, r := range col {
		if r == '_' || r == '-' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" {
		return "_"
	}
	if r, _ := utf8.DecodeRuneInString(name); !(r == '_' || unicode.IsLetter(r)) {
		name = "_" + name
	}
	if strings.HasPrefix(strings.ToLower(name), "xml") {
		name = "_" + name
	}
	return name
}

// xmlSafe leaves out the characters XML cannot hold.
func xmlSafe(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return !validXMLChar(r) }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if validXMLChar(r) {
			return r
		}
		return -1
	}, s)
}

func validXMLChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD && r != 0xFFFE || r >= 0x10000 && r <= 0x10FFFF
}

// sqlText writes a value of a column the way a JDBC driver's getString does,
// more or less: numbers as numbers, dates as 2006-01-02 and moments as
// 2006-01-02 15:04:05.999999999, binary data that is no text as base64.
func sqlText(v any, dbType string) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		if utf8.Valid(x) {
			return string(x)
		}
		return base64.StdEncoding.EncodeToString(x)
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case time.Time:
		if strings.EqualFold(dbType, "DATE") && x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return x.Format("2006-01-02")
		}
		return x.Format("2006-01-02 15:04:05.999999999")
	}
	return fmt.Sprint(v)
}

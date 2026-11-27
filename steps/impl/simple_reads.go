package impl

import (
	"strings"
)

// SimpleHeaderReads returns the headers a simple template expression reads,
// for static flow checks such as flow-level partitionBy expressions.
func SimpleHeaderReads(expr string) [][]string {
	return headerReads(expr, false)
}

// headerReads returns the headers a simple expression reads, for the static
// checks of a flow: for each ${header.x} reference with a fixed name, the
// names it may read (${header.a.b} reads the header a.b, else a). A condition
// is compiled as content and filter compile theirs, anything else as a
// template. It is nil when the expression does not compile; loading the flow
// reports that.
func headerReads(expr string, condition bool) [][]string {
	var reads [][]string
	c := &compiler{reads: &reads}
	expr = strings.TrimSpace(expr)
	if condition {
		b, err := pseudoBlock(expr)
		if err != nil {
			return nil
		}
		if _, err := c.condition(b, b.body); err != nil {
			return nil
		}
		return reads
	}
	if _, err := c.compile(expr); err != nil {
		return nil
	}
	return reads
}

// readsIn returns the headers an expression in language reads: a simple one,
// or an xpath or jsonpath with ${...} in it, which is a simple template.
func readsIn(language, expr string, condition bool) [][]string {
	switch language {
	case "simple":
		return headerReads(expr, condition)
	case "xpath", "jsonpath":
		if strings.Contains(expr, "${") {
			return headerReads(expr, false)
		}
	}
	return nil
}

// recordRead records a reference to a header that may have each of names, when
// the compiler collects them. A name that is computed (it holds a block) or
// that is no plain name (such as a.trim(), which is tried before a) is left
// out, so only headers that are really read are recorded.
func (c *compiler) recordRead(names ...string) {
	if c.reads == nil {
		return
	}
	var read []string
	for _, s := range names {
		s = strings.NewReplacer("'", "", `"`, "").Replace(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if strings.ContainsAny(s, " \t\r\n${}()[]"+string([]rune{phOpen, phClose})) || strings.HasPrefix(s, ".") {
			continue
		}
		read = append(read, s)
	}
	if len(read) > 0 {
		*c.reads = append(*c.reads, read)
	}
}

package mapping

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Check size/depth and cancellation before handing source to the XSLT library.
// Comments and ordinary processing instructions are valid stylesheet source;
// document type/entity declarations are not part of this adapter.
func checkStylesheet(src string, b *budget) error {
	d := xml.NewDecoder(strings.NewReader(src))
	depth := 0
	for {
		if e := b.tick(); e != nil {
			return e
		}
		t, e := d.Token()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return fmt.Errorf("invalid XSLT XML")
		}
		switch t.(type) {
		case xml.StartElement:
			depth++
			if e := b.node(depth); e != nil {
				return e
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return fmt.Errorf("XSLT document type declarations are unsupported")
		}
	}
}

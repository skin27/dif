// Package xsltutil provides pure, resolver-free stylesheet compilation.
package xsltutil

import (
	"fmt"
	"github.com/knroy/go-xml/xdm"
	"github.com/knroy/go-xml/xslt"
	"strings"
)

func Compile(src string) (*xslt.Stylesheet, error) {
	tree, err := xdm.ParseString(src, xdm.ParseOptions{})
	if err != nil {
		return nil, fmt.Errorf("stylesheet is not XML: %s", firstLine(err))
	}
	sheet, err := xslt.Compile(tree.Root, xslt.CompileOptions{})
	if err != nil {
		return nil, fmt.Errorf("stylesheet: %s", firstLine(err))
	}
	return sheet, nil
}
func firstLine(err error) string { s, _, _ := strings.Cut(err.Error(), "\n"); return s }

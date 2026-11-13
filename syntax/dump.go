package syntax

import (
	"fmt"
	"reflect"
	"strings"
)

var (
	spanType = reflect.TypeOf(Span{})
	posType  = reflect.TypeOf(Pos(0))
)

// Dump renders any AST node as an indented tree, without positions and
// without zero-valued fields; it is the golden form of the tests and the
// equality used by the print/parse round trip.
func Dump(n any) string {
	var b strings.Builder
	dump(&b, reflect.ValueOf(n), 0)
	b.WriteByte('\n')
	return b.String()
}

// DumpFile dumps the units of a file.
func DumpFile(f *File) string {
	var b strings.Builder
	for _, u := range f.Units {
		dump(&b, reflect.ValueOf(u), 0)
		b.WriteByte('\n')
	}
	return b.String()
}

func dump(b *strings.Builder, v reflect.Value, ind int) {
	pad := func(n int) {
		for i := 0; i < n; i++ {
			b.WriteString("  ")
		}
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		dump(b, v.Elem(), ind)
	case reflect.Struct:
		b.WriteString(v.Type().Name())
		b.WriteString(" {")
		first := true
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			// Orig (the original spelling of a name) is not part of the dump:
			// the printer writes canonical lower case
			if f.Type == spanType || f.Type == posType || !f.IsExported() || f.Name == "Orig" {
				continue
			}
			fv := v.Field(i)
			if f.Name == "StmtBase" || f.Name == "Span" {
				// embedded: show label and name only
				if f.Name == "StmtBase" {
					sb := fv.Interface().(StmtBase)
					if sb.Label != 0 {
						b.WriteString(fmt.Sprintf("\n%*sLabel: %d", (ind+1)*2, "", sb.Label))
						first = false
					}
					if sb.Name != "" {
						b.WriteString(fmt.Sprintf("\n%*sName: %q", (ind+1)*2, "", sb.Name))
						first = false
					}
				}
				continue
			}
			if fv.IsZero() {
				continue
			}
			first = false
			b.WriteByte('\n')
			pad(ind + 1)
			b.WriteString(f.Name)
			b.WriteString(": ")
			dump(b, fv, ind+1)
		}
		if !first {
			b.WriteByte('\n')
			pad(ind)
		}
		b.WriteByte('}')
	case reflect.Slice:
		if v.Len() == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			b.WriteByte('\n')
			pad(ind + 1)
			dump(b, v.Index(i), ind+1)
		}
		b.WriteByte('\n')
		pad(ind)
		b.WriteByte(']')
	case reflect.Array:
		fmt.Fprintf(b, "%v", v.Interface())
	case reflect.String:
		fmt.Fprintf(b, "%q", v.String())
	default:
		fmt.Fprintf(b, "%v", v.Interface())
	}
}

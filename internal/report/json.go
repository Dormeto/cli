// The JSON report: ports jsonReport of manager packages/estimate/src/report.ts, JSON.stringify(e, null, 2) and a newline,
// with "tool" added first (the only difference from Node's by design).
package report

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/estimate"
)

type tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// withTool writes "tool" before the estimate's own fields (the embedded struct's fields are inlined, in their order).
type withTool struct {
	Tool tool `json:"tool"`
	estimate.Estimate
}

// JSON is the estimate as JSON.stringify(e, null, 2) writes it, a newline after, with "tool": {"name": "dormeto",
// "version": version} first. A number JSON cannot hold (NaN, infinity) is null, as JavaScript writes it. It returns nil
// only if the estimate holds a value no JSON can (a channel; none does).
func JSON(e estimate.Estimate, version string) []byte {
	report := withTool{Tool: tool{Name: "dormeto", Version: version}, Estimate: e}
	b, err := encode(report)
	if err != nil {
		// A number JSON cannot hold (NaN, infinity): null, as JSON.stringify writes it.
		var tree any
		if tree, err = treeOf(reflect.ValueOf(report)); err == nil {
			b, err = encode(tree)
		}
	}
	if err != nil {
		return nil
	}
	return unescapeLineSeparators(b)
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(v)
	return buf.Bytes(), err
}

// unescapeLineSeparators writes U+2028 and U+2029 as they are, as JSON.stringify does (Go escapes them as a backslash, "u" and
// 2028 or 2029). Each backslash starts an escape, so a text backslash followed by the same letters (written as two
// backslashes) is left alone.
func unescapeLineSeparators(b []byte) []byte {
	lineSeparator, paragraphSeparator := []byte("\\"+"u2028"), []byte("\\"+"u2029")
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			out = append(out, b[i])
			continue
		}
		switch rest := b[i:]; {
		case bytes.HasPrefix(rest, lineSeparator):
			out = append(out, string(rune(0x2028))...)
			i += len(lineSeparator) - 1
		case bytes.HasPrefix(rest, paragraphSeparator):
			out = append(out, string(rune(0x2029))...)
			i += len(paragraphSeparator) - 1
		default:
			// The escape's own second character is not a start.
			out = append(out, b[i])
			if i+1 < len(b) {
				i++
				out = append(out, b[i])
			}
		}
	}
	return out
}

// The estimate as a tree of JSON values, when the encoder refuses it: a non-finite number is null there, as
// JSON.stringify writes it. Same names, order and omitempty as the encoder's; Ordered objects keep their keys' order;
// a type with its own MarshalJSON is encoded by it.
type object struct {
	keys   []string
	values []any
}

func (o object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		value, err := json.Marshal(o.values[i])
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

var marshaler = reflect.TypeFor[json.Marshaler]()

func treeOf(v reflect.Value) (any, error) {
	switch v.Kind() {
	case reflect.Invalid:
		return nil, nil
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, nil
		}
		if v.Type().Implements(marshaler) {
			return rawOf(v)
		}
		return treeOf(v.Elem())
	}
	if strings.HasPrefix(v.Type().Name(), "Ordered[") {
		var o object
		for _, k := range v.MethodByName("Keys").Call(nil)[0].Interface().([]string) {
			got := v.MethodByName("Get").Call([]reflect.Value{reflect.ValueOf(k)})[0]
			value, err := treeOf(got)
			if err != nil {
				return nil, err
			}
			o.keys, o.values = append(o.keys, k), append(o.values, value)
		}
		return o, nil
	}
	if v.Type().Implements(marshaler) {
		return rawOf(v)
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		if f := v.Float(); math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, nil
		}
		return v.Interface(), nil
	case reflect.Struct:
		var o object
		return o, fieldsOf(v, &o)
	case reflect.Slice:
		if v.IsNil() {
			return nil, nil
		}
		items := make([]any, v.Len())
		for i := range items {
			item, err := treeOf(v.Index(i))
			if err != nil {
				return nil, err
			}
			items[i] = item
		}
		return items, nil
	case reflect.Map:
		if v.IsNil() {
			return nil, nil
		}
		var o object
		keys := v.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		for _, k := range keys {
			value, err := treeOf(v.MapIndex(k))
			if err != nil {
				return nil, err
			}
			o.keys, o.values = append(o.keys, k.String()), append(o.values, value)
		}
		return o, nil
	}
	return v.Interface(), nil
}

func rawOf(v reflect.Value) (any, error) {
	b, err := json.Marshal(v.Interface())
	return json.RawMessage(b), err
}

// fieldsOf adds a struct's fields as the encoder names them: embedded structs inlined, "-" skipped, omitempty honoured.
func fieldsOf(v reflect.Value, o *object) error {
	for i := 0; i < v.NumField(); i++ {
		f, fv := v.Type().Field(i), v.Field(i)
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if tag == "-" || (!f.IsExported() && !f.Anonymous) {
			continue
		}
		if f.Anonymous && name == "" && fv.Kind() == reflect.Struct {
			if err := fieldsOf(fv, o); err != nil {
				return err
			}
			continue
		}
		if name == "" {
			name = f.Name
		}
		if strings.Contains(opts, "omitempty") && fv.IsZero() {
			continue
		}
		value, err := treeOf(fv)
		if err != nil {
			return err
		}
		o.keys, o.values = append(o.keys, name), append(o.values, value)
	}
	return nil
}

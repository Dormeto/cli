// An object whose keys keep the order they came in: Node's byAccount, byRegion and accountNames are objects, written
// (and shown in the reports) in their insertion order, which a Go map loses.
package estimate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// Ordered is a JSON object whose keys keep the order they were first set in, as a JavaScript object's (none of its
// keys is an array index, which JavaScript would put first: account ids have 12 digits, above 2^32, and regions are
// names). The zero value is empty.
type Ordered[V any] struct {
	keys   []string
	values map[string]V
}

// Set sets a key's value; a new key goes last, a known one keeps its place.
func (o *Ordered[V]) Set(key string, v V) {
	if o.values == nil {
		o.values = map[string]V{}
	}
	if _, known := o.values[key]; !known {
		o.keys = append(o.keys, key)
	}
	o.values[key] = v
}

// Get is a key's value.
func (o Ordered[V]) Get(key string) (V, bool) {
	v, ok := o.values[key]
	return v, ok
}

// Keys are the keys, in order.
func (o Ordered[V]) Keys() []string { return slices.Clone(o.keys) }

// Len is the number of keys.
func (o Ordered[V]) Len() int { return len(o.keys) }

// MarshalJSON writes the object, its keys in order.
func (o Ordered[V]) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(o.values[k])
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

// UnmarshalJSON reads an object, keeping its keys' order.
func (o *Ordered[V]) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("estimate: an object is expected, not %.40s", b)
	}
	out := Ordered[V]{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		var v V
		if err := dec.Decode(&v); err != nil {
			return err
		}
		out.Set(key.(string), v)
	}
	*o = out
	return nil
}

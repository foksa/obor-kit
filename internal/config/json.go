package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Ordered is a JSON object of V that keeps the config's key order: kinds, models, commands and agents are listed
// in the order the config names them (merge keeps a known key's place and appends a new one).
type Ordered[V any] struct {
	Keys []string
	M    map[string]V
}

func (o *Ordered[V]) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*o = Ordered[V]{}
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("%s: must be an object", o.name())
	}
	*o = Ordered[V]{M: map[string]V{}}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		k := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		var v V
		if err := strict(raw, &v); err != nil {
			return fmt.Errorf("%s.%s: %s", o.name(), k, strings.TrimPrefix(err.Error(), "json: "))
		}
		if _, seen := o.M[k]; !seen {
			o.Keys = append(o.Keys, k)
		}
		o.M[k] = v
	}
	return nil
}

// name is the config key holding an Ordered of V, for errors (encoding/json adds no context to an Unmarshaler's).
func (o *Ordered[V]) name() string {
	switch any(*new(V)).(type) {
	case KindEntry:
		return "kinds"
	case ModelEntry:
		return "models"
	case Command:
		return "commands"
	case RoleEntry:
		return "roles"
	}
	return "agents"
}

func (o Ordered[V]) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.Keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(Compact(k) + ":" + Compact(o.M[k]))
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// List is a JSON array decoded fresh (encoding/json would decode into the old elements).
type List[T any] []T

func (l *List[T]) UnmarshalJSON(b []byte) error {
	var v []T
	err := strict(b, &v)
	*l = v
	return err
}

// member is one key of a JSON object, in file order.
type member struct {
	Key string
	Val json.RawMessage
}

// object is b's members in order (a repeated key keeps its first place, the last value); ok false when b is no object.
func object(b []byte) ([]member, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	var out []member
	at := map[string]int{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		k := t.(string)
		if i, ok := at[k]; ok {
			out[i].Val = v
			continue
		}
		at[k] = len(out)
		out = append(out, member{k, v})
	}
	return out, true
}

// merge is jq's a * b: two objects merge key by key (recursively, a's keys first, then b's new ones); anything else in
// b (null, a scalar, a list) replaces a.
func merge(a, b json.RawMessage) json.RawMessage {
	ao, aok := object(a)
	bo, bok := object(b)
	if !aok || !bok {
		return b
	}
	for _, m := range bo {
		i := 0
		for i < len(ao) && ao[i].Key != m.Key {
			i++
		}
		if i == len(ao) {
			ao = append(ao, m)
		} else {
			ao[i].Val = merge(ao[i].Val, m.Val)
		}
	}
	return joinMembers(ao)
}

// strict decodes one JSON value into v: unknown keys are errors, numbers in an any stay json.Number.
func strict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// Compact is v as one line of JSON (no HTML escaping).
func Compact(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "null"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// Pretty is v as JSON indented by two spaces, with a trailing newline.
func Pretty(v any) string {
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(Compact(v)), "", "  "); err != nil {
		return "null\n"
	}
	return b.String() + "\n"
}

// valueTyper is implemented by Ordered: the type of its values, for the schema walk.
type valueTyper interface {
	valueType() reflect.Type
}

func (Ordered[V]) valueType() reflect.Type { return reflect.TypeFor[V]() }

// dropUnknown is b without the object members the destination type t does not know, and their dotted paths (list
// elements as name[i]). It walks b against the type, so two members of one name are told apart by where they sit. A
// value of the wrong shape is left as it is: the strict decode reports it.
func dropUnknown(b json.RawMessage, t reflect.Type, path string) (json.RawMessage, []string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if vt, ok := reflect.Zero(t).Interface().(valueTyper); ok {
		return dropInMembers(b, path, func(string) (reflect.Type, bool) { return vt.valueType(), true })
	}
	switch t.Kind() {
	case reflect.Struct:
		return dropInMembers(b, path, func(key string) (reflect.Type, bool) { return fieldType(t, key) })
	case reflect.Map:
		return dropInMembers(b, path, func(string) (reflect.Type, bool) { return t.Elem(), true })
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(b, &items) != nil {
			return b, nil
		}
		var found []string
		for i, it := range items {
			var f []string
			items[i], f = dropUnknown(it, t.Elem(), fmt.Sprintf("%s[%d]", path, i))
			found = append(found, f...)
		}
		if found == nil {
			return b, nil
		}
		out, _ := json.Marshal(items)
		return out, found
	}
	return b, nil
}

// dropInMembers drops the members of the object b that typeOf does not know and recurses into the others.
func dropInMembers(b json.RawMessage, path string, typeOf func(key string) (reflect.Type, bool)) (json.RawMessage, []string) {
	ms, ok := object(b)
	if !ok {
		return b, nil
	}
	var found []string
	kept := ms[:0]
	for _, m := range ms {
		at := m.Key
		if path != "" {
			at = path + "." + m.Key
		}
		ft, known := typeOf(m.Key)
		if !known {
			found = append(found, at)
			continue
		}
		var f []string
		m.Val, f = dropUnknown(m.Val, ft, at)
		found = append(found, f...)
		kept = append(kept, m)
	}
	if found == nil {
		return b, nil
	}
	return joinMembers(kept), found
}

// fieldType is the type of t's field that encoding/json fills from key.
func fieldType(t reflect.Type, key string) (reflect.Type, bool) {
	f, ok := fieldOf(t, key)
	return f.Type, ok
}

// fieldOf is t's field that encoding/json fills from key (its tag name, else the field name; exact match first, then
// case-insensitive, as the decoder does).
func fieldOf(t reflect.Type, key string) (reflect.StructField, bool) {
	var fold reflect.StructField
	found := false
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if name == key {
			return f, true
		}
		if !found && strings.EqualFold(name, key) {
			fold, found = f, true
		}
	}
	return fold, found
}

func joinMembers(ms []member) json.RawMessage {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(Compact(m.Key) + ":")
		out.Write(m.Val)
	}
	out.WriteByte('}')
	return out.Bytes()
}

// EditJSON is the JSON object doc with the member at path set to val (a json.RawMessage as it is, anything else
// marshalled), creating the objects on the way; a nil val (or an empty json.RawMessage) deletes it and every object it
// leaves empty. Every other member keeps its place and bytes; a new one goes last. An empty doc or a null on the way is
// an empty object; any other non-object on the way is an error. The result is compact where it changed.
func EditJSON(doc []byte, path []string, val any) ([]byte, error) {
	var raw json.RawMessage
	switch v := val.(type) {
	case nil:
	case json.RawMessage:
		raw = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	if len(path) == 0 {
		return nil, errors.New("no key")
	}
	return editAt(doc, path, 0, raw)
}

func editAt(doc []byte, path []string, i int, val json.RawMessage) ([]byte, error) {
	var ms []member
	if t := bytes.TrimSpace(doc); len(t) > 0 && string(t) != "null" {
		var ok bool
		if ms, ok = object(t); !ok {
			if i == 0 {
				return nil, errors.New("not a JSON object")
			}
			return nil, fmt.Errorf("%s is not an object", strings.Join(path[:i], "."))
		}
	}
	at := 0
	for at < len(ms) && ms[at].Key != path[i] {
		at++
	}
	v := val
	if i < len(path)-1 {
		var cur []byte
		if at < len(ms) {
			cur = ms[at].Val
		}
		child, err := editAt(cur, path, i+1, val)
		if err != nil {
			return nil, err
		}
		if v = child; val == nil && string(child) == "{}" {
			v = nil
		}
	}
	switch {
	case v == nil && at < len(ms):
		ms = append(ms[:at], ms[at+1:]...)
	case v == nil:
	case at < len(ms):
		ms[at].Val = v
	default:
		ms = append(ms, member{path[i], v})
	}
	return joinMembers(ms), nil
}

// MemberAt is the member of the JSON object doc at path (nil: none); an object on the way that is not one is an error,
// as in EditJSON.
func MemberAt(doc []byte, path []string) (json.RawMessage, error) {
	cur := json.RawMessage(doc)
	for i, k := range path {
		if t := bytes.TrimSpace(cur); len(t) == 0 || string(t) == "null" {
			return nil, nil
		}
		ms, ok := object(cur)
		if !ok {
			if i == 0 {
				return nil, errors.New("not a JSON object")
			}
			return nil, fmt.Errorf("%s is not an object", strings.Join(path[:i], "."))
		}
		cur = nil
		for _, m := range ms {
			if m.Key == k {
				cur = m.Val
			}
		}
		if cur == nil {
			return nil, nil
		}
	}
	return cur, nil
}

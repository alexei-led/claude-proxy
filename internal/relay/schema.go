package relay

import (
	"bytes"
	"encoding/json"
)

// Server-key providers validate tool schemas with their own regex dialects and reject
// some patterns Anthropic accepts: DeepSeek fails the whole request on Claude Code's
// `^[^\0]*$`. A pattern only constrains generated arguments, so the relay drops every
// `pattern` keyword from tool input schemas on those routes. OAuth routes keep the
// original bytes. Everything outside the rewritten schemas keeps its bytes and order.

type member struct {
	key   string
	value json.RawMessage
}

var (
	// Keywords whose value is one subschema, or a list of them for `items` in older drafts.
	subschemaKeys = map[string]bool{
		"items": true, "additionalItems": true, "additionalProperties": true, "not": true,
		"if": true, "then": true, "else": true, "contains": true, "propertyNames": true,
		"unevaluatedItems": true, "unevaluatedProperties": true,
	}
	subschemaListKeys = map[string]bool{"anyOf": true, "oneOf": true, "allOf": true, "prefixItems": true}
	subschemaMapKeys  = map[string]bool{
		"properties": true, "patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true,
	}
)

// stripToolPatterns returns body unchanged unless a tool input schema held a pattern.
func stripToolPatterns(body []byte) []byte {
	members, ok := objectMembers(body)
	if !ok {
		return body
	}
	changed := false
	for i, entry := range members {
		if entry.key != "tools" {
			continue
		}
		if next, edited := mapList(entry.value, stripTool); edited {
			members[i].value = next
			changed = true
		}
	}
	if !changed {
		return body
	}
	return encodeObject(members)
}

func stripTool(raw json.RawMessage) (json.RawMessage, bool) {
	members, ok := objectMembers(raw)
	if !ok {
		return raw, false
	}
	changed := false
	for i, entry := range members {
		if entry.key != "input_schema" {
			continue
		}
		if next, edited := stripSchema(entry.value); edited {
			members[i].value = next
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	return encodeObject(members), true
}

// stripSchema walks only schema keywords, so a property named "pattern" (Grep's) and
// data such as enum, const, default or examples stay intact.
func stripSchema(raw json.RawMessage) (json.RawMessage, bool) {
	members, ok := objectMembers(raw)
	if !ok {
		return raw, false // boolean schema or not a schema
	}
	kept := members[:0]
	changed := false
	for _, entry := range members {
		next, edited := entry.value, false
		switch {
		case entry.key == "pattern" && isString(entry.value):
			changed = true
			continue
		case subschemaKeys[entry.key]:
			next, edited = stripSchema(entry.value)
			if !edited {
				next, edited = mapList(entry.value, stripSchema)
			}
		case subschemaListKeys[entry.key]:
			next, edited = mapList(entry.value, stripSchema)
		case subschemaMapKeys[entry.key]:
			next, edited = mapMembers(entry.value, stripSchema)
		}
		changed = changed || edited
		kept = append(kept, member{key: entry.key, value: next})
	}
	if !changed {
		return raw, false
	}
	return encodeObject(kept), true
}

func mapList(raw json.RawMessage, edit func(json.RawMessage) (json.RawMessage, bool)) (json.RawMessage, bool) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return raw, false
	}
	changed := false
	for i, item := range items {
		if next, edited := edit(item); edited {
			items[i] = next
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	var out bytes.Buffer
	out.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(item)
	}
	out.WriteByte(']')
	return out.Bytes(), true
}

func mapMembers(raw json.RawMessage, edit func(json.RawMessage) (json.RawMessage, bool)) (json.RawMessage, bool) {
	members, ok := objectMembers(raw)
	if !ok {
		return raw, false
	}
	changed := false
	for i, entry := range members {
		if next, edited := edit(entry.value); edited {
			members[i].value = next
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	return encodeObject(members), true
}

// objectMembers keeps member order and duplicate keys, which a map would lose.
func objectMembers(raw []byte) ([]member, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	var members []member
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		members = append(members, member{key: key, value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, false
	}
	return members, true
}

func encodeObject(members []member) []byte {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, entry := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(entry.key) // Marshal of a string cannot fail.
		out.Write(key)
		out.WriteByte(':')
		out.Write(entry.value)
	}
	out.WriteByte('}')
	return out.Bytes()
}

func isString(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '"'
}

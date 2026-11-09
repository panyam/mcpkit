package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
)

// Open result types (SEP-2663 CreateTaskResult and GetTaskResult extend the
// open Result) keep keys they don't model in an Extras map. These helpers do
// the spreading and collecting for any struct, deriving the known keys from
// its json tags so a new typed field never falls into Extras (#1665).

var knownKeysCache sync.Map // reflect.Type -> map[string]bool

// knownJSONKeys returns the top-level json keys a struct type encodes,
// following embedded structs the way encoding/json promotes them.
func knownJSONKeys(t reflect.Type) map[string]bool {
	if v, ok := knownKeysCache.Load(t); ok {
		return v.(map[string]bool)
	}
	keys := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			keys[name] = true
		}
	}
	walk(t)
	knownKeysCache.Store(t, keys)
	return keys
}

// marshalWithExtras spreads extras at the top level of typed's encoding.
// typed must encode to a JSON object. A typed key wins over an extra.
func marshalWithExtras(typed []byte, extras map[string]any) ([]byte, error) {
	if len(extras) == 0 {
		return typed, nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(typed, &out); err != nil {
		return nil, err
	}
	for k, v := range extras {
		if _, taken := out[k]; taken {
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[k] = b
	}
	return json.Marshal(out)
}

// unmarshalExtras returns the top-level keys of data that t does not model,
// or nil when there are none.
func unmarshalExtras(data []byte, t reflect.Type) (map[string]any, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	known := knownJSONKeys(t)
	var extras map[string]any
	for k, v := range raw {
		if known[k] {
			continue
		}
		var val any
		if err := json.Unmarshal(v, &val); err != nil {
			return nil, err
		}
		if extras == nil {
			extras = map[string]any{}
		}
		extras[k] = val
	}
	return extras, nil
}

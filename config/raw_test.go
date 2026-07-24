package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestKnownKeys guarantees knownKeys stays in sync with Raw's json tags, so
// unknown-property detection never misfires on a field we actually decode.
func TestKnownKeys(t *testing.T) {
	rt := reflect.TypeOf(Raw{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		if !knownKeys[name] {
			t.Errorf("field %s (json:%q) is not in knownKeys", rt.Field(i).Name, name)
		}
	}
	// And no stale entries: every knownKeys name must map to a field.
	tagged := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			tagged[name] = true
		}
	}
	for k := range knownKeys {
		if !tagged[k] {
			t.Errorf("knownKeys has %q with no matching Raw field", k)
		}
	}
}

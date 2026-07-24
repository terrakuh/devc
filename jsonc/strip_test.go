package jsonc

import (
	"encoding/json"
	"testing"
)

func TestStrip(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"line comment", "{\n// hi\n\"a\":1}", "{\n     \n\"a\":1}"},
		{"trailing line comment", `{"a":1} // done`, `{"a":1}        `},
		{"block comment", `{/* x */"a":1}`, `{       "a":1}`},
		{"comment chars in string", `{"url":"http://x//y"}`, `{"url":"http://x//y"}`},
		{"slash-star in string", `{"a":"/* not a comment */"}`, `{"a":"/* not a comment */"}`},
		{"trailing comma object", `{"a":1,}`, `{"a":1 }`},
		{"trailing comma array", `[1,2,]`, `[1,2 ]`},
		{"trailing comma with space", `[1, 2, ]`, `[1, 2  ]`},
		{"trailing comma before comment", "[1,2 /*c*/]", "[1,2      ]"},
		{"nested trailing", `{"a":[1,],"b":2,}`, `{"a":[1 ],"b":2 }`},
		{"escaped quote in string", `{"a":"he said \"hi\"",}`, `{"a":"he said \"hi\"" }`},
		{"non-trailing comma untouched", `[1,2]`, `[1,2]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Strip([]byte(c.in))
			if err != nil {
				t.Fatalf("Strip error: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("Strip(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
			if len(got) != len(c.in) {
				t.Fatalf("length changed: got %d want %d", len(got), len(c.in))
			}
		})
	}
}

func TestStripRoundtripsToValidJSON(t *testing.T) {
	src := `{
		// the workspace service
		"name": "Shop", // display name
		"service": "workspace",
		"dockerComposeFile": [
			"compose.yaml",
			"telemetry.dev.yaml", /* overlay */
		],
		"forwardPorts": [8080, 5173,],
	}`
	stripped, err := Strip([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(stripped, &v); err != nil {
		t.Fatalf("stripped output is not valid JSON: %v\n%s", err, stripped)
	}
	if v["name"] != "Shop" {
		t.Fatalf("name = %v", v["name"])
	}
}

func TestStripErrors(t *testing.T) {
	if _, err := Strip([]byte(`{"a":"unterminated`)); err == nil {
		t.Fatal("expected error for unterminated string")
	}
	if _, err := Strip([]byte(`{/* unterminated`)); err == nil {
		t.Fatal("expected error for unterminated block comment")
	}
}

package strictjson

import (
	"encoding/json"
	"testing"
)

func TestObjectRefusesAmbiguousOrTrailingInput(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate":    `{"a":1,"a":2}`,
		"trailing":     `{"a":1} {}`,
		"array":        `[1]`,
		"unterminated": `{"a":1`,
		"empty":        ``,
	} {
		if _, err := Object([]byte(raw)); err == nil {
			t.Fatalf("%s: accepted %q", name, raw)
		}
	}
	fields, err := Object([]byte(` {"a": 1, "b": {"a": 2}} `))
	if err != nil || len(fields) != 2 || string(fields["a"]) != "1" {
		t.Fatalf("fields = %v err = %v", fields, err)
	}
}

func TestExactFieldsEnforcesAllowedAndRequiredNames(t *testing.T) {
	fields := map[string]json.RawMessage{"a": json.RawMessage(`1`), "b": json.RawMessage(`2`)}
	if err := ExactFields(fields, []string{"a", "b"}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := ExactFields(fields, []string{"a"}, nil); err == nil {
		t.Fatal("an unexpected field was accepted")
	}
	if err := ExactFields(fields, []string{"a", "b", "c"}, []string{"c"}); err == nil {
		t.Fatal("a missing required field was accepted")
	}
}

func TestScalarDecodersRefuseOtherJSONTypes(t *testing.T) {
	if value, err := String(json.RawMessage(` "x" `)); err != nil || value != "x" {
		t.Fatalf("string = %q, %v", value, err)
	}
	for _, raw := range []string{`1`, `null`, `["x"]`, `"x" "y"`} {
		if _, err := String(json.RawMessage(raw)); err == nil {
			t.Fatalf("String accepted %s", raw)
		}
	}
	if number, err := Number(json.RawMessage(`9007199254740991`)); err != nil || number.String() != "9007199254740991" {
		t.Fatalf("number = %q, %v", number, err)
	}
	for _, raw := range []string{`"1"`, `null`, `1 2`, `[1]`} {
		if _, err := Number(json.RawMessage(raw)); err == nil {
			t.Fatalf("Number accepted %s", raw)
		}
	}
	if items, err := Array(json.RawMessage(`[1,"a"]`)); err != nil || len(items) != 2 {
		t.Fatalf("array = %v, %v", items, err)
	}
	for _, raw := range []string{`null`, `{}`, `"a"`, `[1] [2]`} {
		if _, err := Array(json.RawMessage(raw)); err == nil {
			t.Fatalf("Array accepted %s", raw)
		}
	}
}

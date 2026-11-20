package httpapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Review of #199, finding 1: complete reads a body against its type at
// every depth, through pointers, lists and maps; a field encoding/json
// does not read (unexported, or "-") is not required, and one without a
// name is required by its own.
func TestCompleteRequiresEveryMemberAndNoNull(t *testing.T) {
	type inner struct {
		N *int `json:"n"`
	}
	type body struct {
		A       int              `json:"a"`
		Opt     string           `json:"opt,omitzero"`
		M       map[string]inner `json:"m,omitempty"`
		L       []*inner         `json:"l,omitempty"`
		Skipped int              `json:"-"`
		Bare    int
		hidden  int
	}
	_ = body{}.hidden
	for raw, ok := range map[string]bool{
		`{"a":1,"Bare":2}`:                      true,
		`{"a":1,"Bare":2,"opt":"x"}`:            true,
		`{"a":1,"Bare":2,"m":{"k":{"n":3}}}`:    true,
		`{"a":1,"Bare":2,"l":[{"n":3}]}`:        true,
		`{"a":1}`:                               false,
		`{"Bare":2}`:                            false,
		`{"a":null,"Bare":2}`:                   false,
		`{"a":1,"Bare":2,"opt":null}`:           false,
		`{"a":1,"Bare":2,"m":{"k":{}}}`:         false,
		`{"a":1,"Bare":2,"m":{"k":{"n":null}}}`: false,
		`{"a":1,"Bare":2,"m":[]}`:               false,
		`{"a":1,"Bare":2,"l":[null]}`:           false,
		`{"a":1,"Bare":2,"l":{}}`:               false,
		`[]`:                                    false,
	} {
		var tree any
		if err := json.Unmarshal([]byte(raw), &tree); err != nil {
			t.Fatal(err)
		}
		if got := complete(tree, reflect.TypeFor[*body]()); got != ok {
			t.Errorf("complete(%s) = %v, want %v", raw, got, ok)
		}
	}
}

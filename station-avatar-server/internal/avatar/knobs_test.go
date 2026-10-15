package avatar

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateNamesTheFieldAtFault(t *testing.T) {
	cases := []struct {
		name  string
		req   Request
		field string
	}{
		{"empty id", Request{ID: "  ", Knobs: Defaults()}, "id"},
		{"long id", Request{ID: strings.Repeat("a", MaxIDLen+1), Knobs: Defaults()}, "id"},
		{"fill high", Request{ID: "a", Knobs: Knobs{Fill: 101, Hue: -1}}, "fill"},
		{"fill low", Request{ID: "a", Knobs: Knobs{Fill: -1, Hue: -1}}, "fill"},
		{"hue high", Request{ID: "a", Knobs: Knobs{Fill: 65, Hue: 360}}, "hue"},
		{"hue low", Request{ID: "a", Knobs: Knobs{Fill: 65, Hue: -2}}, "hue"},
		{"accent name", Request{ID: "a", Knobs: Defaults(), Accent: "red"}, "accent"},
		{"accent short", Request{ID: "a", Knobs: Defaults(), Accent: "#FF6B5"}, "accent"},
	}
	for _, c := range cases {
		var fe *FieldError
		if err := c.req.Validate(); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: got %v, want a FieldError on %q", c.name, err, c.field)
		}
	}
	if err := (Request{ID: "agt-1", Knobs: Defaults(), Accent: "#FF6B5B"}).Validate(); err != nil {
		t.Errorf("defaults should be valid: %v", err)
	}
}

func TestRunIDIsStableAndSeparatesInputs(t *testing.T) {
	a := Request{ID: "agt-1", Knobs: Defaults()}
	if a.RunID() != a.RunID() {
		t.Fatal("the same request must give the same run id")
	}
	b := a
	b.Knobs.Hue = 200
	c := Request{ID: "agt-2", Knobs: Defaults()}
	d := a
	d.Accent = "#FF6B5B"
	if a.RunID() == b.RunID() || a.RunID() == c.RunID() || a.RunID() == d.RunID() {
		t.Fatal("different id, knobs or accent must give a different run id")
	}
	e := d
	e.Accent = "#ff6b5b"
	if d.RunID() != e.RunID() {
		t.Fatal("hex case must not change the run id")
	}
}

package pgplugin

import "testing"

func TestReviewedPreload(t *testing.T) {
	cases := []struct {
		in             string
		stat, reviewed bool
	}{
		{"", false, true},
		{"pg_stat_statements", true, true},
		{" pg_stat_statements ", true, true},
		{`"pg_stat_statements"`, true, true},
		{"pg_stat_statements,", true, true},
		{"auto_explain", false, false},
		{"pg_stat_statements,auto_explain", false, false},
		{"pgaudit", false, false},
		{"pg_stat_statements_x", false, false},
	}
	for _, c := range cases {
		stat, reviewed := reviewedPreload(c.in)
		if stat != c.stat || reviewed != c.reviewed {
			t.Errorf("reviewedPreload(%q) = %v,%v; want %v,%v", c.in, stat, reviewed, c.stat, c.reviewed)
		}
	}
}

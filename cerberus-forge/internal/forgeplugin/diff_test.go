package forgeplugin

import (
	"strings"
	"testing"
)

func TestDiffScriptsCountsAndRendersChanges(t *testing.T) {
	current := "a\nb\nc\nd\ne\nf\ng\nh\n"
	proposed := "a\nb\nC\nd\ne\nf\ng\nh\ni\n"
	diff := diffScripts(current, proposed)
	if !diff.Changed || diff.Added != 2 || diff.Removed != 1 {
		t.Fatalf("diff = %+v", diff)
	}
	want := "@@ -1,5 +1,5 @@\n a\n b\n-c\n+C\n d\n e\n@@ -7,2 +7,3 @@\n g\n h\n+i\n"
	if diff.Unified != want {
		t.Fatalf("unified =\n%s\nwant\n%s", diff.Unified, want)
	}
}

func TestDiffScriptsOfIdenticalScriptsIsEmpty(t *testing.T) {
	if diff := diffScripts("a\nb\n", "a\nb\n"); diff.Changed || diff.Unified != "" {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestDiffScriptsFromEmpty(t *testing.T) {
	diff := diffScripts("", "a\nb\n")
	if diff.Added != 2 || diff.Removed != 0 || !strings.HasPrefix(diff.Unified, "@@ -1,0 +1,2 @@\n+a\n+b\n") {
		t.Fatalf("diff = %+v", diff)
	}
}

// A script too large to diff line by line is reported as a replacement.
func TestDiffScriptsCapsTheTable(t *testing.T) {
	big := strings.Repeat("x\n", 3000)
	diff := diffScripts(big, big+"y\n"+strings.Repeat("z\n", 2000))
	if !diff.Changed || !diff.Truncated || diff.Unified != "" {
		t.Fatalf("diff = %+v", diff)
	}
}

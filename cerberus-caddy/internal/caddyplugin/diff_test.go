package caddyplugin

import "testing"

func TestUnifiedDiffEqualIsEmpty(t *testing.T) {
	if got := unifiedDiff("a", "b", "x\ny\n", "x\ny\n"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestUnifiedDiffShape(t *testing.T) {
	old := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	updated := "1\n2\n3\n4\nFIVE\n6\n7\n8\n9\n10\n11\n12\n"
	want := "--- a\n+++ b\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+FIVE\n 6\n 7\n 8\n"
	if got := unifiedDiff("a", "b", old, updated); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnifiedDiffSplitsDistantChanges(t *testing.T) {
	old := "a\n1\n2\n3\n4\n5\n6\n7\n8\n9\nz\n"
	updated := "A\n1\n2\n3\n4\n5\n6\n7\n8\n9\nZ\n"
	got := unifiedDiff("a", "b", old, updated)
	want := "--- a\n+++ b\n@@ -1,4 +1,4 @@\n-a\n+A\n 1\n 2\n 3\n@@ -8,4 +8,4 @@\n 7\n 8\n 9\n-z\n+Z\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnifiedDiffFromNothing(t *testing.T) {
	want := "--- /dev/null\n+++ b\n@@ -0,0 +1,2 @@\n+x\n+y\n"
	if got := unifiedDiff("/dev/null", "b", "", "x\ny\n"); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

package palette

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func reg(t *testing.T) *Registry {
	t.Helper()
	return &Registry{Path: filepath.Join(t.TempDir(), "palette_registry.json")}
}

func TestTableMatchesThePythonOrder(t *testing.T) {
	// These are the first and last rows and the count in avatar.py's CURATED.
	if len(Curated) != 16 || Curated[0].Name != "coral" || Curated[15].Name != "cream" || Curated[15].BG != "#3B2B55" {
		t.Fatalf("the shared table drifted from the Python generator: %v", Curated)
	}
}

func TestFirstSixteenAgentsNeverShareASlot(t *testing.T) {
	r := reg(t)
	seen := map[int]string{}
	for i := 0; i < 16; i++ {
		urn := "agent-" + string(rune('a'+i))
		idx, _, err := r.Slot(urn)
		if err != nil {
			t.Fatal(err)
		}
		if other, dup := seen[idx]; dup {
			t.Fatalf("%s and %s share slot %d", urn, other, idx)
		}
		seen[idx] = urn
	}
	// The 17th has no free slot and falls back to its hash slot.
	if _, _, err := r.Slot("one-too-many"); err != nil {
		t.Fatal(err)
	}
}

func TestSlotIsPersistedAndStable(t *testing.T) {
	r := reg(t)
	a, _, _ := r.Slot("agt-cogsworth")
	b, _, _ := r.Slot("agt-cogsworth")
	if a != b {
		t.Fatalf("slot changed between calls: %d vs %d", a, b)
	}
	if _, ok, _ := r.Peek("agt-cogsworth"); !ok {
		t.Fatal("slot was not persisted")
	}
	if _, ok, _ := r.Peek("never-seen"); ok {
		t.Fatal("Peek must not assign")
	}
}

func TestStartSlotIsTheHashOfTheNFCName(t *testing.T) {
	// A fresh registry gives the hash slot itself: sha256(NFC)[0:2] % 16.
	r := reg(t)
	urn := "Ünï-cöde"
	h := sha256.Sum256([]byte(norm.NFC.String(urn)))
	want := (int(h[0])<<8 | int(h[1])) % 16
	if got, _, _ := r.Slot(urn); got != want {
		t.Fatalf("slot = %d, want %d", got, want)
	}
}

func TestNFCAndNFDNamesAreTheSameAgent(t *testing.T) {
	r := reg(t)
	nfc := norm.NFC.String("Ünï-cöde")
	nfd := norm.NFD.String("Ünï-cöde")
	if nfc == nfd {
		t.Fatal("test needs distinct byte strings")
	}
	a, _, _ := r.Slot(nfc)
	b, _, _ := r.Slot(nfd)
	if a != b {
		t.Fatalf("NFC and NFD forms got different slots: %d, %d", a, b)
	}
}

func TestACorruptRegistryIsRefusedNotOverwritten(t *testing.T) {
	r := reg(t)
	if err := os.WriteFile(r.Path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Slot("a"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v, want ErrCorrupt", err)
	}
	if b, _ := os.ReadFile(r.Path); string(b) != "{not json" {
		t.Fatal("a corrupt registry must be left alone for a human to look at")
	}
	if err := os.WriteFile(r.Path, []byte(`{"a": 99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Slot("b"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("out-of-range slot: got %v, want ErrCorrupt", err)
	}
}

func TestReadsAFileWrittenByThePython(t *testing.T) {
	r := reg(t)
	// avatar.py writes json.dump(reg, indent=1, ensure_ascii=False).
	if err := os.WriteFile(r.Path, []byte("{\n \"zeta-9\": 6,\n \"Ünï-cöde\": 11\n}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if idx, p, _ := r.Slot("zeta-9"); idx != 6 || p.Name != "violet" {
		t.Fatalf("got %d %s", idx, p.Name)
	}
	if idx, _, _ := r.Slot("Ünï-cöde"); idx != 11 {
		t.Fatalf("got %d", idx)
	}
	r.Slot("new-agent")
	b, _ := os.ReadFile(r.Path)
	if !strings.Contains(string(b), "Ünï-cöde") || strings.Contains(string(b), `\u00`) {
		t.Fatalf("unicode must be written as-is, got:\n%s", b)
	}
}

func TestConcurrentFirstUseNeverSharesASlot(t *testing.T) {
	r := reg(t)
	var wg sync.WaitGroup
	got := make([]int, 12)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			idx, _, err := r.Slot("agent-" + string(rune('a'+i)))
			if err != nil {
				t.Error(err)
			}
			got[i] = idx
		}(i)
	}
	wg.Wait()
	seen := map[int]bool{}
	for _, idx := range got {
		if seen[idx] {
			t.Fatalf("two concurrent agents shared slot %d: %v", idx, got)
		}
		seen[idx] = true
	}
}

func TestAHeldLockReportsLocked(t *testing.T) {
	r := reg(t)
	r.LockWait = 30 * 1e6 // 30ms
	if err := os.WriteFile(r.Path+".lock", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Slot("a"); !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

func TestByName(t *testing.T) {
	if i, p, ok := ByName("Violet"); !ok || i != 6 || p.Accent != "#9B6BFF" {
		t.Fatalf("got %d %v %v", i, p, ok)
	}
	if _, _, ok := ByName("chartreuse"); ok {
		t.Fatal("unknown name must not match")
	}
}

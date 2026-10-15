package spend

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func ledger(t *testing.T) *Ledger {
	return &Ledger{Path: filepath.Join(t.TempDir(), "spend_ledger.json")}
}

var noon = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func TestTotalsSplitTodayFromAll(t *testing.T) {
	l := ledger(t)
	for _, e := range []Entry{
		{At: noon.Add(-48 * time.Hour), URN: "a", USD: 0.5},
		{At: noon.Add(-13 * time.Hour), URN: "b", USD: 0.25}, // 23:00 the day before
		{At: noon.Add(-2 * time.Hour), URN: "c", USD: 0.034}, // today
		{At: noon.Add(5 * time.Hour), URN: "d", USD: 0.034},  // later today (clock skew is fine)
		{At: noon, URN: "e", USD: 0},                         // a free call still counts as a call
	} {
		if err := l.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.Totals(noon)
	if err != nil {
		t.Fatal(err)
	}
	if got.All != 0.818 || got.Today != 0.068 || got.Calls != 5 {
		t.Fatalf("totals = %+v", got)
	}
}

func TestADayIsAUTCDay(t *testing.T) {
	l := ledger(t)
	l.Record(Entry{At: time.Date(2026, 10, 15, 23, 59, 0, 0, time.FixedZone("far", -11*3600)), USD: 1}) // 10:59 UTC on the 16th
	got, _ := l.Totals(time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC))
	if got.Today != 1 {
		t.Fatalf("a UTC-day boundary was crossed wrongly: %+v", got)
	}
}

func TestEmptyLedgerIsZeroNotAnError(t *testing.T) {
	if got, err := ledger(t).Totals(noon); err != nil || got != (Totals{}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestACorruptLedgerIsRefusedAndLeftAlone(t *testing.T) {
	l := ledger(t)
	os.WriteFile(l.Path, []byte("{nope"), 0o644)
	if _, err := l.Totals(noon); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("totals: %v", err)
	}
	if err := l.Record(Entry{USD: 1}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("record: %v", err)
	}
	if b, _ := os.ReadFile(l.Path); string(b) != "{nope" {
		t.Fatal("a corrupt ledger must not be overwritten: the limits depend on it")
	}
	os.WriteFile(l.Path, []byte(`{"entries":[{"usd":-5}]}`), 0o644)
	if _, err := l.Totals(noon); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a negative entry would lower the total: %v", err)
	}
}

func TestConcurrentRecordsAreAllKept(t *testing.T) {
	l := ledger(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Record(Entry{At: noon, USD: 0.01}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got, _ := l.Totals(noon); got.Calls != 20 || got.All != 0.2 {
		t.Fatalf("lost writes: %+v", got)
	}
}

func TestAHeldLockReportsLocked(t *testing.T) {
	l := ledger(t)
	l.LockWait = 30 * time.Millisecond
	os.WriteFile(l.Path+".lock", nil, 0o644)
	if err := l.Record(Entry{USD: 1}); !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v", err)
	}
}

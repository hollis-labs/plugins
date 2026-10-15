// Package spend is the avatar plugin's own record of what paid engines have
// cost, so spend limits are enforced here rather than trusted to whatever the
// engine keeps. It is a small append-only file; a day is a UTC day.
package spend

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrLocked: another writer held the ledger for too long.
var ErrLocked = errors.New("spend ledger is locked by another writer")

// ErrCorrupt: the file exists but cannot be read. Never overwritten: the limits
// are only as good as this record.
var ErrCorrupt = errors.New("spend ledger is unreadable")

// Entry is one paid attempt, successful or not.
type Entry struct {
	At   time.Time `json:"at"`
	URN  string    `json:"urn"`
	Mode string    `json:"mode"`
	Info string    `json:"info,omitempty"` // model name, or the failure reason
	USD  float64   `json:"usd"`
}

// Ledger is the file.
type Ledger struct {
	Path string
	// LockWait bounds waiting for another writer (default 2s).
	LockWait time.Duration
}

type file struct {
	Entries []Entry `json:"entries"`
}

// Totals are the spend so far.
type Totals struct {
	Today, All float64
	Calls      int
}

// Totals sums the ledger; today is the UTC day containing now.
func (l *Ledger) Totals(now time.Time) (Totals, error) {
	f, err := l.load()
	if err != nil {
		return Totals{}, err
	}
	var t Totals
	day := now.UTC().Format("2006-01-02")
	for _, e := range f.Entries {
		t.All += e.USD
		t.Calls++
		if e.At.UTC().Format("2006-01-02") == day {
			t.Today += e.USD
		}
	}
	t.Today, t.All = round6(t.Today), round6(t.All)
	return t, nil
}

// Record appends an entry. Zero-cost entries are kept: a call that cost nothing
// is still a call.
func (l *Ledger) Record(e Entry) error {
	unlock, err := l.lock()
	if err != nil {
		return err
	}
	defer unlock()
	f, err := l.load()
	if err != nil {
		return err
	}
	f.Entries = append(f.Entries, e)
	b, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return err
	}
	tmp := l.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.Path)
}

func (l *Ledger) load() (file, error) {
	b, err := os.ReadFile(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return file{}, nil
	}
	if err != nil {
		return file{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return file{}, fmt.Errorf("%w: %s: %v", ErrCorrupt, l.Path, err)
	}
	for _, e := range f.Entries {
		if e.USD < 0 {
			return file{}, fmt.Errorf("%w: %s has a negative entry", ErrCorrupt, l.Path)
		}
	}
	return f, nil
}

func (l *Ledger) lock() (func(), error) {
	wait := l.LockWait
	if wait <= 0 {
		wait = 2 * time.Second
	}
	path := l.Path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > time.Minute {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, ErrLocked
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func round6(v float64) float64 { return float64(int64(v*1e6+0.5)) / 1e6 }

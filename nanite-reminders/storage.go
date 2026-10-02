package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

// Each source retains the complete typed core snapshot. Edits change only the
// addressed fields; unrecognized columns and SQLite storage classes survive.
// The checkpoint and rows are one atomic commit, never separate writes.
type table struct {
	ImportSHA256   string                 `json:"import_sha256"`
	Snapshot       pluginapi.DataSnapshot `json:"snapshot"`
	CreationCounts map[string]int         `json:"creation_counts,omitempty"`
}
type database struct{ dir string }

var errNotFound = errors.New("reminder not found")

const maxTableBytes = 256 << 20

func sourceFile(source string) string {
	digest := sha256.Sum256([]byte(source))
	return "reminders-" + hex.EncodeToString(digest[:]) + ".json"
}
func noLink(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("reminders: non-regular storage file")
	}
	return nil
}

// Separate lock descriptors coordinate both concurrent SDK calls and multiple
// host processes sharing DataDir. Nonblocking polling honors cancellation.
func (db database) transaction(ctx context.Context, source string, mutate func(*table) (bool, error)) error {
	root, err := os.OpenRoot(db.dir)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // Root.Close does not flush writes.
	lockName := sourceFile(source) + ".lock"
	if err = noLink(root, lockName); err != nil {
		return err
	}
	lock, err := root.OpenFile(lockName, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck // The lock file contains no data.
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck // Descriptor close also releases the lock.
	name := sourceFile(source)
	if err = noLink(root, name); err != nil {
		return err
	}
	state := table{}
	file, err := root.Open(name)
	if err == nil {
		bounded := &io.LimitedReader{R: file, N: maxTableBytes + 1}
		decoder := json.NewDecoder(bounded)
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&state)
		var extra any
		if decodeErr == nil && decoder.Decode(&extra) != io.EOF {
			decodeErr = fmt.Errorf("reminders: trailing storage data")
		}
		closeErr := file.Close()
		if decodeErr != nil {
			return decodeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if bounded.N <= 0 {
			return fmt.Errorf("reminders: storage exceeds limit")
		}
		if err = validateTable(state, source); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	changed, err := mutate(&state)
	if err != nil || !changed {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = validateTable(state, source); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(raw) > maxTableBytes {
		return fmt.Errorf("reminders: storage exceeds limit")
	}
	temporary := name + "." + newID() + ".tmp"
	out, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary) //nolint:errcheck // Removed after success; best-effort cleanup after failure.
	_, writeErr := out.Write(raw)
	if writeErr == nil {
		writeErr = out.Sync()
	}
	closeErr := out.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = root.Rename(temporary, name); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr = directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
func newID() string { return rand.Text() }

var requiredColumns = []string{"id", "session_id", "scope", "project_id", "text", "trigger_json", "fired_at", "created_at", "updated_at"}

func columns(snapshot pluginapi.DataSnapshot) (map[string]int, error) {
	result := make(map[string]int)
	for i, name := range snapshot.Columns {
		result[name] = i
	}
	for _, name := range requiredColumns {
		if _, ok := result[name]; !ok {
			return nil, fmt.Errorf("reminders: required column %s absent", name)
		}
	}
	return result, nil
}
func validateTable(state table, source string) error {
	if len(state.ImportSHA256) != 64 || state.Snapshot.SourceID != source || state.Snapshot.PluginID != pluginID || state.Snapshot.Feature != "reminders" {
		return fmt.Errorf("reminders: storage identity differs")
	}
	if _, err := hex.DecodeString(state.ImportSHA256); err != nil {
		return err
	}
	if err := state.Snapshot.Validate(); err != nil {
		return err
	}
	index, err := columns(state.Snapshot)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, row := range state.Snapshot.Rows {
		id, readErr := cellString(row[index["id"]])
		if readErr != nil || id == "" || seen[id] {
			return fmt.Errorf("reminders: missing or duplicate reminder ID")
		}
		seen[id] = true
	}
	for id, count := range state.CreationCounts {
		if !seen[id] || count < 0 {
			return fmt.Errorf("reminders: invalid creation baseline")
		}
	}
	return nil
}
func (db database) importReceipt(ctx context.Context, receipt pluginapi.DataExportReceipt) error {
	if receipt.PluginID != pluginID || receipt.Feature != "reminders" {
		return fmt.Errorf("reminders: foreign receipt")
	}
	root, err := os.OpenRoot(db.dir)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck
	file, err := root.Open(receipt.Path)
	if err != nil {
		return err
	}
	exported, decodeErr := pluginapi.DecodeDataExport(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return decodeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = receipt.Verify(exported); err != nil {
		return err
	}
	incoming := table{ImportSHA256: receipt.SHA256, Snapshot: exported.Snapshot}
	if err = validateTable(incoming, receipt.SourceID); err != nil {
		return err
	}
	return db.transaction(ctx, receipt.SourceID, func(state *table) (bool, error) {
		if state.ImportSHA256 != "" {
			if state.ImportSHA256 != receipt.SHA256 {
				return false, fmt.Errorf("reminders: conflicting export replay")
			}
			return false, nil
		}
		*state = incoming
		return true, nil
	})
}

func cellString(cell pluginapi.DataCell) (string, error) {
	value, err := cell.Value()
	if err != nil {
		return "", err
	}
	if value == nil {
		return "", nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		return fmt.Sprint(v), nil
	}
}

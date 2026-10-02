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
	"strings"
	"syscall"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

// Each source retains the complete typed core snapshot. Edits change only the
// addressed fields; unrecognized columns and SQLite storage classes survive.
// The checkpoint and rows are one atomic commit, never separate writes.
type table struct {
	ImportSHA256 string                 `json:"import_sha256"`
	Snapshot     pluginapi.DataSnapshot `json:"snapshot"`
}
type database struct {
	dir           string
	cache         *tableCache
	syncDirectory func(*os.File) error
}

var errNotFound = errors.New("document not found")

const maxTableBytes = pluginapi.MaxDataExportBytes
const maxNativeTableBytes = 64 << 20

var errInvalid = errors.New("invalid document request")
var errQuota = errors.New("document quota exceeded")
var errCommitUncertain = errors.New("commit outcome uncertain; refresh documents before retrying")

func sourceFile(source string) string {
	digest := sha256.Sum256([]byte(source))
	return "documents-" + hex.EncodeToString(digest[:]) + ".json"
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
		return fmt.Errorf("documents: non-regular storage file")
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
	if err = db.cleanupTemporary(root, name); err != nil {
		return err
	}
	if err = noLink(root, name); err != nil {
		return err
	}
	state := table{}
	info, err := root.Stat(name)
	if err == nil {
		var hit bool
		state, hit = db.cached(name, info)
		if !hit {
			file, openErr := root.Open(name)
			if openErr != nil {
				return openErr
			}
			bounded := &io.LimitedReader{R: file, N: maxTableBytes + 1}
			decoder := json.NewDecoder(bounded)
			decoder.DisallowUnknownFields()
			decodeErr := decoder.Decode(&state)
			var extra any
			if decodeErr == nil && decoder.Decode(&extra) != io.EOF {
				decodeErr = fmt.Errorf("documents: trailing storage data")
			}
			closeErr := file.Close()
			if decodeErr != nil {
				return decodeErr
			}
			if closeErr != nil {
				return closeErr
			}
			if bounded.N <= 0 {
				return fmt.Errorf("documents: storage exceeds import limit")
			}
			if err = validateTable(state, source); err != nil {
				return err
			}
			db.remember(name, info, state)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previousBytes := int64(0)
	if info != nil {
		previousBytes = info.Size()
	}
	imported := state.ImportSHA256 != ""

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
		return fmt.Errorf("%w: serialized table exceeds 128 MiB import bound", errQuota)
	}
	if imported && len(raw) > maxNativeTableBytes && int64(len(raw)) > previousBytes {
		return fmt.Errorf("%w: serialized table exceeds 64 MiB growth cap", errQuota)
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
		return errCommitUncertain
	}
	syncDirectory := db.syncDirectory
	if syncDirectory == nil {
		syncDirectory = func(file *os.File) error { return file.Sync() }
	}
	syncErr := syncDirectory(directory)
	closeErr = directory.Close()
	// The renamed state is visible even if directory fsync fails. Cache only
	// that visible committed image and report the uncertainty explicitly.
	info, statErr := root.Stat(name)
	if statErr == nil {
		db.remember(name, info, state)
	}
	if syncErr != nil || closeErr != nil || statErr != nil {
		return errCommitUncertain
	}
	return nil
}
func newID() string { return rand.Text() }

var requiredColumns = []string{"id", "session_id", "name", "mime_type", "content", "size_bytes", "included", "full_content", "summary", "created_at", "updated_at"}

func columns(snapshot pluginapi.DataSnapshot) (map[string]int, error) {
	result := make(map[string]int)
	for i, name := range snapshot.Columns {
		result[name] = i
	}
	for _, name := range requiredColumns {
		if _, ok := result[name]; !ok {
			return nil, fmt.Errorf("documents: required column %s absent", name)
		}
	}
	return result, nil
}
func validateTable(state table, source string) error {
	if len(state.ImportSHA256) != 64 || state.Snapshot.SourceID != source || state.Snapshot.PluginID != pluginID || state.Snapshot.Feature != "documents" {
		return fmt.Errorf("documents: storage identity differs")
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
			return fmt.Errorf("documents: missing or duplicate document ID")
		}
		seen[id] = true
	}
	return nil
}
func (db database) importReceipt(ctx context.Context, receipt pluginapi.DataExportReceipt) error {
	if receipt.PluginID != pluginID || receipt.Feature != "documents" {
		return fmt.Errorf("documents: foreign receipt")
	}
	return db.transaction(ctx, receipt.SourceID, func(state *table) (bool, error) {
		// The durable checkpoint is authoritative after a successful import.
		// Export files may be archived without disabling reads or replay.
		if state.ImportSHA256 != "" {
			if state.ImportSHA256 != receipt.SHA256 {
				return false, fmt.Errorf("documents: conflicting export replay")
			}
			return false, nil
		}
		root, err := os.OpenRoot(db.dir)
		if err != nil {
			return false, err
		}
		defer root.Close() //nolint:errcheck
		file, err := root.Open(receipt.Path)
		if err != nil {
			return false, err
		}
		exported, decodeErr := pluginapi.DecodeDataExport(file)
		closeErr := file.Close()
		if decodeErr != nil {
			return false, decodeErr
		}
		if closeErr != nil {
			return false, closeErr
		}
		if err = receipt.Verify(exported); err != nil {
			return false, err
		}
		incoming := table{ImportSHA256: receipt.SHA256, Snapshot: exported.Snapshot}
		if err = validateTable(incoming, receipt.SourceID); err != nil {
			return false, err
		}
		*state = incoming
		return true, nil
	})
}
func (db database) cleanupTemporary(root *os.Root, name string) error {
	if db.cache != nil {
		db.cache.mu.Lock()
		done := db.cache.cleaned[name]
		db.cache.mu.Unlock()
		if done {
			return nil
		}
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		file := entry.Name()
		if !strings.HasPrefix(file, name+".") || !strings.HasSuffix(file, ".tmp") {
			continue
		}
		nonce := strings.TrimSuffix(strings.TrimPrefix(file, name+"."), ".tmp")
		if len(nonce) != 26 {
			continue
		}
		valid := true
		for _, r := range nonce {
			if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				valid = false
			}
		}
		if !valid {
			continue
		}
		// Only our regular temporary files for this locked workspace are removed.
		if err = noLink(root, file); err != nil {
			return err
		}
		if err = root.Remove(file); err != nil {
			return err
		}
	}
	if db.cache != nil {
		db.cache.mu.Lock()
		db.cache.cleaned[name] = true
		db.cache.mu.Unlock()
	}
	return nil
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

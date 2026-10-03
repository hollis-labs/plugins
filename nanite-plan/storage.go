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
type importedTable struct {
	ImportSHA256 string                 `json:"import_sha256"`
	Snapshot     pluginapi.DataSnapshot `json:"snapshot"`
}
type table struct {
	Tables map[string]importedTable `json:"tables"`
}
type database struct {
	dir             string
	cache           *tableCache
	syncDirectory   func(*os.File) error
	nativeGrowthCap int // Optional smaller cap for bounded regression fixtures.
}

var errNotFound = errors.New("work item not found")

const maxTableBytes = 3 * pluginapi.MaxDataExportBytes
const maxNativeTableBytes = 64 << 20

var errInvalid = errors.New("invalid work item request")
var errQuota = errors.New("work item quota exceeded")
var errUnavailable = errors.New("plan unavailable")
var errCommitUncertain = errors.New("commit outcome uncertain; refresh plan before retrying")

func sourceFile(source string) string {
	digest := sha256.Sum256([]byte(source))
	return "plan-" + hex.EncodeToString(digest[:]) + ".json"
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
		return fmt.Errorf("plan: non-regular storage file")
	}
	return nil
}

// Separate lock descriptors coordinate both concurrent SDK calls and multiple
// host processes sharing DataDir. Nonblocking polling honors cancellation.
func (db database) transaction(ctx context.Context, source string, mutate func(*table) (bool, error)) error {
	return db.transactionWithGrowth(ctx, source, func(state *table) (bool, bool, error) {
		changed, err := mutate(state)
		return changed, false, err
	})
}

// Growth is semantic: added rows or longer content/summary, excluding settings
// and timestamp serialization. Imports use transaction without native growth.
func (db database) transactionWithGrowth(ctx context.Context, source string, mutate func(*table) (bool, bool, error)) error {
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
				decodeErr = fmt.Errorf("plan: trailing storage data")
			}
			closeErr := file.Close()
			if decodeErr != nil {
				return decodeErr
			}
			if closeErr != nil {
				return closeErr
			}
			if bounded.N <= 0 {
				return fmt.Errorf("plan: storage exceeds import limit")
			}
			if err = validateTable(state, source); err != nil {
				return err
			}
			db.remember(name, info, state)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	changed, growing, err := mutate(&state)
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
		return fmt.Errorf("%w: serialized workspace exceeds 384 MiB import bound", errQuota)
	}
	limit := maxNativeTableBytes
	if db.nativeGrowthCap > 0 {
		limit = db.nativeGrowthCap
	}
	if growing && len(raw) > limit {
		return fmt.Errorf("%w: serialized table exceeds native growth cap (%d bytes)", errQuota, limit)
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

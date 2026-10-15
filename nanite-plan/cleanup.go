package main

import (
	"os"
	"strings"
)

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

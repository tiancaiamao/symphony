// Package singleton provides a file-based single-instance lock using flock.
// It prevents multiple Symphony servers from running against the same database.
package singleton

import (
	"fmt"
	"os"
	"strconv"
	"syscall"

	"symphonia/internal/logging"
)

// Lock represents a held file lock.
type Lock struct {
	file *os.File
}

// Acquire tries to acquire an exclusive flock on the given path.
// Returns the lock if successful, or an error if another instance holds it.
// The lock is automatically released when the process exits (even on crash).
func Acquire(lockPath string) (*Lock, error) {
	// Open or create the lock file
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}

	// Try non-blocking exclusive lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Lock is held by another process — read its PID for diagnostics
		pidBytes, _ := os.ReadFile(lockPath)
		pidStr := string(pidBytes)
		if pidStr == "" {
			pidStr = "unknown"
		}

		f.Close()
		return nil, fmt.Errorf("another symphony instance is already running (PID %s, lock: %s)",
			pidStr, lockPath)
	}

	// Write our PID to the lock file for diagnostics
	if err := f.Truncate(0); err != nil {
		// Non-fatal: lock is held, PID is just informational
		logging.Warn("Failed to truncate lock file", logging.Err(err))
	}
	if _, err := f.Seek(0, 0); err != nil {
		logging.Warn("Failed to seek lock file", logging.Err(err))
	}
	pidStr := strconv.Itoa(os.Getpid())
	if _, err := f.WriteString(pidStr); err != nil {
		logging.Warn("Failed to write PID to lock file", logging.Err(err))
	}
	// Sync to ensure PID is on disk
	f.Sync()

	logging.Info("Acquired instance lock", logging.String("path", lockPath), logging.Int("pid", os.Getpid()))

	return &Lock{file: f}, nil
}

// Release releases the flock and removes the lock file.
func (l *Lock) Release() {
	if l.file != nil {
		lockPath := l.file.Name()
		syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		l.file.Close()
		os.Remove(lockPath)
		logging.Info("Released instance lock", logging.String("path", lockPath))
	}
}
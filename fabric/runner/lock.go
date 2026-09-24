package main

import (
	"fmt"
	"os"
	"time"
)

// Lock is a simple exclusive run lock (build brief sections 7.1, 9.3.1),
// implemented as an atomically-created file rather than flock(2) so it works
// unchanged on the container filesystem and on a mounted Lakehouse path.
type Lock struct {
	path string
}

func AcquireLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf(
				"lock file %s already exists: another run may be in progress, or a previous run crashed without releasing it (see fabric/verification/RUNBOOK.md for recovery)",
				path,
			)
		}
		return nil, fmt.Errorf("creating lock file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "pid=%d started=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, fmt.Errorf("writing lock file %s: %w", path, err)
	}
	return &Lock{path: path}, nil
}

func (l *Lock) Release() error {
	return os.Remove(l.path)
}

//go:build !windows

package agent

import (
	"errors"
	"os"
	"syscall"
)

func researchTryFileLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func researchUnlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

func researchTransientFileError(err error) bool {
	return errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EINTR)
}

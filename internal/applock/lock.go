// Package applock serializes credential operations between helper invocations.
package applock

import (
	"errors"
	"os"
	"syscall"
)

var ErrBusy = errors.New("account_operation_busy")
var ErrStorage = errors.New("private_state_directory_unavailable")

type Lock struct{ file *os.File }

func Acquire(dir string) (*Lock, error) {
	if os.MkdirAll(dir, 0700) != nil {
		return nil, ErrStorage
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrStorage
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, ErrStorage
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrStorage
	}
	defer root.Close()
	f, err := root.OpenFile("account-operation.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrStorage
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, ErrStorage
	}
	stat, ok = info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		f.Close()
		return nil, ErrStorage
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, ErrStorage
	}
	return &Lock{file: f}, nil
}

// Do not unlink the lock file: replacing its inode would break mutual exclusion.
func (l *Lock) Close() error { return l.file.Close() }

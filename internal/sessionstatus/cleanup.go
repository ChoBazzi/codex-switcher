package sessionstatus

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

// Serialize replacement and expiry checks so cleanup cannot delete a resumed
// session using an older sample. This lock never covers network or model work.
func mutationLock(root *os.Root) (*os.File, error) {
	f, err := root.OpenFile("status-write.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrStatus
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, ErrStatus
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		f.Close()
		return nil, ErrStatus
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX) != nil {
		f.Close()
		return nil, ErrStatus
	}
	return f, nil
}

// Cleanup removes only validated observational files idle for more than 14 days.
// It is independent of GET requests and does not touch Wiki, CLI homes or affinity.
func Cleanup(dir string, now time.Time) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return ErrStatus
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || st.Uid != uint32(os.Getuid()) {
		return ErrStatus
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ErrStatus
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return ErrStatus
	}
	defer f.Close()
	// Bounded batches also allow recovery when the read API's 4096 cap is exceeded.
	for {
		entries, err := f.ReadDir(128)
		if err != nil && err != io.EOF {
			return ErrStatus
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") || !hexID(strings.TrimSuffix(name, ".json"), 16) {
				continue
			}
			if err := cleanupFile(root, name, now); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

func cleanupFile(root *os.Root, name string, now time.Time) error {
	lock, err := mutationLock(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return ErrStatus
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ErrStatus
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return ErrStatus
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	var s Session
	if err != nil || len(b) > 4096 || json.Unmarshal(b, &s) != nil || !valid(s) || name != s.Conversation+".json" {
		return ErrStatus
	}
	const retention = 14 * 24 * time.Hour
	if now.Sub(s.Heartbeat) <= retention || now.Sub(s.UpdatedAt) <= retention {
		return nil
	}
	if root.Remove(name) != nil {
		return ErrStatus
	}
	return nil
}

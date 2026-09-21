package accounts

import (
	"context"
	"sync"
)

// A zero-value mutation gate with cancellable waiters. No helper goroutine is
// left queued to start an exchange after its caller has canceled.
type operationLock struct {
	once sync.Once
	gate chan struct{}
}

func (l *operationLock) LockContext(ctx context.Context) error {
	l.once.Do(func() { l.gate = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case l.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			l.Unlock()
			return err
		}
		return nil
	}
}

func (l *operationLock) Lock()   { _ = l.LockContext(context.Background()) }
func (l *operationLock) Unlock() { <-l.gate }

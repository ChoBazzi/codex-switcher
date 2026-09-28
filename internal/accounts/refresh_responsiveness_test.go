package accounts

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRefreshKeepsLocalReadsAvailable(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			next := renewed(t, "alpha")
			m, _ := refreshFixture(t, refreshFunc(func(context.Context, Credentials) (Credentials, error) {
				close(entered)
				<-release
				if fail {
					return Credentials{}, ErrRefresh
				}
				return next, nil
			}))
			before, err := m.HistoryCredential("a")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := m.RequestAccess("a", time.Now()); done <- err }()
			<-entered
			read := make(chan bool, 1)
			go func() {
				states, err := m.Status()
				owner, historyErr := m.HistoryCredential("a")
				_, accessErr := m.Access("a", time.Now())
				read <- err == nil && len(states) == 5 && owner == before && historyErr == nil && accessErr == ErrExpired
			}()
			select {
			case valid := <-read:
				if !valid {
					t.Fatal("local reads exposed incomplete refresh or lost owner")
				}
			case <-time.After(time.Second):
				t.Fatal("local reads blocked behind OAuth")
			}
			// Another model request joins the exchange instead of trusting the
			// temporarily blocked record or initiating another OAuth attempt.
			joined := make(chan error, 1)
			go func() { _, err := m.RequestAccess("a", time.Now()); joined <- err }()
			unblock()
			for _, result := range []<-chan error{done, joined} {
				select {
				case err := <-result:
					if (err != nil) != fail {
						t.Fatal("joined request did not share committed outcome")
					}
				case <-time.After(time.Second):
					t.Fatal("joined refresh did not finish")
				}
			}
			owner, err := m.HistoryCredential("a")
			if fail && (err != ErrRefresh || owner != ([32]byte{})) || !fail && (err != nil || owner != before) {
				t.Fatal("history did not follow final durable outcome")
			}
		})
	}
}

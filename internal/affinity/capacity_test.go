//go:build darwin && cgo

package affinity

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFiveSlotMigrationPreservesOwnership(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		t.Run(version, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "private")
			s := openTest(t, dir)
			// Build an actual old two-slot schema, not just an old version marker.
			for _, q := range []string{
				`DROP TABLE responses`, `DROP TABLE handoffs`, `DROP TABLE sessions`,
				`CREATE TABLE sessions (id TEXT PRIMARY KEY, project TEXT NOT NULL, worktree TEXT NOT NULL, branch TEXT NOT NULL, slot TEXT NOT NULL CHECK(slot IN ('a','b')), last_seen INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('ready','inflight','blocked')), request TEXT NOT NULL DEFAULT '')`,
				`CREATE TABLE responses (id TEXT PRIMARY KEY, session TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE)`,
				`CREATE TABLE handoffs (id TEXT PRIMARY KEY, source TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, target TEXT NOT NULL CHECK(target IN ('a','b')), checkpoint TEXT NOT NULL, digest TEXT NOT NULL, consumed_by TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL)`,
				`PRAGMA user_version=` + version,
			} {
				if _, err := s.db.query(q); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			register(t, s, "old-a", "a", now)
			register(t, s, "old-b", "b", now)
			lease, err := s.Begin(origin("old-a"), nil, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Finish(lease, []string{"synthetic-response"}, true, now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.query(`INSERT INTO handoffs VALUES ('synthetic-handoff','old-a','b','synthetic-checkpoint','synthetic-digest','',1)`); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s = openTest(t, dir)
			rows, err := s.db.query("PRAGMA user_version")
			if err != nil || rows[0][0] != "3" {
				t.Fatal("migration missing")
			}
			rows, err = s.db.query("SELECT source,target FROM handoffs")
			if err != nil || len(rows) != 1 || rows[0][0] != "old-a" || rows[0][1] != "b" {
				t.Fatal("handoff lost")
			}
			for _, slot := range []string{"c", "d", "e"} {
				register(t, s, "new-"+slot, slot, now)
			}
			if _, err := s.Begin(origin("new-e"), []string{"synthetic-response"}, now); !errors.Is(err, ErrUnknown) {
				t.Fatal("cross-account continuation accepted")
			}
			if err := s.InvalidateSlot("e"); err != nil {
				t.Fatal(err)
			}
			if err := s.Ready(origin("new-e")); !errors.Is(err, ErrBlocked) {
				t.Fatal("E logout did not invalidate")
			}
			if err := s.Ready(origin("old-b")); err != nil {
				t.Fatal("B modified")
			}
			if _, err := s.Begin(origin("old-a"), nil, now); !errors.Is(err, ErrBlocked) {
				t.Fatal("pending handoff protection lost")
			}
			if _, err := s.db.query(`DELETE FROM handoffs WHERE id='synthetic-handoff'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Begin(origin("old-a"), []string{"synthetic-response"}, now); err != nil {
				t.Fatal("A response ownership lost")
			}
			if _, err := s.db.query(`INSERT INTO responses VALUES ('orphan','missing-session')`); err == nil {
				t.Fatal("foreign keys not restored")
			}
			if _, err := s.db.query(`UPDATE sessions SET slot='f' WHERE id='old-b'`); err == nil {
				t.Fatal("sixth DB slot accepted")
			}
		})
	}
}

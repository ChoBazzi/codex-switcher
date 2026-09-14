//go:build darwin && cgo

package affinity

/*
#cgo LDFLAGS: -lsqlite3
#include <sqlite3.h>
#include <stdlib.h>
static int bind_text(sqlite3_stmt *s, int n, const char *v) {
    return sqlite3_bind_text(s,n,v,-1,SQLITE_TRANSIENT);
}
*/
import "C"

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

type sqliteDB struct{ handle *C.sqlite3 }

func openDatabase(dir string) (database, error) {
	// Resolve system path aliases before SQLITE_OPEN_NOFOLLOW. The private leaf
	// directory and lifetime lock were already checked by applock.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, ErrStorage
	}
	path := filepath.Join(dir, "affinity.sqlite3")
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, ErrStorage
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Getuid()) {
			return nil, ErrStorage
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrStorage
	}
	f.Close()
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	db := &sqliteDB{}
	if C.sqlite3_open_v2(p, &db.handle, C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_FULLMUTEX|C.SQLITE_OPEN_NOFOLLOW, nil) != C.SQLITE_OK {
		db.close()
		return nil, ErrStorage
	}
	return db, nil
}
func (d *sqliteDB) close() error {
	if d.handle == nil {
		return nil
	}
	if C.sqlite3_close(d.handle) != C.SQLITE_OK {
		return ErrStorage
	}
	d.handle = nil
	return nil
}
func (d *sqliteDB) query(sql string, args ...string) ([][]string, error) {
	q := C.CString(sql)
	defer C.free(unsafe.Pointer(q))
	var stmt *C.sqlite3_stmt
	if C.sqlite3_prepare_v2(d.handle, q, -1, &stmt, nil) != C.SQLITE_OK {
		return nil, ErrStorage
	}
	defer C.sqlite3_finalize(stmt)
	for i, arg := range args {
		v := C.CString(arg)
		rc := C.bind_text(stmt, C.int(i+1), v)
		C.free(unsafe.Pointer(v))
		if rc != C.SQLITE_OK {
			return nil, ErrStorage
		}
	}
	var rows [][]string
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return rows, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, ErrStorage
		}
		row := make([]string, int(C.sqlite3_column_count(stmt)))
		for i := range row {
			p := C.sqlite3_column_text(stmt, C.int(i))
			if p != nil {
				row[i] = C.GoString((*C.char)(unsafe.Pointer(p)))
			}
		}
		rows = append(rows, row)
	}
}

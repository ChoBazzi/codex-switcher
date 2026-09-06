//go:build !darwin || !cgo

package affinity

func openDatabase(string) (database, error) { return nil, ErrStorage }

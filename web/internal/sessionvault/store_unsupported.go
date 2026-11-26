//go:build !windows && (!darwin || !cgo)

package sessionvault

import "io"

func lockProfile(string) (io.Closer, error) { return nil, ErrUnavailable }
func openStore(string) (Store, error)       { return nil, ErrUnavailable }

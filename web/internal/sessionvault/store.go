// Package sessionvault limits ChatGPT account storage to the current OS user.
package sessionvault

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

const maxRecordBytes = 128 << 10

var (
	ErrUnavailable = errors.New("Le coffre de session ChatGPT est indisponible.")
	ErrInUse       = errors.New("Brief Studio est déjà ouvert avec ce profil.")
)

type Store interface {
	Load() ([]byte, error)
	Save([]byte) error
	Delete() error
}

// Open locks the profile until closer.Close so only one process can rotate its
// refresh token. A non-nil closer must be held even if the vault cannot open.
func Open(profileDir string) (Store, io.Closer, error) {
	absolute, err := filepath.Abs(profileDir)
	if err != nil || os.MkdirAll(absolute, 0o700) != nil {
		return nil, nil, ErrUnavailable
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	lock, err := lockProfile(canonical)
	if err != nil {
		return nil, nil, err
	}
	store, err := openStore(canonical)
	return store, lock, err
}

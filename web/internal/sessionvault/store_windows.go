package sessionvault

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	windowsSessionFile    = ".chatgpt-session.dpapi"
	maxWindowsSealedBytes = maxRecordBytes + (64 << 10)
	cryptProtectNoUI      = 0x1
	moveFileReplace       = 0x1
	moveFileWriteThrough  = 0x8
	windowsSharingError   = syscall.Errno(32)
	windowsLockingError   = syscall.Errno(33)
)

var (
	windowsKernel          = syscall.NewLazyDLL("kernel32.dll")
	windowsSystemDirectory = windowsKernel.NewProc("GetSystemDirectoryW")
	windowsMoveFile        = windowsKernel.NewProc("MoveFileExW")
)

type windowsStore struct {
	path      string
	protect   *syscall.LazyProc
	unprotect *syscall.LazyProc
}

func openStore(profileDir string) (Store, error) {
	if windowsSystemDirectory.Find() != nil || windowsMoveFile.Find() != nil {
		return nil, ErrUnavailable
	}
	var dir [syscall.MAX_PATH]uint16
	n, _, _ := windowsSystemDirectory.Call(uintptr(unsafe.Pointer(&dir[0])), uintptr(len(dir)))
	if n == 0 || n >= uintptr(len(dir)) {
		return nil, ErrUnavailable
	}
	crypt := syscall.NewLazyDLL(filepath.Join(syscall.UTF16ToString(dir[:n]), "crypt32.dll"))
	protect := crypt.NewProc("CryptProtectData")
	unprotect := crypt.NewProc("CryptUnprotectData")
	if protect.Find() != nil || unprotect.Find() != nil {
		return nil, ErrUnavailable
	}
	return &windowsStore{filepath.Join(profileDir, windowsSessionFile), protect, unprotect}, nil
}

func lockProfile(profileDir string) (io.Closer, error) {
	path := filepath.Join(profileDir, ".chatgpt-session.lock")
	name, err := windowsPath(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	// No sharing: Windows releases the exclusive handle even after a crash.
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windowsSharingError) || errors.Is(err, windowsLockingError) {
		return nil, ErrInUse
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	return os.NewFile(uintptr(handle), path), nil
}

func (store *windowsStore) Load() ([]byte, error) {
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxWindowsSealedBytes {
		return nil, ErrUnavailable
	}
	sealed, err := io.ReadAll(io.LimitReader(file, maxWindowsSealedBytes+1))
	if err != nil || len(sealed) == 0 || len(sealed) > maxWindowsSealedBytes {
		return nil, ErrUnavailable
	}
	return transformWindowsBlob(store.unprotect, sealed, maxRecordBytes)
}

func (store *windowsStore) Save(record []byte) error {
	if len(record) == 0 || len(record) > maxRecordBytes {
		return ErrUnavailable
	}
	sealed, err := transformWindowsBlob(store.protect, record, maxWindowsSealedBytes)
	if err != nil {
		return err
	}
	// Stage only ciphertext, in the destination directory on the same volume.
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".chatgpt-session-staging-*")
	if err != nil {
		return ErrUnavailable
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(sealed); err != nil {
		_ = temporary.Close()
		return ErrUnavailable
	}
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if syncErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	from, fromErr := windowsPath(temporary.Name())
	to, toErr := windowsPath(store.path)
	if fromErr != nil || toErr != nil {
		return ErrUnavailable
	}
	ok, _, _ := windowsMoveFile.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), moveFileReplace|moveFileWriteThrough)
	if ok == 0 {
		return ErrUnavailable
	}
	return nil
}

func (store *windowsStore) Delete() error {
	if err := os.Remove(store.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	return nil
}

type windowsDataBlob struct {
	size uint32
	data *byte
}

func transformWindowsBlob(proc *syscall.LazyProc, input []byte, limit int) ([]byte, error) {
	// Match both DPAPI operations' DATA_BLOB arguments. No machine scope flag
	// is set, so only the current Windows user can restore the record.
	namespace := []byte("Brief Studio Web ChatGPT session v1")
	in := windowsDataBlob{uint32(len(input)), &input[0]}
	entropy := windowsDataBlob{uint32(len(namespace)), &namespace[0]}
	var out windowsDataBlob
	ok, _, _ := proc.Call(
		uintptr(unsafe.Pointer(&in)), 0, uintptr(unsafe.Pointer(&entropy)), 0, 0,
		cryptProtectNoUI, uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(input)
	runtime.KeepAlive(namespace)
	if out.data != nil {
		defer syscall.LocalFree(syscall.Handle(unsafe.Pointer(out.data)))
	}
	if ok == 0 || out.data == nil || out.size == 0 || out.size > uint32(limit) {
		return nil, ErrUnavailable
	}
	data := unsafe.Slice(out.data, int(out.size))
	result := append([]byte(nil), data...)
	clear(data)
	return result, nil
}

func windowsPath(path string) (*uint16, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	// Enable long paths for the calls that bypass Go's os package.
	if !strings.HasPrefix(absolute, `\\?\`) && !strings.HasPrefix(absolute, `\\.\`) {
		if strings.HasPrefix(absolute, `\\`) {
			absolute = `\\?\UNC\` + absolute[2:]
		} else {
			absolute = `\\?\` + absolute
		}
	}
	return syscall.UTF16PtrFromString(absolute)
}

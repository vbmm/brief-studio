//go:build darwin && cgo

package sessionvault

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>

static CFDataRef briefStudioCodeHash(void) {
	SecCodeRef code = NULL;
	SecStaticCodeRef executable = NULL;
	CFDictionaryRef info = NULL;
	CFDataRef hash = NULL;
	if (SecCodeCopySelf(kSecCSDefaultFlags, &code) == errSecSuccess &&
	    SecCodeCopyStaticCode(code, kSecCSDefaultFlags, &executable) == errSecSuccess &&
	    SecCodeCopySigningInformation(executable, kSecCSSigningInformation, &info) == errSecSuccess) {
		CFTypeRef value = CFDictionaryGetValue(info, kSecCodeInfoUnique);
		if (value && CFGetTypeID(value) == CFDataGetTypeID()) {
			hash = (CFDataRef)CFRetain(value);
		}
	}
	if (info) CFRelease(info);
	if (executable) CFRelease(executable);
	if (code) CFRelease(code);
	return hash;
}

static CFMutableDictionaryRef sessionQuery(const char *account) {
	CFStringRef accountString = CFStringCreateWithCString(NULL, account, kCFStringEncodingASCII);
	if (!accountString) return NULL;
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(
	    NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (query) {
		CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
		CFDictionarySetValue(query, kSecAttrService, CFSTR("com.briefstudio.web.chatgpt-session.v1"));
		CFDictionarySetValue(query, kSecAttrAccount, accountString);
		CFDictionarySetValue(query, kSecAttrSynchronizable, kCFBooleanFalse);
	}
	CFRelease(accountString);
	return query;
}

static OSStatus loadSession(const char *account, CFTypeRef *result) {
	CFMutableDictionaryRef query = sessionQuery(account);
	if (!query) return errSecAllocate;
	CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
	OSStatus status = SecItemCopyMatching(query, result);
	CFRelease(query);
	return status;
}

static OSStatus saveSession(const char *account, const UInt8 *bytes, CFIndex length) {
	CFMutableDictionaryRef query = sessionQuery(account);
	if (!query) return errSecAllocate;
	CFDataRef data = CFDataCreate(NULL, bytes, length);
	if (!data) {
		CFRelease(query);
		return errSecAllocate;
	}
	const void *keys[] = {kSecValueData};
	const void *values[] = {data};
	CFDictionaryRef update = CFDictionaryCreate(
	    NULL, keys, values, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	OSStatus status = update ? SecItemUpdate(query, update) : errSecAllocate;
	if (update) CFRelease(update);
	if (status == errSecItemNotFound) {
		SecAccessRef access = NULL;
		// A NULL trusted list grants unattended access only to the creator.
		status = SecAccessCreate(CFSTR("Brief Studio Web"), NULL, &access);
		if (status == errSecSuccess) {
			CFDictionarySetValue(query, kSecAttrAccess, access);
			CFDictionarySetValue(query, kSecValueData, data);
			status = SecItemAdd(query, NULL);
		}
		if (access) CFRelease(access);
	}
	CFRelease(data);
	CFRelease(query);
	return status;
}

static OSStatus deleteSession(const char *account) {
	CFMutableDictionaryRef query = sessionQuery(account);
	if (!query) return errSecAllocate;
	OSStatus status = SecItemDelete(query);
	CFRelease(query);
	return status;
}
*/
import "C"

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

type keychainStore struct {
	account string
}

// Open supplies the canonical profile path to both platform helpers.
func openStore(profileDir string) (Store, error) {
	cdhash := C.briefStudioCodeHash()
	if cdhash == 0 {
		return nil, fmt.Errorf("%w: cannot identify this Brief Studio executable", ErrUnavailable)
	}
	defer C.CFRelease(C.CFTypeRef(cdhash))
	length := C.CFDataGetLength(cdhash)
	if length < 1 || length > sha256.Size {
		return nil, fmt.Errorf("%w: invalid Brief Studio executable identity", ErrUnavailable)
	}
	code := C.GoBytes(unsafe.Pointer(C.CFDataGetBytePtr(cdhash)), C.int(length))
	// Ad hoc releases have per-build trust. A new build signs in to a new
	// account namespace instead of widening the previous item's ACL.
	profileHash := sha256.Sum256([]byte(profileDir))
	return &keychainStore{account: hex.EncodeToString(profileHash[:]) + "." + hex.EncodeToString(code)}, nil
}

func lockProfile(profileDir string) (io.Closer, error) {
	var dir syscall.Stat_t
	if err := syscall.Stat(profileDir, &dir); err != nil || dir.Mode&syscall.S_IFMT != syscall.S_IFDIR ||
		dir.Uid != uint32(os.Geteuid()) || dir.Mode&0o022 != 0 {
		return nil, ErrUnavailable
	}

	name := filepath.Join(profileDir, ".chatgpt-session.lock")
	fd, err := syscall.Open(name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, ErrUnavailable
	}
	var lock syscall.Stat_t
	if err := syscall.Fstat(fd, &lock); err != nil || lock.Mode&syscall.S_IFMT != syscall.S_IFREG ||
		lock.Uid != uint32(os.Geteuid()) || lock.Mode&0o077 != 0 || lock.Nlink != 1 {
		syscall.Close(fd)
		return nil, ErrUnavailable
	}
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		syscall.Close(fd)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrInUse
		}
		return nil, ErrUnavailable
	}
	return os.NewFile(uintptr(fd), name), nil
}

// The file-based keychain honors this process-wide setting. Serialize changes
// and restore it after each operation, following the native Swift client.
var keychainInteraction sync.Mutex

func (s *keychainStore) perform(op string, action func(*C.char) C.OSStatus) (C.OSStatus, error) {
	keychainInteraction.Lock()
	defer keychainInteraction.Unlock()
	var previous C.Boolean
	if status := C.SecKeychainGetUserInteractionAllowed(&previous); status != C.errSecSuccess {
		return status, keychainError(op, status)
	}
	if previous != 0 {
		if status := C.SecKeychainSetUserInteractionAllowed(0); status != C.errSecSuccess {
			return status, keychainError(op, status)
		}
		defer C.SecKeychainSetUserInteractionAllowed(previous)
	}
	account := C.CString(s.account)
	defer C.free(unsafe.Pointer(account))
	return action(account), nil
}

func keychainError(op string, status C.OSStatus) error {
	return fmt.Errorf("%w: cannot %s Brief Studio's local Keychain item (OSStatus %d)", ErrUnavailable, op, int(status))
}

func (s *keychainStore) Load() ([]byte, error) {
	var result C.CFTypeRef
	status, err := s.perform("read", func(account *C.char) C.OSStatus {
		return C.loadSession(account, &result)
	})
	if result != 0 {
		defer C.CFRelease(result)
	}
	if err != nil {
		return nil, err
	}
	if status == C.errSecItemNotFound {
		return nil, os.ErrNotExist
	}
	if status != C.errSecSuccess {
		return nil, keychainError("read", status)
	}
	if result == 0 || C.CFGetTypeID(result) != C.CFDataGetTypeID() {
		return nil, ErrUnavailable
	}
	data := C.CFDataRef(result)
	length := C.CFDataGetLength(data)
	if length < 1 || length > maxRecordBytes {
		return nil, ErrUnavailable
	}
	return C.GoBytes(unsafe.Pointer(C.CFDataGetBytePtr(data)), C.int(length)), nil
}

func (s *keychainStore) Save(record []byte) error {
	if len(record) == 0 || len(record) > maxRecordBytes {
		return ErrUnavailable
	}
	status, err := s.perform("save", func(account *C.char) C.OSStatus {
		return C.saveSession(account, (*C.UInt8)(unsafe.Pointer(&record[0])), C.CFIndex(len(record)))
	})
	if err != nil {
		return err
	}
	if status != C.errSecSuccess {
		return keychainError("save", status)
	}
	return nil
}

func (s *keychainStore) Delete() error {
	status, err := s.perform("delete", func(account *C.char) C.OSStatus {
		return C.deleteSession(account)
	})
	if err != nil {
		return err
	}
	if status != C.errSecSuccess && status != C.errSecItemNotFound {
		return keychainError("delete", status)
	}
	return nil
}

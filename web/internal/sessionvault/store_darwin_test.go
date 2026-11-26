//go:build darwin && cgo

package sessionvault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the profile identity and process lock without reading or writing
// the user's Keychain. Open only inspects the running executable's signature.
func TestMacVaultCanonicalProfileAndLock(t *testing.T) {
	profile := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(profile, alias); err != nil {
		t.Fatal(err)
	}
	first, lock, err := Open(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if _, duplicate, err := Open(alias); duplicate != nil || !errors.Is(err, ErrInUse) {
		if duplicate != nil {
			_ = duplicate.Close()
		}
		t.Fatalf("profile alias must share lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	second, reopened, err := Open(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if first.(*keychainStore).account != second.(*keychainStore).account {
		t.Fatal("profile alias changed Keychain namespace")
	}

	other, otherLock, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer otherLock.Close()
	if first.(*keychainStore).account == other.(*keychainStore).account {
		t.Fatal("distinct profiles share Keychain namespace")
	}
}

func TestMacVaultRejectsSymlinkLock(t *testing.T) {
	profile := t.TempDir()
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(profile, ".chatgpt-session.lock")); err != nil {
		t.Fatal(err)
	}
	if _, lock, err := Open(profile); lock != nil || !errors.Is(err, ErrUnavailable) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("profile lock symlink must be rejected: %v", err)
	}
	current, err := os.ReadFile(target)
	if err != nil || string(current) != "fixture" {
		t.Fatal("unrelated file was modified")
	}
}

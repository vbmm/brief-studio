package sessionvault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsVaultCiphertextRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty vault: %v", err)
	}
	first := []byte("synthetic refresh token record A")
	second := []byte("synthetic refresh token record B")
	for _, record := range [][]byte{first, second} {
		if err := store.Save(record); err != nil {
			t.Fatal(err)
		}
		onDisk, err := os.ReadFile(filepath.Join(dir, windowsSessionFile))
		if err != nil || len(onDisk) == 0 || bytes.Contains(onDisk, record) {
			t.Fatalf("missing ciphertext or plaintext exposed on disk: %v", err)
		}
		reopened, err := openStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := reopened.Load()
		if err != nil || !bytes.Equal(loaded, record) {
			t.Fatalf("reopened vault did not restore the record: %v", err)
		}
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("vault after deletion: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("repeat vault deletion: %v", err)
	}
}

func TestWindowsVaultRejectsInvalidRecords(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(make([]byte, maxRecordBytes+1)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("oversized record: %v", err)
	}
	for _, invalid := range [][]byte{[]byte("not DPAPI"), make([]byte, maxWindowsSealedBytes+1)} {
		if err := os.WriteFile(filepath.Join(dir, windowsSessionFile), invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("invalid ciphertext: %v", err)
		}
	}
}

func TestWindowsVaultProfileLockReleasesOnClose(t *testing.T) {
	dir := t.TempDir()
	first, err := lockProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if second, err := lockProfile(dir); !errors.Is(err, ErrInUse) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("duplicate profile lock: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := lockProfile(dir)
	if err != nil {
		t.Fatalf("reacquiring released profile lock: %v", err)
	}
	_ = second.Close()
}

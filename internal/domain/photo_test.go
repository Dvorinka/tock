package domain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnlinkProfilePhoto(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Base is flattened under profile-photos/, so this must not touch the root file.
	unlinkProfilePhoto(dir, "profile-photos/../secret.txt")
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("path escaped the photo directory: %v", err)
	}
	unlinkProfilePhoto(dir, "exports/secret.txt")
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("non-photo path was removed: %v", err)
	}

	photoDir := filepath.Join(dir, "profile-photos")
	if err := os.MkdirAll(photoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	photo := filepath.Join(photoDir, "a.png")
	if err := os.WriteFile(photo, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	unlinkProfilePhoto(dir, "profile-photos/a.png")
	if _, err := os.Stat(photo); !os.IsNotExist(err) {
		t.Fatalf("photo remains: %v", err)
	}
	unlinkProfilePhoto("", "profile-photos/a.png")
}

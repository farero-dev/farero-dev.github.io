package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSupportDirEnvOverride(t *testing.T) {
	t.Setenv("FARERO_HOME", "/tmp/frp-home")
	if got := SupportDir(); got != "/tmp/frp-home" {
		t.Fatalf("SupportDir = %q", got)
	}
}

func TestSupportDirDefault(t *testing.T) {
	t.Setenv("FARERO_HOME", "")
	t.Setenv("HOME", "/tmp/frp-fakehome")
	want := filepath.Join("/tmp/frp-fakehome", "Library", "Application Support", "Farero")
	if got := SupportDir(); got != want {
		t.Fatalf("SupportDir = %q, want %q", got, want)
	}
}

func TestSupportDirNoHome(t *testing.T) {
	t.Setenv("FARERO_HOME", "")
	t.Setenv("HOME", "")
	want := filepath.Join("/tmp", "Library", "Application Support", "Farero")
	if got := SupportDir(); got != want {
		t.Fatalf("SupportDir = %q, want %q", got, want)
	}
}

func TestSocket(t *testing.T) {
	t.Setenv("FARERO_HOME", "/tmp/frp-home")
	t.Setenv("FARERO_SOCKET", "")
	if got, want := Socket(), "/tmp/frp-home/farerod.sock"; got != want {
		t.Fatalf("Socket = %q, want %q", got, want)
	}
	t.Setenv("FARERO_SOCKET", "/tmp/frp.sock")
	if got := Socket(); got != "/tmp/frp.sock" {
		t.Fatalf("Socket override = %q", got)
	}
}

func TestDerivedPaths(t *testing.T) {
	t.Setenv("FARERO_HOME", "/tmp/frp-home")
	for got, want := range map[string]string{
		DB():      "/tmp/frp-home/farero.db",
		Config():  "/tmp/frp-home/config.json",
		Backups(): "/tmp/frp-home/backups",
		Secrets(): "/tmp/frp-home/dev-secrets.json",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestEnsure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	t.Setenv("FARERO_HOME", dir)
	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("not created: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", fi.Mode().Perm())
	}
	// A file in the way is an error.
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, nil, 0o600)
	t.Setenv("FARERO_HOME", filepath.Join(file, "sub"))
	if err := Ensure(); err == nil {
		t.Fatal("Ensure under a file should fail")
	}
}

package secret

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func testStore(t *testing.T, s Store) {
	t.Helper()
	if _, err := s.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v", err)
	}
	if err := s.Set("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("other", "o"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("k"); err != nil || v != "v1" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if err := s.Set("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get("k"); v != "v2" {
		t.Fatalf("overwrite = %q", v)
	}
	if err := s.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
	if v, err := s.Get("other"); err != nil || v != "o" {
		t.Fatalf("other = %q, %v", v, err)
	}
	// Deleting a missing key is not an error.
	if err := s.Delete("k"); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryStore(t *testing.T) { testStore(t, NewMemoryStore()) }

func TestFileStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "secrets.json")
	s := NewFileStore(path)
	testStore(t, s)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", di.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file left behind: %v", err)
	}
	// A second store on the same file sees the data.
	s.Set("persist", "yes")
	if v, err := NewFileStore(path).Get("persist"); err != nil || v != "yes" {
		t.Fatalf("persist = %q, %v", v, err)
	}
}

func TestFileStoreErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	os.WriteFile(path, []byte("{broken"), 0o600)
	s := NewFileStore(path)
	if _, err := s.Get("k"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Get corrupt = %v", err)
	}
	if err := s.Set("k", "v"); err == nil {
		t.Error("Set on corrupt file should fail")
	}
	if err := s.Delete("k"); err == nil {
		t.Error("Delete on corrupt file should fail")
	}
	// A directory where the file should be: read fails with a non-NotExist error.
	d := filepath.Join(dir, "isdir")
	os.Mkdir(d, 0o700)
	if _, err := NewFileStore(d).Get("k"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Get on directory = %v", err)
	}
	// Parent is a regular file: save cannot create the directory.
	f := filepath.Join(dir, "file")
	os.WriteFile(f, nil, 0o600)
	if err := NewFileStore(filepath.Join(f, "s.json")).Set("k", "v"); err == nil {
		t.Error("Set under a file should fail")
	}
	// Read-only directory: the temp file cannot be written.
	ro := filepath.Join(dir, "ro")
	os.Mkdir(ro, 0o500)
	if os.Geteuid() != 0 {
		if err := NewFileStore(filepath.Join(ro, "s.json")).Set("k", "v"); err == nil {
			t.Error("Set in read-only dir should fail")
		}
	}
}

func TestFileStoreConcurrent(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "s.json"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				s.Set("k"+string(rune('a'+i)), "v")
			}
		}()
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		if _, err := s.Get("k" + string(rune('a'+i))); err != nil {
			t.Errorf("lost key %d: %v", i, err)
		}
	}
}

func TestPluginKey(t *testing.T) {
	if got := PluginKey("github", "token"); got != "plugin.github.token" {
		t.Fatalf("PluginKey = %q", got)
	}
	if KeyGatewaySecret != "gateway.secret" {
		t.Fatal("KeyGatewaySecret changed")
	}
}

func TestRandomToken(t *testing.T) {
	a, b := RandomToken(), RandomToken()
	if len(a) != 64 || len(b) != 64 {
		t.Fatalf("len = %d, %d", len(a), len(b))
	}
	if a == b {
		t.Fatal("tokens repeat")
	}
	for _, c := range a {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			t.Fatalf("not hex: %q", a)
		}
	}
}

type failStore struct {
	getErr, setErr error
}

func (f failStore) Get(string) (string, error) { return "", f.getErr }
func (f failStore) Set(string, string) error   { return f.setErr }
func (f failStore) Delete(string) error        { return nil }

func TestGetOrCreate(t *testing.T) {
	s := NewMemoryStore()
	calls := 0
	gen := func() string { calls++; return "gen" }
	v, err := GetOrCreate(s, "k", gen)
	if err != nil || v != "gen" || calls != 1 {
		t.Fatalf("first = %q, %v, %d calls", v, err, calls)
	}
	if stored, _ := s.Get("k"); stored != "gen" {
		t.Fatalf("not stored: %q", stored)
	}
	v, err = GetOrCreate(s, "k", gen)
	if err != nil || v != "gen" || calls != 1 {
		t.Fatalf("second = %q, %v, %d calls", v, err, calls)
	}
	// A real error from Get is returned and nothing is generated.
	boom := errors.New("boom")
	v, err = GetOrCreate(failStore{getErr: boom}, "k", gen)
	if !errors.Is(err, boom) || v != "" || calls != 1 {
		t.Fatalf("get error = %q, %v, %d calls", v, err, calls)
	}
	// A failing Set is reported.
	setBoom := errors.New("set boom")
	_, err = GetOrCreate(failStore{getErr: ErrNotFound, setErr: setBoom}, "k", gen)
	if !errors.Is(err, setBoom) {
		t.Fatalf("set error = %v", err)
	}
}

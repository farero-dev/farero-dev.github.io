package agentcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// del removes a key and keeps the order of the rest, wherever the key was.
func TestObjectDelKeepsOrder(t *testing.T) {
	for _, tc := range []struct {
		del  string
		want []string
	}{
		{"a", []string{"b", "c"}},
		{"b", []string{"a", "c"}},
		{"c", []string{"a", "b"}},
		{"x", []string{"a", "b", "c"}},
	} {
		o, err := parseObject([]byte(`{"a":1,"b":2,"c":3}`))
		if err != nil {
			t.Fatal(err)
		}
		o.del(tc.del)
		if !slices.Equal(o.keys, tc.want) {
			t.Errorf("del %q: keys %v, want %v", tc.del, o.keys, tc.want)
		}
		if _, ok := o.get(tc.del); ok {
			t.Errorf("del %q: still there", tc.del)
		}
	}
}

// A failed atomic write leaves the target alone and no temporary file
// behind.
func TestWriteAtomicFailureCleansUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.json")
	if err := os.MkdirAll(filepath.Join(target, "keep"), 0o755); err != nil {
		t.Fatal(err) // a non-empty directory: rename onto it fails
	}
	if err := writeAtomic(target, []byte(`{}`), 0o644); err == nil {
		t.Fatal("expected an error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("left behind: %v", names)
	}
	// And a successful one replaces the file with the given mode.
	file := filepath.Join(dir, "ok.json")
	if err := writeAtomic(file, []byte(`{"a":1}`), 0o640); err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	b, _ := os.ReadFile(file)
	fi, _ := os.Stat(file)
	if json.Unmarshal(b, &v) != nil || v["a"] != 1 || fi.Mode().Perm() != 0o640 {
		t.Fatalf("written %q mode %v", b, fi.Mode().Perm())
	}
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArgumentsFailBeforeDatabase(t *testing.T) {
	t.Setenv("HEIMDALL_KEY_DATABASE_URL", "postgres://private-secret@invalid")
	for _, args := range [][]string{{}, {"--status", "--initialize"}, {"--register", "--key-file", "private-file", "--limit", "1"}, {"--retire", "--fingerprint", "bad"}, {"--status", "--key-file", "private-file"}, {"--initialize"}, {"--register", "--fresh-key", "--key-file", "private-file", "--limit", "4294967296"}, {"--status", "--password=private-secret"}} {
		var out, diagnostic bytes.Buffer
		if code := run(args, &out, &diagnostic); code != 2 {
			t.Fatalf("expected usage failure, got %d", code)
		}
		if out.Len() != 0 || strings.Contains(diagnostic.String(), "private-secret") || strings.Contains(diagnostic.String(), "private-file") {
			t.Fatal("echoed sensitive input")
		}
	}
}
func TestKeyFileValidation(t *testing.T) {
	for _, test := range []struct {
		name  string
		size  int
		mode  os.FileMode
		valid bool
	}{{"valid", 32, 0600, true}, {"short", 31, 0600, false}, {"long", 33, 0600, false}, {"public", 32, 0644, false}} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, bytes.Repeat([]byte{7}, test.size), test.mode); err != nil {
				t.Fatal(err)
			}
			key, err := readKey(path)
			if (err == nil) != test.valid {
				t.Fatal("incorrect validation")
			}
			if test.valid && !bytes.Equal(key, bytes.Repeat([]byte{7}, 32)) {
				t.Fatal("changed key")
			}
		})
	}
	if _, err := readKey(t.TempDir()); err == nil {
		t.Fatal("accepted directory")
	}
}

func TestHelpAndSymlink(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if run([]string{"--help"}, &out, &diagnostic) != 0 || !strings.Contains(out.String(), "fresh-key") || diagnostic.Len() != 0 {
		t.Fatal("missing help")
	}
	root := t.TempDir()
	key := filepath.Join(root, "key")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(key, bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(key, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := readKey(link); err == nil {
		t.Fatal("accepted symlink")
	}
}

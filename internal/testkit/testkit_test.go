package testkit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestUTF16(t *testing.T) {
	if got := UTF16("aб", false); !bytes.Equal(got, []byte{0xFF, 0xFE, 'a', 0, 0x31, 0x04}) {
		t.Errorf("LE: % x", got)
	}
	if got := UTF16("aб", true); !bytes.Equal(got, []byte{0xFE, 0xFF, 0, 'a', 0x04, 0x31}) {
		t.Errorf("BE: % x", got)
	}
}

func TestTreesCreated(t *testing.T) {
	root := t.TempDir()
	files := IgnoreTree(t, root)
	if len(files) < 40 {
		t.Errorf("дерево слишком бедное: %d файлов", len(files))
	}
	for _, rel := range files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s не создан: %v", rel, err)
		}
	}
	scanRoot := t.TempDir()
	ScanTree(t, scanRoot)
	if b, err := os.ReadFile(filepath.Join(scanRoot, "nul.bin")); err != nil || !bytes.Contains(b, []byte{0}) {
		t.Errorf("nul.bin без NUL-байта: %v", err)
	}
}

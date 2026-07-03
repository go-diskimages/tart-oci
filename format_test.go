package tartoci

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Compile-time assertion that Format satisfies the go-diskimages format
// interfaces structurally (mirrors the method set of diskimage_format.Format).
var _ interface {
	Name() string
	Create(string, int64) error
	Detect(string) (bool, error)
	ToRaw(string, string, io.Writer) error
	Resize(string, int64) error
} = Format{}

func TestFormatName(t *testing.T) {
	if got := (Format{}).Name(); got != "tart-oci" {
		t.Errorf("Name() = %q", got)
	}
}

func TestFormatCreateAndRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "img")
	const size = 200 * 1024
	if err := (Format{}).Create(dir, size); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ok, err := (Format{}).Detect(dir)
	if err != nil || !ok {
		t.Fatalf("Detect = (%v, %v), want (true, nil)", ok, err)
	}
	dst := filepath.Join(t.TempDir(), "disk.raw")
	if err := (Format{}).ToRaw(dir, dst, io.Discard); err != nil {
		t.Fatalf("ToRaw: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Errorf("raw size = %d, want %d", info.Size(), size)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, make([]byte, size)) {
		t.Error("expected all-zero disk")
	}
}

func TestFormatCreateInvalidSize(t *testing.T) {
	if err := (Format{}).Create(t.TempDir(), 0); err == nil {
		t.Fatal("expected error for size 0")
	}
}

func TestFormatCreateWriteError(t *testing.T) {
	skipIfRoot(t)
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	if err := (Format{}).Create(filepath.Join(file, "layout"), 512); err == nil {
		t.Fatal("expected writeLayout error")
	}
}

func TestFormatDetect(t *testing.T) {
	t.Run("not exist", func(t *testing.T) {
		ok, err := (Format{}).Detect(filepath.Join(t.TempDir(), "nope"))
		if ok || err == nil {
			t.Fatalf("Detect = (%v, %v), want (false, err)", ok, err)
		}
	})
	t.Run("not a dir", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "f")
		_ = os.WriteFile(file, []byte("x"), 0o600)
		ok, err := (Format{}).Detect(file)
		if ok || err != nil {
			t.Fatalf("Detect = (%v, %v), want (false, nil)", ok, err)
		}
	})
	t.Run("empty dir", func(t *testing.T) {
		ok, err := (Format{}).Detect(t.TempDir())
		if ok || err != nil {
			t.Fatalf("Detect = (%v, %v), want (false, nil)", ok, err)
		}
	})
}

func TestFormatToRawError(t *testing.T) {
	if err := (Format{}).ToRaw(t.TempDir(), filepath.Join(t.TempDir(), "d"), io.Discard); err == nil {
		t.Fatal("expected ToRaw error on a non-layout directory")
	}
}

func TestFormatResizeUnsupported(t *testing.T) {
	if err := (Format{}).Resize(t.TempDir(), 1024); err == nil {
		t.Fatal("expected Resize to be unsupported")
	}
}

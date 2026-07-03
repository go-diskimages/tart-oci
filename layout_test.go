package tartoci

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-compressions/lz4"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("skipping permission-based test as root")
	}
}

func TestBlobPath(t *testing.T) {
	got := BlobPath("/cache", "sha256:abc123")
	if want := filepath.Join("/cache", "blobs", "sha256", "abc123"); got != want {
		t.Errorf("BlobPath = %q, want %q", got, want)
	}
	// A digest without the "algo:" shape lands directly under blobs/.
	got = BlobPath("/cache", "short")
	if want := filepath.Join("/cache", "blobs", "short"); got != want {
		t.Errorf("BlobPath(short) = %q, want %q", got, want)
	}
}

func TestEncodeAppleFrameRoundTrip(t *testing.T) {
	// From github.com/go-compressions/lz4, via the frames we build.
	cases := [][]byte{
		nil,
		[]byte("small"),
		bytes.Repeat([]byte("block-boundary-crossing-"), 6000), // > 64 KiB, multiple blocks
	}
	for i, raw := range cases {
		got, err := lz4.DecompressApple(encodeAppleFrame(raw))
		if err != nil {
			t.Fatalf("case %d: decode: %v", i, err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatalf("case %d: round-trip mismatch (%d vs %d)", i, len(got), len(raw))
		}
	}
}

func TestReadManifestErrors(t *testing.T) {
	t.Run("missing index", func(t *testing.T) {
		if _, err := readManifest(t.TempDir()); err == nil {
			t.Fatal("expected missing index.json error")
		}
	})
	t.Run("bad index json", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "index.json"), []byte("{bad"), 0o600)
		if _, err := readManifest(dir); err == nil {
			t.Fatal("expected parse error")
		}
	})
	t.Run("no manifests", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"manifests":[]}`), 0o600)
		if _, err := readManifest(dir); err == nil {
			t.Fatal("expected no-manifests error")
		}
	})
	t.Run("missing manifest blob", func(t *testing.T) {
		dir := t.TempDir()
		idx, _ := json.Marshal(index{Manifests: []Descriptor{{Digest: "sha256:deadbeef"}}})
		_ = os.WriteFile(filepath.Join(dir, "index.json"), idx, 0o600)
		if _, err := readManifest(dir); err == nil {
			t.Fatal("expected missing manifest blob error")
		}
	})
	t.Run("bad manifest json", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755)
		bad := []byte("{not json")
		_ = os.WriteFile(BlobPath(dir, digest(bad)), bad, 0o600)
		idx, _ := json.Marshal(index{Manifests: []Descriptor{{Digest: digest(bad)}}})
		_ = os.WriteFile(filepath.Join(dir, "index.json"), idx, 0o600)
		if _, err := readManifest(dir); err == nil {
			t.Fatal("expected manifest parse error")
		}
	})
}

func TestExtractDiskRoundTrip(t *testing.T) {
	raw := bytes.Repeat([]byte("layout-data-"), 4000)
	dir := filepath.Join(t.TempDir(), "layout")
	if err := writeLayout(dir, raw); err != nil {
		t.Fatalf("writeLayout: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "disk.raw")
	var log bytes.Buffer
	if err := ExtractDisk(dir, dst, &log); err != nil {
		t.Fatalf("ExtractDisk: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, raw) {
		t.Fatalf("extract mismatch (%d vs %d)", len(got), len(raw))
	}
}

func TestExtractDiskErrors(t *testing.T) {
	goodDir := filepath.Join(t.TempDir(), "good")
	if err := writeLayout(goodDir, []byte("disk")); err != nil {
		t.Fatal(err)
	}

	t.Run("read manifest error", func(t *testing.T) {
		if err := ExtractDisk(t.TempDir(), filepath.Join(t.TempDir(), "d"), io.Discard); err == nil {
			t.Fatal("expected read manifest error")
		}
	})
	t.Run("no disk layers", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755)
		m, _ := json.Marshal(Manifest{Layers: []Descriptor{{MediaType: MediaTypeConfig, Digest: "sha256:x"}}})
		_ = os.WriteFile(BlobPath(dir, digest(m)), m, 0o600)
		idx, _ := json.Marshal(index{Manifests: []Descriptor{{Digest: digest(m)}}})
		_ = os.WriteFile(filepath.Join(dir, "index.json"), idx, 0o600)
		if err := ExtractDisk(dir, filepath.Join(t.TempDir(), "d"), io.Discard); err == nil {
			t.Fatal("expected no-disk-layers error")
		}
	})
	t.Run("create dst error", func(t *testing.T) {
		if err := ExtractDisk(goodDir, filepath.Join(t.TempDir(), "nodir", "d"), io.Discard); err == nil {
			t.Fatal("expected create error")
		}
	})
	t.Run("layer extract error", func(t *testing.T) {
		// Build a valid layout, then corrupt the disk blob so extractLayer fails
		// from inside ExtractDisk's loop.
		dir := filepath.Join(t.TempDir(), "corrupt")
		if err := writeLayout(dir, []byte("disk contents")); err != nil {
			t.Fatal(err)
		}
		m, err := readManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		diskBlob := BlobPath(dir, m.DiskLayers()[0].Digest)
		_ = os.WriteFile(diskBlob, []byte("no longer a valid frame"), 0o600)
		if err := ExtractDisk(dir, filepath.Join(t.TempDir(), "d"), io.Discard); err == nil {
			t.Fatal("expected extract-layer error")
		}
	})
	t.Run("fsync error", func(t *testing.T) {
		defer restoreFsync(injectFsyncError())
		if err := ExtractDisk(goodDir, filepath.Join(t.TempDir(), "d"), io.Discard); err == nil {
			t.Fatal("expected fsync error")
		}
	})
}

func TestExtractLayerErrors(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755)
	raw := []byte("hello disk data")
	frame := encodeAppleFrame(raw)
	_ = os.WriteFile(BlobPath(dir, digest(frame)), frame, 0o600)
	good := Descriptor{MediaType: MediaTypeDisk, Digest: digest(frame), Annotations: map[string]string{
		AnnUncompressedSize: "15", AnnUncompressedDigest: digest(raw)}}

	t.Run("open blob error", func(t *testing.T) {
		bad := Descriptor{Digest: "sha256:missing"}
		if err := extractLayer(dir, bad, io.Discard); err == nil {
			t.Fatal("expected open error")
		}
	})
	t.Run("decompress error", func(t *testing.T) {
		junk := []byte("not a frame at all")
		_ = os.WriteFile(BlobPath(dir, digest(junk)), junk, 0o600)
		bad := Descriptor{Digest: digest(junk)}
		if err := extractLayer(dir, bad, io.Discard); err == nil {
			t.Fatal("expected decompress error")
		}
	})
	t.Run("uncompressed digest mismatch", func(t *testing.T) {
		bad := good
		bad.Annotations = map[string]string{AnnUncompressedDigest: digest([]byte("wrong"))}
		if err := extractLayer(dir, bad, io.Discard); err == nil {
			t.Fatal("expected digest mismatch")
		}
	})
	t.Run("size mismatch", func(t *testing.T) {
		bad := good
		bad.Annotations = map[string]string{AnnUncompressedSize: "999"}
		if err := extractLayer(dir, bad, io.Discard); err == nil {
			t.Fatal("expected size mismatch")
		}
	})
	t.Run("bad size annotation", func(t *testing.T) {
		bad := good
		bad.Annotations = map[string]string{AnnUncompressedSize: "xyz"}
		if err := extractLayer(dir, bad, io.Discard); err == nil {
			t.Fatal("expected bad-size error")
		}
	})
	t.Run("success", func(t *testing.T) {
		var buf bytes.Buffer
		if err := extractLayer(dir, good, &buf); err != nil {
			t.Fatalf("extractLayer: %v", err)
		}
		if !bytes.Equal(buf.Bytes(), raw) {
			t.Fatal("content mismatch")
		}
	})
}

func TestWriteLayoutErrors(t *testing.T) {
	skipIfRoot(t)
	t.Run("mkdir error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		_ = os.WriteFile(file, []byte("x"), 0o600)
		if err := writeLayout(filepath.Join(file, "layout"), []byte("x")); err == nil {
			t.Fatal("expected mkdir error")
		}
	})
	t.Run("write blob error", func(t *testing.T) {
		dir := t.TempDir()
		blobDir := filepath.Join(dir, "blobs", "sha256")
		_ = os.MkdirAll(blobDir, 0o755)
		_ = os.Chmod(blobDir, 0o555) // read-only: blob WriteFile fails
		defer os.Chmod(blobDir, 0o755)
		if err := writeLayout(dir, []byte("x")); err == nil {
			t.Fatal("expected write-blob error")
		}
	})
	t.Run("write index error", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755)
		_ = os.Chmod(dir, 0o555) // root read-only: index.json WriteFile fails
		defer os.Chmod(dir, 0o755)
		if err := writeLayout(dir, []byte("x")); err == nil {
			t.Fatal("expected write-index error")
		}
	})
}

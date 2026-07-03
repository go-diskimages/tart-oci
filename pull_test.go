package tartoci

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// blobResp is a 200 response carrying body as the blob content.
func blobResp(body io.Reader) *http.Response {
	rc, ok := body.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(body)
	}
	return &http.Response{StatusCode: 200, Body: rc, Header: make(http.Header)}
}

// regServingBlob returns a Registry whose every blob GET yields newBody().
func regServingBlob(newBody func() io.Reader) *Registry {
	return NewRegistry(Reference{Registry: "example.com", Repository: "org/img"},
		WithHTTPClient(clientWith(func(*http.Request) (*http.Response, error) {
			return blobResp(newBody()), nil
		})))
}

func TestWriteDiskLayer_Success(t *testing.T) {
	raw := bytes.Repeat([]byte("payload-"), 5000)
	layer, frame := diskLayerFor(raw)
	reg := regServingBlob(func() io.Reader { return bytes.NewReader(frame) })
	var out bytes.Buffer
	if err := reg.writeDiskLayer(context.Background(), layer, &out); err != nil {
		t.Fatalf("writeDiskLayer: %v", err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Fatalf("output mismatch: %d vs %d bytes", out.Len(), len(raw))
	}
}

func TestWriteDiskLayer_Errors(t *testing.T) {
	raw := []byte("some disk bytes here")
	layer, frame := diskLayerFor(raw)

	t.Run("blob fetch error", func(t *testing.T) {
		reg := NewRegistry(Reference{Registry: "example.com", Repository: "org/img"},
			WithHTTPClient(clientWith(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
			})))
		if err := reg.writeDiskLayer(context.Background(), layer, io.Discard); err == nil {
			t.Fatal("expected blob fetch error")
		}
	})
	t.Run("decompress error", func(t *testing.T) {
		reg := regServingBlob(func() io.Reader { return bytes.NewReader([]byte("not a frame")) })
		if err := reg.writeDiskLayer(context.Background(), layer, io.Discard); err == nil {
			t.Fatal("expected decompress error")
		}
	})
	t.Run("trailing read error", func(t *testing.T) {
		reg := regServingBlob(func() io.Reader {
			return io.MultiReader(bytes.NewReader(frame), errReadCloser{})
		})
		if err := reg.writeDiskLayer(context.Background(), layer, io.Discard); err == nil {
			t.Fatal("expected trailing read error")
		}
	})
	t.Run("compressed digest mismatch", func(t *testing.T) {
		bad := layer
		bad.Digest = digestOf([]byte("wrong")) // does not match frame's real digest
		reg := regServingBlob(func() io.Reader { return bytes.NewReader(frame) })
		if err := reg.writeDiskLayer(context.Background(), bad, io.Discard); err == nil {
			t.Fatal("expected compressed digest mismatch")
		}
	})
	t.Run("uncompressed digest mismatch", func(t *testing.T) {
		bad := layer
		bad.Annotations = map[string]string{AnnUncompressedDigest: digestOf([]byte("nope"))}
		reg := regServingBlob(func() io.Reader { return bytes.NewReader(frame) })
		if err := reg.writeDiskLayer(context.Background(), bad, io.Discard); err == nil {
			t.Fatal("expected uncompressed digest mismatch")
		}
	})
	t.Run("size mismatch", func(t *testing.T) {
		bad := layer
		bad.Annotations = map[string]string{AnnUncompressedSize: "999999"}
		reg := regServingBlob(func() io.Reader { return bytes.NewReader(frame) })
		if err := reg.writeDiskLayer(context.Background(), bad, io.Discard); err == nil {
			t.Fatal("expected size mismatch")
		}
	})
	t.Run("bad size annotation", func(t *testing.T) {
		bad := layer
		bad.Annotations = map[string]string{AnnUncompressedSize: "not-a-number"}
		reg := regServingBlob(func() io.Reader { return bytes.NewReader(frame) })
		if err := reg.writeDiskLayer(context.Background(), bad, io.Discard); err == nil {
			t.Fatal("expected bad-size-annotation error")
		}
	})
}

func TestFetchRawBlob(t *testing.T) {
	content := []byte(`{"config":"data"}`)
	d := Descriptor{MediaType: MediaTypeConfig, Digest: digestOf(content), Size: int64(len(content))}

	t.Run("success", func(t *testing.T) {
		reg := regServingBlob(func() io.Reader { return bytes.NewReader(content) })
		dst := filepath.Join(t.TempDir(), "config.json")
		if err := reg.fetchRawBlob(context.Background(), d, dst); err != nil {
			t.Fatalf("fetchRawBlob: %v", err)
		}
		got, _ := os.ReadFile(dst)
		if !bytes.Equal(got, content) {
			t.Fatalf("content mismatch")
		}
	})
	t.Run("blob fetch error", func(t *testing.T) {
		reg := NewRegistry(Reference{Registry: "example.com", Repository: "org/img"},
			WithHTTPClient(clientWith(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial failed")
			})))
		if err := reg.fetchRawBlob(context.Background(), d, filepath.Join(t.TempDir(), "c")); err == nil {
			t.Fatal("expected blob fetch error")
		}
	})
	t.Run("create error", func(t *testing.T) {
		reg := regServingBlob(func() io.Reader { return bytes.NewReader(content) })
		// dst inside a path that is a file, not a directory.
		file := filepath.Join(t.TempDir(), "afile")
		_ = os.WriteFile(file, []byte("x"), 0o600)
		if err := reg.fetchRawBlob(context.Background(), d, filepath.Join(file, "c")); err == nil {
			t.Fatal("expected create error")
		}
	})
	t.Run("download error", func(t *testing.T) {
		reg := regServingBlob(func() io.Reader { return errReadCloser{} })
		if err := reg.fetchRawBlob(context.Background(), d, filepath.Join(t.TempDir(), "c")); err == nil {
			t.Fatal("expected download error")
		}
	})
	t.Run("digest mismatch", func(t *testing.T) {
		bad := Descriptor{Digest: digestOf([]byte("other"))}
		reg := regServingBlob(func() io.Reader { return bytes.NewReader(content) })
		if err := reg.fetchRawBlob(context.Background(), bad, filepath.Join(t.TempDir(), "c")); err == nil {
			t.Fatal("expected digest mismatch")
		}
	})
	t.Run("fsync error", func(t *testing.T) {
		defer restoreFsync(injectFsyncError())
		reg := regServingBlob(func() io.Reader { return bytes.NewReader(content) })
		if err := reg.fetchRawBlob(context.Background(), d, filepath.Join(t.TempDir(), "c")); err == nil {
			t.Fatal("expected fsync error")
		}
	})
}

func TestPullDisk_EndToEnd(t *testing.T) {
	c1 := bytes.Repeat([]byte{0xAA}, 4096)
	c2 := bytes.Repeat([]byte{0x55, 0x11}, 3000)
	img := buildImage(t, "cirruslabs/base", [][]byte{c1, c2}, nil, nil)
	srv := img.serve(t, false)

	dst := filepath.Join(t.TempDir(), "disk.raw")
	var log bytes.Buffer
	if err := PullDisk(context.Background(), img.ref(srv), dst, &log, WithHTTPClient(srv.Client())); err != nil {
		t.Fatalf("PullDisk: %v", err)
	}
	got, _ := os.ReadFile(dst)
	want := append(append([]byte{}, c1...), c2...)
	if !bytes.Equal(got, want) {
		t.Fatalf("disk mismatch: got %d bytes, want %d", len(got), len(want))
	}
	if log.Len() == 0 {
		t.Error("expected progress output")
	}
}

func TestPullDisk_Errors(t *testing.T) {
	img := buildImage(t, "cirruslabs/base", [][]byte{[]byte("disk")}, nil, nil)
	srv := img.serve(t, false)
	ref := img.ref(srv)

	t.Run("bad reference", func(t *testing.T) {
		if err := PullDisk(context.Background(), "no-registry", "x", io.Discard); err == nil {
			t.Fatal("expected reference error")
		}
	})
	t.Run("manifest error", func(t *testing.T) {
		bad := "127.0.0.1:1/org/img:latest" // nothing listening
		if err := PullDisk(context.Background(), bad, filepath.Join(t.TempDir(), "d"), io.Discard); err == nil {
			t.Fatal("expected manifest error")
		}
	})
	t.Run("create dst error", func(t *testing.T) {
		if err := PullDisk(context.Background(), ref, filepath.Join(t.TempDir(), "nodir", "d"), io.Discard,
			WithHTTPClient(srv.Client())); err == nil {
			t.Fatal("expected create error")
		}
	})
	t.Run("no disk layers", func(t *testing.T) {
		cfgOnly := buildImage(t, "cirruslabs/cfg", nil, []byte("{}"), nil)
		s2 := cfgOnly.serve(t, false)
		if err := PullDisk(context.Background(), cfgOnly.ref(s2), filepath.Join(t.TempDir(), "d"), io.Discard,
			WithHTTPClient(s2.Client())); err == nil {
			t.Fatal("expected no-disk-layers error")
		}
	})
	t.Run("fsync error", func(t *testing.T) {
		defer restoreFsync(injectFsyncError())
		if err := PullDisk(context.Background(), ref, filepath.Join(t.TempDir(), "d"), io.Discard,
			WithHTTPClient(srv.Client())); err == nil {
			t.Fatal("expected fsync error")
		}
	})
}

func TestPull_Bundle(t *testing.T) {
	disk := bytes.Repeat([]byte("D"), 2048)
	cfg := []byte(`{"os":"darwin","arch":"arm64"}`)
	nvram := []byte("nvram-bytes")
	img := buildImage(t, "cirruslabs/base", [][]byte{disk}, cfg, nvram)
	srv := img.serve(t, false)

	dir := filepath.Join(t.TempDir(), "vm")
	if err := Pull(context.Background(), img.ref(srv), dir, io.Discard, WithHTTPClient(srv.Client())); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	for name, want := range map[string][]byte{"config.json": cfg, "nvram.bin": nvram, "disk.img": disk} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s mismatch", name)
		}
	}
}

func TestPull_Errors(t *testing.T) {
	img := buildImage(t, "cirruslabs/base", [][]byte{[]byte("disk")}, []byte("{}"), nil)
	srv := img.serve(t, false)
	ref := img.ref(srv)

	t.Run("bad reference", func(t *testing.T) {
		if err := Pull(context.Background(), "bad", "x", io.Discard); err == nil {
			t.Fatal("expected reference error")
		}
	})
	t.Run("manifest error", func(t *testing.T) {
		if err := Pull(context.Background(), "127.0.0.1:1/org/img:latest", t.TempDir(), io.Discard); err == nil {
			t.Fatal("expected manifest error")
		}
	})
	t.Run("mkdir error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		_ = os.WriteFile(file, []byte("x"), 0o600)
		if err := Pull(context.Background(), ref, filepath.Join(file, "sub"), io.Discard,
			WithHTTPClient(srv.Client())); err == nil {
			t.Fatal("expected mkdir error")
		}
	})
	t.Run("config fetch error", func(t *testing.T) {
		// Make destDir/config.json un-creatable by pre-creating it as a directory.
		dir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dir, "config.json"), 0o755)
		if err := Pull(context.Background(), ref, dir, io.Discard, WithHTTPClient(srv.Client())); err == nil {
			t.Fatal("expected config fetch error")
		}
	})
	t.Run("disk create error", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dir, "disk.img"), 0o755) // block disk.img
		if err := Pull(context.Background(), ref, dir, io.Discard, WithHTTPClient(srv.Client())); err == nil {
			t.Fatal("expected disk create error")
		}
	})
	t.Run("nvram fetch error", func(t *testing.T) {
		withNV := buildImage(t, "cirruslabs/nv", [][]byte{[]byte("disk")}, nil, []byte("nv"))
		s2 := withNV.serve(t, false)
		dir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dir, "nvram.bin"), 0o755)
		if err := Pull(context.Background(), withNV.ref(s2), dir, io.Discard, WithHTTPClient(s2.Client())); err == nil {
			t.Fatal("expected nvram fetch error")
		}
	})
	t.Run("disk download error", func(t *testing.T) {
		// An image whose disk blob is absent: config fetch succeeds, then the
		// disk layer download fails inside writeDisk.
		bad := buildImage(t, "cirruslabs/nodisk", [][]byte{[]byte("disk")}, []byte("{}"), nil)
		delete(bad.blobs, bad.blobsDiskDigest())
		s2 := bad.serve(t, false)
		if err := Pull(context.Background(), bad.ref(s2), t.TempDir(), io.Discard, WithHTTPClient(s2.Client())); err == nil {
			t.Fatal("expected disk download error")
		}
	})
	t.Run("config fsync error", func(t *testing.T) {
		defer restoreFsync(injectFsyncError())
		if err := Pull(context.Background(), ref, t.TempDir(), io.Discard, WithHTTPClient(srv.Client())); err == nil {
			t.Fatal("expected config fsync error")
		}
	})
	t.Run("disk fsync error", func(t *testing.T) {
		// No config/nvram, so the disk file's fsync is the first one reached.
		diskOnly := buildImage(t, "cirruslabs/diskonly", [][]byte{[]byte("disk")}, nil, nil)
		s2 := diskOnly.serve(t, false)
		defer restoreFsync(injectFsyncError())
		if err := Pull(context.Background(), diskOnly.ref(s2), t.TempDir(), io.Discard, WithHTTPClient(s2.Client())); err == nil {
			t.Fatal("expected disk fsync error")
		}
	})
}

// injectFsyncError swaps fsyncFile for one that always fails, returning the
// original so it can be restored.
func injectFsyncError() func(*os.File) error {
	orig := fsyncFile
	fsyncFile = func(*os.File) error { return errors.New("sync failed") }
	return orig
}

func restoreFsync(orig func(*os.File) error) { fsyncFile = orig }

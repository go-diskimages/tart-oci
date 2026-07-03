package tartoci

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// digestOf returns the "sha256:<hex>" digest of b.
func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return digestPrefix + hex.EncodeToString(h[:])
}

// diskLayerFor builds a disk.v2 descriptor + its Apple LZ4 frame blob for raw.
func diskLayerFor(raw []byte) (Descriptor, []byte) {
	frame := encodeAppleFrame(raw)
	return Descriptor{
		MediaType: MediaTypeDisk,
		Digest:    digestOf(frame),
		Size:      int64(len(frame)),
		Annotations: map[string]string{
			AnnUncompressedSize:   strconv.Itoa(len(raw)),
			AnnUncompressedDigest: digestOf(raw),
		},
	}, frame
}

// mockImage is a synthetic Tart OCI image plus the blobs backing it.
type mockImage struct {
	repo         string
	tag          string
	manifestData []byte
	blobs        map[string][]byte // digest -> content
}

// buildImage assembles a manifest from disk chunks and optional config/nvram
// blobs. Any of the extra descriptors may be nil.
func buildImage(t *testing.T, repo string, chunks [][]byte, cfg, nvram []byte) *mockImage {
	t.Helper()
	img := &mockImage{repo: repo, tag: "latest", blobs: map[string][]byte{}}
	m := Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeOCIManifest,
		Config:        Descriptor{MediaType: "application/vnd.oci.image.config.v1+json"},
	}
	if cfg != nil {
		d := Descriptor{MediaType: MediaTypeConfig, Digest: digestOf(cfg), Size: int64(len(cfg))}
		m.Layers = append(m.Layers, d)
		img.blobs[d.Digest] = cfg
	}
	for _, raw := range chunks {
		d, frame := diskLayerFor(raw)
		m.Layers = append(m.Layers, d)
		img.blobs[d.Digest] = frame
	}
	if nvram != nil {
		d := Descriptor{MediaType: MediaTypeNVRAM, Digest: digestOf(nvram), Size: int64(len(nvram))}
		m.Layers = append(m.Layers, d)
		img.blobs[d.Digest] = nvram
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	img.manifestData = data
	img.blobs[digestOf(data)] = data
	return img
}

// serve starts an httptest server for img. When requireAuth is true, the
// registry endpoints demand a Bearer token obtained from /token.
func (img *mockImage) serve(t *testing.T, requireAuth bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	const token = "test-token"

	authOK := func(w http.ResponseWriter, r *http.Request) bool {
		if !requireAuth {
			return true
		}
		if r.Header.Get("Authorization") == "Bearer "+token {
			return true
		}
		w.Header().Set("WWW-Authenticate",
			`Bearer realm="`+srv.URL+`/token",service="registry",scope="repository:`+img.repo+`:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
	})
	mux.HandleFunc("/v2/"+img.repo+"/manifests/", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(w, r) {
			return
		}
		w.Header().Set("Content-Type", MediaTypeOCIManifest)
		_, _ = w.Write(img.manifestData)
	})
	mux.HandleFunc("/v2/"+img.repo+"/blobs/", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(w, r) {
			return
		}
		digest := r.URL.Path[strings.LastIndex(r.URL.Path, "/blobs/")+len("/blobs/"):]
		b, ok := img.blobs[digest]
		if !ok {
			http.Error(w, "blob not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// blobsDiskDigest returns the digest of the first disk layer in the manifest.
func (img *mockImage) blobsDiskDigest() string {
	var m Manifest
	_ = json.Unmarshal(img.manifestData, &m)
	return m.DiskLayers()[0].Digest
}

// ref builds a reference string ("host/repo:tag") targeting srv.
func (img *mockImage) ref(srv *httptest.Server) string {
	host := strings.TrimPrefix(srv.URL, "http://")
	return host + "/" + img.repo + ":" + img.tag
}

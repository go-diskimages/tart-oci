package tartoci

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func clientWith(f roundTripFunc) *http.Client { return &http.Client{Transport: f} }

// stringResp builds a 200 response carrying body.
func stringResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// errReadCloser is a body whose Read always fails.
type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("boom") }
func (errReadCloser) Close() error             { return nil }

func TestDefaultBaseURLAndLocalHost(t *testing.T) {
	if got := defaultBaseURL("ghcr.io"); got != "https://ghcr.io" {
		t.Errorf("defaultBaseURL(ghcr.io) = %q", got)
	}
	if got := defaultBaseURL("localhost:5000"); got != "http://localhost:5000" {
		t.Errorf("defaultBaseURL(localhost:5000) = %q", got)
	}
	local := []string{"localhost", "127.0.0.1", "127.0.0.1:5000", "[::1]:5000"}
	for _, h := range local {
		if !isLocalHost(h) {
			t.Errorf("isLocalHost(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"ghcr.io", "example.com:443", "[2001:db8::1]:443"} {
		if isLocalHost(h) {
			t.Errorf("isLocalHost(%q) = true, want false", h)
		}
	}
}

func TestManifestAuthFlow(t *testing.T) {
	img := buildImage(t, "cirruslabs/base", [][]byte{[]byte("hello disk")}, nil, nil)
	srv := img.serve(t, true) // requires a Bearer token
	reg := NewRegistry(mustRef(t, img.ref(srv)), WithHTTPClient(srv.Client()))
	m, err := reg.Manifest(context.Background())
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(m.DiskLayers()) != 1 {
		t.Fatalf("disk layers = %d, want 1", len(m.DiskLayers()))
	}
}

func TestManifestNon200(t *testing.T) {
	img := buildImage(t, "cirruslabs/base", [][]byte{[]byte("x")}, nil, nil)
	srv := img.serve(t, false)
	// A repository the server does not serve -> 404.
	reg := NewRegistry(Reference{Registry: strings.TrimPrefix(srv.URL, "http://"),
		Repository: "no/such", Tag: "latest"}, WithHTTPClient(srv.Client()))
	if _, err := reg.Manifest(context.Background()); err == nil {
		t.Fatal("expected non-200 error")
	}
}

func TestManifestIndexResolution(t *testing.T) {
	// Serve an image index that points at a real image manifest by digest.
	disk, frame := diskLayerFor([]byte("indexed disk"))
	m := Manifest{SchemaVersion: 2, MediaType: MediaTypeOCIManifest, Layers: []Descriptor{disk}}
	mData, _ := json.Marshal(m)
	mDigest := digestOf(mData)
	idxData, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     MediaTypeOCIIndex,
		"manifests":     []Descriptor{{MediaType: MediaTypeOCIManifest, Digest: mDigest}},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/org/img/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(idxData)
	})
	mux.HandleFunc("/v2/org/img/manifests/"+mDigest, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mData)
	})
	mux.HandleFunc("/v2/org/img/blobs/"+disk.Digest, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(frame)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	reg := NewRegistry(Reference{Registry: strings.TrimPrefix(srv.URL, "http://"),
		Repository: "org/img", Tag: "latest"}, WithHTTPClient(srv.Client()))
	got, err := reg.Manifest(context.Background())
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(got.DiskLayers()) != 1 {
		t.Fatalf("disk layers = %d, want 1", len(got.DiskLayers()))
	}
}

func TestManifestErrors(t *testing.T) {
	base := Reference{Registry: "example.com", Repository: "org/img", Tag: "latest"}

	t.Run("build request", func(t *testing.T) {
		reg := NewRegistry(base, WithBaseURL("://bad-url"))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected build-request error")
		}
	})
	t.Run("transport error", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(clientWith(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		})))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected transport error")
		}
	})
	t.Run("read body error", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(clientWith(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: errReadCloser{}, Header: make(http.Header)}, nil
		})))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected read-body error")
		}
	})
	t.Run("parse error", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(clientWith(func(*http.Request) (*http.Response, error) {
			return stringResp("{not json"), nil
		})))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected parse error")
		}
	})
	t.Run("no layers", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(clientWith(func(*http.Request) (*http.Response, error) {
			return stringResp(`{"schemaVersion":2,"layers":[]}`), nil
		})))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected no-layers error")
		}
	})
}

func TestAuthenticateErrors(t *testing.T) {
	base := Reference{Registry: "example.com", Repository: "org/img", Tag: "latest"}

	// Helper: a transport that 401s with a given challenge, then (if a token is
	// obtained) 200s the manifest. tokenBody/tokenStatus control /token.
	makeClient := func(challenge, tokenBody string, tokenStatus int, tokenErr bool) *http.Client {
		return clientWith(func(r *http.Request) (*http.Response, error) {
			if strings.Contains(r.URL.Path, "/token") || strings.Contains(r.URL.Host, "token") {
				if tokenErr {
					return nil, errors.New("token dial failed")
				}
				resp := &http.Response{StatusCode: tokenStatus, Body: io.NopCloser(strings.NewReader(tokenBody)), Header: make(http.Header)}
				return resp, nil
			}
			if r.Header.Get("Authorization") == "Bearer good" {
				return stringResp(`{"schemaVersion":2,"layers":[{"mediaType":"` + MediaTypeDisk + `","digest":"sha256:x"}]}`), nil
			}
			resp := &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}
			resp.Header.Set("WWW-Authenticate", challenge)
			return resp, nil
		})
	}
	tokRealm := `Bearer realm="http://reg.example/token",service="reg",scope="repository:org/img:pull"`

	t.Run("non-bearer challenge", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(makeClient(`Basic realm="x"`, "", 200, false)))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected unsupported-challenge error")
		}
	})
	t.Run("missing realm", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(makeClient(`Bearer service="reg"`, "", 200, false)))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected missing-realm error")
		}
	})
	t.Run("bad realm url", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(makeClient(`Bearer realm="://nope"`, "", 200, false)))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected bad-realm error")
		}
	})
	t.Run("token transport error", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(makeClient(tokRealm, "", 200, true)))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected token transport error")
		}
	})
	t.Run("token non-200", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(makeClient(tokRealm, "", 500, false)))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected token status error")
		}
	})
	t.Run("token bad json", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(makeClient(tokRealm, "{bad", 200, false)))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected token decode error")
		}
	})
	t.Run("token empty", func(t *testing.T) {
		reg := NewRegistry(base, WithHTTPClient(makeClient(tokRealm, `{"token":""}`, 200, false)))
		if _, err := reg.Manifest(context.Background()); err == nil {
			t.Fatal("expected empty-token error")
		}
	})
	t.Run("access_token fallback", func(t *testing.T) {
		reg := NewRegistry(base, WithBasicAuth("u", "p"),
			WithHTTPClient(makeClient(tokRealm, `{"access_token":"good"}`, 200, false)))
		if _, err := reg.Manifest(context.Background()); err != nil {
			t.Fatalf("expected success with access_token fallback: %v", err)
		}
	})
}

// TestGetRetryTransportError covers the branch where the post-authentication
// retry request itself fails at the transport.
func TestGetRetryTransportError(t *testing.T) {
	base := Reference{Registry: "example.com", Repository: "org/img", Tag: "latest"}
	calls := 0
	reg := NewRegistry(base, WithHTTPClient(clientWith(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/token") {
			return stringResp(`{"token":"good"}`), nil
		}
		calls++
		if calls == 1 {
			resp := &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}
			resp.Header.Set("WWW-Authenticate", `Bearer realm="http://reg/token",service="reg"`)
			return resp, nil
		}
		return nil, errors.New("retry dial failed")
	})))
	if _, err := reg.Manifest(context.Background()); err == nil {
		t.Fatal("expected retry transport error")
	}
}

func TestParseChallenge(t *testing.T) {
	got := parseChallenge(`realm="https://r/t",service="reg",scope="repository:a/b:pull,push",junk`)
	if got["realm"] != "https://r/t" || got["service"] != "reg" {
		t.Errorf("parseChallenge = %v", got)
	}
	if got["scope"] != "repository:a/b:pull,push" {
		t.Errorf("scope with comma not preserved: %q", got["scope"])
	}
	if _, ok := got["junk"]; ok {
		t.Errorf("bare token should be skipped: %v", got)
	}
}

func mustRef(t *testing.T, s string) Reference {
	t.Helper()
	r, err := ParseReference(s)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", s, err)
	}
	return r
}

package tartoci

import (
	"strings"
	"testing"
)

func TestParseReference(t *testing.T) {
	const sha = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		in   string
		want Reference
	}{
		{"ghcr.io/cirruslabs/macos-sequoia-base:latest",
			Reference{"ghcr.io", "cirruslabs/macos-sequoia-base", "latest", ""}},
		{"ghcr.io/cirruslabs/macos-sequoia-base",
			Reference{"ghcr.io", "cirruslabs/macos-sequoia-base", "latest", ""}},
		{"registry:5000/team/img:v1",
			Reference{"registry:5000", "team/img", "v1", ""}},
		{"localhost/img:tag",
			Reference{"localhost", "img", "tag", ""}},
		{"ghcr.io/org/img@" + sha,
			Reference{"ghcr.io", "org/img", "", sha}},
	}
	for _, tc := range cases {
		got, err := ParseReference(tc.in)
		if err != nil {
			t.Fatalf("ParseReference(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseReference(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestParseReferenceErrors(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"no registry host": "cirruslabs/img:latest",
		"bare name":        "img",
		"empty tag":        "ghcr.io/org/img:",
		"missing repo":     "ghcr.io/:tag",
		"bad digest len":   "ghcr.io/org/img@sha256:deadbeef",
		"bad digest algo":  "ghcr.io/org/img@md5:0123456789abcdef0123456789abcdef",
	}
	for name, in := range cases {
		if _, err := ParseReference(in); err == nil {
			t.Errorf("%s: ParseReference(%q) expected error", name, in)
		}
	}
}

func TestReferenceStringAndManifestRef(t *testing.T) {
	const sha = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tagRef := Reference{Registry: "ghcr.io", Repository: "org/img", Tag: "latest"}
	if got := tagRef.String(); got != "ghcr.io/org/img:latest" {
		t.Errorf("String() = %q", got)
	}
	if got := tagRef.manifestRef(); got != "latest" {
		t.Errorf("manifestRef() = %q", got)
	}
	digestRef := Reference{Registry: "ghcr.io", Repository: "org/img", Digest: sha}
	if got := digestRef.String(); !strings.HasSuffix(got, "@"+sha) {
		t.Errorf("String() = %q", got)
	}
	if got := digestRef.manifestRef(); got != sha {
		t.Errorf("manifestRef() = %q", got)
	}
}

package tartoci

import (
	"context"
	"io"
	"os"
	"testing"
)

// TestIntegration_RealGHCR pulls a manifest and verifies one real disk layer
// from a public Tart image on ghcr.io. It is skipped unless
// TART_OCI_NETWORK_TEST=1 is set, since it needs network access.
//
// It exercises the full anonymous-token → manifest → blob → Apple-LZ4-decode →
// digest-verify path against a live registry, downloading only the single
// smallest disk layer (a few MiB) rather than the whole multi-GB disk.
func TestIntegration_RealGHCR(t *testing.T) {
	if os.Getenv("TART_OCI_NETWORK_TEST") != "1" {
		t.Skip("set TART_OCI_NETWORK_TEST=1 to run the live ghcr.io integration test")
	}
	const ref = "ghcr.io/cirruslabs/macos-sequoia-base:latest"
	parsed, err := ParseReference(ref)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	reg := NewRegistry(parsed)
	m, err := reg.Manifest(context.Background())
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	disks := m.DiskLayers()
	if len(disks) == 0 {
		t.Fatal("no disk layers in real manifest")
	}

	// Pick the smallest disk layer to keep the download modest.
	smallest := disks[0]
	for _, d := range disks[1:] {
		if d.Size < smallest.Size {
			smallest = d
		}
	}
	t.Logf("verifying disk layer %s (%d compressed bytes)", smallest.Digest, smallest.Size)
	if err := reg.writeDiskLayer(context.Background(), smallest, io.Discard); err != nil {
		t.Fatalf("writeDiskLayer against real registry: %v", err)
	}
}

package tunnel

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestSessionCopyAllocationsDoNotScaleWithChunks(t *testing.T) {
	oneChunk := sessionCopyAllocs(1)
	manyChunks := sessionCopyAllocs(128)
	if manyChunks > oneChunk+1 {
		t.Fatalf("allocations scale with chunks: one=%.2f many=%.2f", oneChunk, manyChunks)
	}
}

func BenchmarkPhase3C3SessionCopy(b *testing.B) {
	payload := bytes.Repeat([]byte{0xa5}, 32<<10)
	closer := closeFunc(func() error { return nil })
	b.ReportAllocs()
	b.SetBytes(int64(len(payload) * 2))
	b.ResetTimer()
	for range b.N {
		session, err := NewSession(
			Endpoint{Reader: bytes.NewReader(payload), Writer: io.Discard, Closer: closer},
			Endpoint{Reader: bytes.NewReader(payload), Writer: io.Discard, Closer: closer},
			0,
		)
		if err != nil {
			b.Fatal(err)
		}
		result := session.Run(context.Background())
		if result.DownstreamToUpstream != uint64(len(payload)) || result.UpstreamToDownstream != uint64(len(payload)) {
			b.Fatalf("result=%+v", result)
		}
	}
}

func sessionCopyAllocs(chunks int) float64 {
	payload := bytes.Repeat([]byte{0x5a}, chunks*(32<<10))
	closer := closeFunc(func() error { return nil })
	return testing.AllocsPerRun(25, func() {
		session, err := NewSession(
			Endpoint{Reader: bytes.NewReader(payload), Writer: io.Discard, Closer: closer},
			Endpoint{Reader: bytes.NewReader(payload), Writer: io.Discard, Closer: closer},
			0,
		)
		if err != nil {
			panic(err)
		}
		result := session.Run(context.Background())
		if result.DownstreamToUpstream != uint64(len(payload)) || result.UpstreamToDownstream != uint64(len(payload)) {
			panic("unexpected byte count")
		}
	})
}

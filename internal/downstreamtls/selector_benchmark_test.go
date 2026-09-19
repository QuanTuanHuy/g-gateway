package downstreamtls

import (
	"crypto/tls"
	"fmt"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func BenchmarkSelectorDefault10K(b *testing.B) {
	selector := benchmarkSelector10K(b, false)
	benchmarkLookup(b, func() {
		_, _, _ = selector.Select(&tls.ClientHelloInfo{ServerName: "unknown.example.net"})
	})
}

func BenchmarkSelectorExact10K(b *testing.B) {
	selector := benchmarkSelector10K(b, false)
	benchmarkLookup(b, func() {
		_, _, _ = selector.Select(&tls.ClientHelloInfo{ServerName: "host09999.example.com"})
	})
}

func BenchmarkSelectorWildcard10K(b *testing.B) {
	selector := benchmarkSelector10K(b, true)
	benchmarkLookup(b, func() {
		_, _, _ = selector.Select(&tls.ClientHelloInfo{ServerName: "shop.zone09999.example.com"})
	})
}

func BenchmarkCompileSelector10K(b *testing.B) {
	policy, materials, now := benchmarkPolicy10K(b, false)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Compile(policy, materials, now); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkLookup(b *testing.B, lookup func()) {
	b.Helper()
	if allocations := testing.AllocsPerRun(1000, lookup); allocations != 0 {
		b.Fatalf("lookup allocations = %f, want 0", allocations)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		lookup()
	}
}

func benchmarkSelector10K(b *testing.B, wildcard bool) *Selector {
	b.Helper()
	policy, materials, now := benchmarkPolicy10K(b, wildcard)
	selector, err := Compile(policy, materials, now)
	if err != nil {
		b.Fatal(err)
	}
	return selector
}

func benchmarkPolicy10K(b *testing.B, wildcard bool) (*model.DownstreamTLSPolicy, []*tlsmaterial.Certificate, time.Time) {
	b.Helper()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	defaultCertificate := newSelectorCertificate(b, "default", 1, []string{"default.example"}, "", now.Add(-time.Hour), now.Add(time.Hour))
	materials := []*tlsmaterial.Certificate{defaultCertificate}
	bindings := make([]model.SNIBinding, 0, 100)
	for batch := 0; batch < 100; batch++ {
		certificateID := fmt.Sprintf("indexed-%03d", batch)
		hosts := make([]string, 100)
		for offset := range hosts {
			index := batch*len(hosts) + offset
			if wildcard {
				hosts[offset] = fmt.Sprintf("*.zone%05d.example.com", index)
			} else {
				hosts[offset] = fmt.Sprintf("host%05d.example.com", index)
			}
		}
		materials = append(materials, newSelectorCertificate(
			b, certificateID, int64(batch+2), hosts, "", now.Add(-time.Hour), now.Add(time.Hour),
		))
		bindings = append(bindings, model.SNIBinding{CertificateRef: certificateID, Hosts: hosts})
	}
	return &model.DownstreamTLSPolicy{
		DefaultCertificateRef: "default",
		SNIBindings:           bindings,
	}, materials, now
}

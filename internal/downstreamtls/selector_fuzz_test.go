package downstreamtls

import (
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func FuzzCompileSelector(f *testing.F) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	defaultCertificate := newSelectorCertificate(f, "default", 1, []string{"default.example"}, "", now.Add(-time.Hour), now.Add(time.Hour))
	exactCertificate := newSelectorCertificate(f, "exact", 2, []string{"api.example.com"}, "", now.Add(-time.Hour), now.Add(time.Hour))
	wildcardCertificate := newSelectorCertificate(f, "wild", 3, []string{"*.example.com"}, "", now.Add(-time.Hour), now.Add(time.Hour))
	materials := []*tlsmaterial.Certificate{defaultCertificate, exactCertificate, wildcardCertificate}

	for _, seed := range [][3]string{
		{"api.example.com", "*.example.com", "shop.example.com"},
		{"API.EXAMPLE.COM.", "*.Example.COM", "bad_name.example"},
		{"a..example", "*.*.example.com", ""},
	} {
		f.Add(seed[0], seed[1], seed[2])
	}
	f.Fuzz(func(t *testing.T, exactHost, wildcardHost, serverName string) {
		if len(exactHost)+len(wildcardHost)+len(serverName) > 2048 {
			t.Skip()
		}
		selector, err := Compile(&model.DownstreamTLSPolicy{
			DefaultCertificateRef: "default",
			SNIBindings: []model.SNIBinding{
				{CertificateRef: "exact", Hosts: []string{exactHost}},
				{CertificateRef: "wild", Hosts: []string{wildcardHost}},
			},
		}, materials, now)
		if err != nil {
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Code == "" {
				t.Fatalf("Compile() returned unstable error %T: %v", err, err)
			}
			return
		}
		certificate, selection, selectErr := selector.Select(&tls.ClientHelloInfo{ServerName: serverName})
		if selectErr != nil || certificate == nil || selection == SelectionError {
			t.Fatalf("Select() = (%v, %q, %v)", certificate, selection, selectErr)
		}
	})
}

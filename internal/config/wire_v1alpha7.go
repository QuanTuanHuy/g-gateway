package config

import "github.com/QuanTuanHuy/g-gateway/internal/model"

type documentV7 struct {
	APIVersion    string                   `yaml:"api_version"`
	Runtime       runtimeDocumentV4        `yaml:"runtime"`
	Listeners     listenersDocument        `yaml:"listeners"`
	Server        serverDocument           `yaml:"server"`
	Telemetry     telemetryDocument        `yaml:"telemetry"`
	TrustBundles  []trustBundleDocumentV5  `yaml:"trust_bundles"`
	Certificates  []certificateDocumentV5  `yaml:"certificates"`
	DownstreamTLS *downstreamTLSDocumentV7 `yaml:"downstream_tls"`
	Routes        []routeDocumentV6        `yaml:"routes"`
	Services      []serviceDocumentV6      `yaml:"services"`
	Upstreams     []upstreamDocumentV5     `yaml:"upstreams"`
}

type downstreamTLSDocumentV7 struct {
	DefaultCertificateRef string                 `yaml:"default_certificate_ref"`
	SNIBindings           []sniBindingDocumentV7 `yaml:"sni_bindings"`
}

type sniBindingDocumentV7 struct {
	CertificateRef string   `yaml:"certificate_ref"`
	Hosts          []string `yaml:"hosts"`
}

func convertV7(wire documentV7) (BootstrapConfig, model.ResourceSet, error) {
	bootstrap, resources, err := convertV6(documentV6{
		APIVersion:   apiVersionV1Alpha6,
		Runtime:      wire.Runtime,
		Listeners:    wire.Listeners,
		Server:       wire.Server,
		Telemetry:    wire.Telemetry,
		TrustBundles: wire.TrustBundles,
		Certificates: wire.Certificates,
		Routes:       wire.Routes,
		Services:     wire.Services,
		Upstreams:    wire.Upstreams,
	})
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	if wire.DownstreamTLS == nil {
		return bootstrap, resources, nil
	}
	policy := &model.DownstreamTLSPolicy{
		DefaultCertificateRef: wire.DownstreamTLS.DefaultCertificateRef,
		SNIBindings:           make([]model.SNIBinding, len(wire.DownstreamTLS.SNIBindings)),
	}
	for index := range wire.DownstreamTLS.SNIBindings {
		binding := wire.DownstreamTLS.SNIBindings[index]
		policy.SNIBindings[index] = model.SNIBinding{
			CertificateRef: binding.CertificateRef,
			Hosts:          append([]string(nil), binding.Hosts...),
		}
	}
	resources.DownstreamTLS = policy
	return bootstrap, resources, nil
}

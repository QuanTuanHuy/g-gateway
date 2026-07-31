package config

import (
	"fmt"
	"strings"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

type documentV5 struct {
	APIVersion   string                  `yaml:"api_version"`
	Runtime      runtimeDocumentV4       `yaml:"runtime"`
	Listeners    listenersDocument       `yaml:"listeners"`
	Server       serverDocument          `yaml:"server"`
	Telemetry    telemetryDocument       `yaml:"telemetry"`
	TrustBundles []trustBundleDocumentV5 `yaml:"trust_bundles"`
	Certificates []certificateDocumentV5 `yaml:"certificates"`
	Routes       []routeDocumentV4       `yaml:"routes"`
	Services     []serviceDocumentV2     `yaml:"services"`
	Upstreams    []upstreamDocumentV5    `yaml:"upstreams"`
}

type certificateDocumentV5 struct {
	ID              string `yaml:"id"`
	CertificateFile string `yaml:"certificate_file"`
	PrivateKeyFile  string `yaml:"private_key_file"`
}

type trustBundleDocumentV5 struct {
	ID     string `yaml:"id"`
	CAFile string `yaml:"ca_file"`
}

type upstreamDocumentV5 struct {
	ID        string               `yaml:"id"`
	Endpoints []endpointDocumentV3 `yaml:"endpoints"`
	Balancer  balancerDocumentV3   `yaml:"balancer"`
	Transport transportDocumentV5  `yaml:"transport"`
	Health    healthDocumentV4     `yaml:"health"`
	Retry     retryDocumentV4      `yaml:"retry"`
}

type transportDocumentV5 struct {
	Protocol                  string                  `yaml:"protocol"`
	TLS                       *transportTLSDocumentV5 `yaml:"tls"`
	DialTimeout               string                  `yaml:"dial_timeout"`
	ResponseHeaderTimeout     string                  `yaml:"response_header_timeout"`
	IdleConnectionTimeout     string                  `yaml:"idle_connection_timeout"`
	MaxIdleConnections        int                     `yaml:"max_idle_connections"`
	MaxIdleConnectionsPerHost int                     `yaml:"max_idle_connections_per_host"`
}

type transportTLSDocumentV5 struct {
	TrustBundleRef       string `yaml:"trust_bundle_ref"`
	ClientCertificateRef string `yaml:"client_certificate_ref"`
	ServerName           string `yaml:"server_name"`
}

func convertV5(wire documentV5) (BootstrapConfig, model.ResourceSet, error) {
	if err := validateMaterialDocumentsV5(wire.Certificates, wire.TrustBundles); err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	upstreams := make([]upstreamDocumentV4, len(wire.Upstreams))
	for index, upstream := range wire.Upstreams {
		upstreams[index] = upstreamDocumentV4{
			upstreamDocumentV3: upstreamDocumentV3{
				ID:        upstream.ID,
				Endpoints: upstream.Endpoints,
				Balancer:  upstream.Balancer,
				Transport: transportDocument{
					DialTimeout:               upstream.Transport.DialTimeout,
					ResponseHeaderTimeout:     upstream.Transport.ResponseHeaderTimeout,
					IdleConnectionTimeout:     upstream.Transport.IdleConnectionTimeout,
					MaxIdleConnections:        upstream.Transport.MaxIdleConnections,
					MaxIdleConnectionsPerHost: upstream.Transport.MaxIdleConnectionsPerHost,
				},
			},
			Health: upstream.Health,
			Retry:  upstream.Retry,
		}
	}
	bootstrap, resources, err := convertV4(documentV4{
		APIVersion: apiVersionV1Alpha4,
		Runtime:    wire.Runtime,
		Listeners:  wire.Listeners,
		Server:     wire.Server,
		Telemetry:  wire.Telemetry,
		Routes:     wire.Routes,
		Services:   wire.Services,
		Upstreams:  upstreams,
	})
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, err
	}
	for index := range resources.Upstreams {
		protocol := model.TransportProtocolAuto
		if wire.Upstreams[index].Transport.Protocol != "" {
			protocol = model.TransportProtocol(wire.Upstreams[index].Transport.Protocol)
		}
		resources.Upstreams[index].Transport.Protocol = protocol
		if policy := wire.Upstreams[index].Transport.TLS; policy != nil {
			resources.Upstreams[index].Transport.TLS = &model.UpstreamTLSPolicy{
				TrustBundleRef:       policy.TrustBundleRef,
				ClientCertificateRef: policy.ClientCertificateRef,
				ServerName:           policy.ServerName,
			}
		}
	}

	var sourceBytes int64
	resources.TrustBundles = make([]*tlsmaterial.TrustBundle, 0, len(wire.TrustBundles))
	for index, document := range wire.TrustBundles {
		bundle, count, loadErr := tlsmaterial.LoadTrustBundle(document.ID, document.CAFile)
		if loadErr != nil {
			return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("trust_bundles[%d]: %w", index, loadErr)
		}
		sourceBytes += count
		if sourceBytes > tlsmaterial.MaxCandidateSourceBytes {
			return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf(
				"TLS material source bytes %d exceed maximum %d",
				sourceBytes,
				tlsmaterial.MaxCandidateSourceBytes,
			)
		}
		resources.TrustBundles = append(resources.TrustBundles, bundle)
	}
	resources.Certificates = make([]*tlsmaterial.Certificate, 0, len(wire.Certificates))
	for index, document := range wire.Certificates {
		certificate, count, loadErr := tlsmaterial.LoadCertificate(
			document.ID,
			document.CertificateFile,
			document.PrivateKeyFile,
		)
		if loadErr != nil {
			return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("certificates[%d]: %w", index, loadErr)
		}
		sourceBytes += count
		if sourceBytes > tlsmaterial.MaxCandidateSourceBytes {
			return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf(
				"TLS material source bytes %d exceed maximum %d",
				sourceBytes,
				tlsmaterial.MaxCandidateSourceBytes,
			)
		}
		resources.Certificates = append(resources.Certificates, certificate)
	}
	return bootstrap, resources, nil
}

func validateMaterialDocumentsV5(
	certificates []certificateDocumentV5,
	trustBundles []trustBundleDocumentV5,
) error {
	count := len(certificates) + len(trustBundles)
	if count > tlsmaterial.MaxMaterialResources {
		return fmt.Errorf(
			"TLS material resources: got %d, maximum is %d",
			count,
			tlsmaterial.MaxMaterialResources,
		)
	}
	seen := make(map[string]string, count)
	validateID := func(field, id string) error {
		if id == "" || strings.TrimSpace(id) != id {
			return fmt.Errorf("%s: must be non-empty without surrounding whitespace", field)
		}
		if previous, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%s: duplicate TLS material id %q also used by %s", field, id, previous)
		}
		seen[id] = field
		return nil
	}
	for index, document := range trustBundles {
		field := fmt.Sprintf("trust_bundles[%d]", index)
		if err := validateID(field+".id", document.ID); err != nil {
			return err
		}
		if strings.TrimSpace(document.CAFile) == "" {
			return fmt.Errorf("%s.ca_file: path must not be empty", field)
		}
	}
	for index, document := range certificates {
		field := fmt.Sprintf("certificates[%d]", index)
		if err := validateID(field+".id", document.ID); err != nil {
			return err
		}
		if strings.TrimSpace(document.CertificateFile) == "" {
			return fmt.Errorf("%s.certificate_file: path must not be empty", field)
		}
		if strings.TrimSpace(document.PrivateKeyFile) == "" {
			return fmt.Errorf("%s.private_key_file: path must not be empty", field)
		}
	}
	return nil
}

package upstream

import (
	"fmt"
	"net/url"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

type materialIndex struct {
	certificates map[string]*tlsmaterial.Certificate
	trustBundles map[string]*tlsmaterial.TrustBundle
}

type transportProfile struct {
	scheme            string
	protocol          model.TransportProtocol
	transport         model.TransportConfig
	trustBundle       *tlsmaterial.TrustBundle
	clientCertificate *tlsmaterial.Certificate
	serverName        string
}

func newMaterialIndex(resources model.ResourceSet) (materialIndex, error) {
	index := materialIndex{
		certificates: make(map[string]*tlsmaterial.Certificate, len(resources.Certificates)),
		trustBundles: make(map[string]*tlsmaterial.TrustBundle, len(resources.TrustBundles)),
	}
	seen := make(map[string]string, len(resources.Certificates)+len(resources.TrustBundles))
	for position, certificate := range resources.Certificates {
		field := fmt.Sprintf("certificates[%d].id", position)
		if certificate == nil || certificate.ID() == "" {
			return materialIndex{}, configError("TLS_MATERIAL_ID_INVALID", field, "", fmt.Errorf("certificate ID must not be empty"))
		}
		id := certificate.ID()
		if previous, duplicate := seen[id]; duplicate {
			return materialIndex{}, configError("TLS_MATERIAL_ID_DUPLICATE", field, "", fmt.Errorf("ID %q duplicates %s", id, previous))
		}
		seen[id] = field
		index.certificates[id] = certificate
	}
	for position, bundle := range resources.TrustBundles {
		field := fmt.Sprintf("trust_bundles[%d].id", position)
		if bundle == nil || bundle.ID() == "" {
			return materialIndex{}, configError("TLS_MATERIAL_ID_INVALID", field, "", fmt.Errorf("trust-bundle ID must not be empty"))
		}
		id := bundle.ID()
		if previous, duplicate := seen[id]; duplicate {
			return materialIndex{}, configError("TLS_MATERIAL_ID_DUPLICATE", field, "", fmt.Errorf("ID %q duplicates %s", id, previous))
		}
		seen[id] = field
		index.trustBundles[id] = bundle
	}
	return index, nil
}

func compileTransportProfile(resource model.Upstream, materials materialIndex) (transportProfile, error) {
	scheme := ""
	for _, endpoint := range resource.Endpoints {
		if endpoint.Weight == 0 {
			continue
		}
		parsed, err := url.Parse(endpoint.URL)
		if err != nil {
			return transportProfile{}, configError("UPSTREAM_ENDPOINT_INVALID", "upstreams.endpoints", resource.ID, err)
		}
		if scheme == "" {
			scheme = parsed.Scheme
			continue
		}
		if scheme != parsed.Scheme {
			return transportProfile{}, configError("UPSTREAM_SCHEME_MIXED", "upstreams.endpoints", resource.ID, nil)
		}
	}
	if scheme == "" {
		return transportProfile{}, configError("UPSTREAM_NO_ACTIVE_ENDPOINT", "upstreams.endpoints", resource.ID, nil)
	}

	profile := transportProfile{
		scheme:    scheme,
		protocol:  resource.Transport.Protocol,
		transport: resource.Transport,
	}
	if resource.Transport.TLS == nil {
		return profile, nil
	}
	policy := resource.Transport.TLS
	profile.serverName = policy.ServerName
	if policy.TrustBundleRef != "" {
		bundle := materials.trustBundles[policy.TrustBundleRef]
		if bundle == nil {
			return transportProfile{}, configError(
				"TLS_MATERIAL_REF_NOT_FOUND",
				"upstreams.transport.tls.trust_bundle_ref",
				resource.ID,
				fmt.Errorf("trust bundle %q does not resolve", policy.TrustBundleRef),
			)
		}
		profile.trustBundle = bundle
	}
	if policy.ClientCertificateRef != "" {
		certificate := materials.certificates[policy.ClientCertificateRef]
		if certificate == nil {
			return transportProfile{}, configError(
				"TLS_MATERIAL_REF_NOT_FOUND",
				"upstreams.transport.tls.client_certificate_ref",
				resource.ID,
				fmt.Errorf("certificate %q does not resolve", policy.ClientCertificateRef),
			)
		}
		profile.clientCertificate = certificate
	}
	return profile, nil
}

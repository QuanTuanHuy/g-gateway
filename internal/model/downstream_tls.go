package model

// DownstreamTLSPolicy selects the default certificate and explicit SNI
// bindings for one runtime revision.
type DownstreamTLSPolicy struct {
	DefaultCertificateRef string
	SNIBindings           []SNIBinding
}

// SNIBinding maps explicit exact or wildcard DNS hosts to one certificate
// resource.
type SNIBinding struct {
	CertificateRef string
	Hosts          []string
}

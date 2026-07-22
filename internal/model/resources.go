package model

import "time"

type ResourceSet struct {
	Routes    []Route
	Upstreams []Upstream
}

type Route struct {
	ID          string
	Match       RouteMatch
	UpstreamRef string
}

type RouteMatch struct {
	Path    string
	Methods []string
}

type Upstream struct {
	ID        string
	Endpoints []string
	Transport TransportConfig
}

type TransportConfig struct {
	DialTimeout               time.Duration
	ResponseHeaderTimeout     time.Duration
	IdleConnectionTimeout     time.Duration
	MaxIdleConnections        int
	MaxIdleConnectionsPerHost int
}

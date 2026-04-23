package protocol

import "time"

type Version struct {
	Major int
	Minor int
}

type ServiceRecord struct {
	ProtocolVersion string
	PeerID          string
	TenantID        string
	ServiceName     string
	LocalTarget     string
	Features        []string
}

type Lease struct {
	LeaseID            string
	TTL                time.Duration
	HeartbeatInterval  time.Duration
	AllowedHostnames   []string
	AllowedPathPrefixes []string
}

type OpenRequest struct {
	RequestID      string
	CorrelationID  string
	Method         string
	Authority      string
	Path           string
	Query          string
	Headers        map[string]string
	ContentLength  int64
	BodyMode       string
}

type ResponseStart struct {
	RequestID   string
	StatusCode  int
	Headers     map[string]string
}

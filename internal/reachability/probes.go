package reachability

import "context"

type Probe interface {
	Probe(context.Context) error
}

type ProbeFunc func(context.Context) error

func (f ProbeFunc) Probe(ctx context.Context) error {
	return f(ctx)
}

type FailureKind string

type SuccessKind string

const (
	FailureKindUnknown FailureKind = "unknown"
	FailureKindProbe   FailureKind = "probe"
	FailureKindNetwork FailureKind = "network"
	FailureKindRelay   FailureKind = "relay"
	FailureKindGrant   FailureKind = "grant"
)

const (
	SuccessKindUnknown SuccessKind = "unknown"
	SuccessKindProbe   SuccessKind = "probe"
	SuccessKindNetwork SuccessKind = "network"
	SuccessKindRelay   SuccessKind = "relay"
	SuccessKindGrant   SuccessKind = "grant"
)

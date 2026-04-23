package discovery

type Resolver interface {
	Resolve(serviceName string) (peerID string, err error)
}

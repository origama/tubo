package routing

type Route struct {
	Hostname    string
	PathPrefix  string
	TenantID    string
	ServiceName string
	PeerID      string
}

func Match(hostname, path string, routes []Route) (Route, bool) {
	for _, r := range routes {
		if r.Hostname == hostname && len(path) >= len(r.PathPrefix) && path[:len(r.PathPrefix)] == r.PathPrefix {
			return r, true
		}
	}
	return Route{}, false
}

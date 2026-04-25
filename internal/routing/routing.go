package routing

type Route struct {
	Hostname    string
	PathPrefix  string
	TenantID    string
	ServiceName string
	PeerID      string
}

func Match(hostname, path string, routes []Route) (Route, bool) {
	var best Route
	bestLen := 0
	found := false

	for _, r := range routes {
		if r.Hostname == hostname && len(path) >= len(r.PathPrefix) && path[:len(r.PathPrefix)] == r.PathPrefix {
			if !found || len(r.PathPrefix) > bestLen {
				best = r
				bestLen = len(r.PathPrefix)
				found = true
			}
		}
	}

	return best, found
}

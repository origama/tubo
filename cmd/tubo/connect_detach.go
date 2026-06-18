package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	connectflow "github.com/origama/tubo/internal/connectflow"
)

type connectCLIRequest struct {
	ServiceRef string
	Local      string
	ConfigPath string
	Timeout    time.Duration
	JSONOut    bool
	CachedOnly bool
	Live       bool
	Token      string
	Cluster    string
	Namespace  string
}

func connectUsageError() error {
	return errors.New("usage: tubo connect [--token <share-invite>] <service-name> [--local host:port] [flags]")
}

func parseConnectCLIArgs(args []string) (connectCLIRequest, error) {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	local := fs.String("local", "", "")
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	timeout := fs.Duration("timeout", defaultDiscoveryTimeout, "")
	jsonOut := fs.Bool("json", false, "")
	cachedOnly := fs.Bool("cached-only", false, "")
	live := fs.Bool("live", false, "")
	token := fs.String("token", "", "")
	cluster := fs.String("cluster", "", "")
	namespace := fs.String("namespace", "", "")
	namespaceShort := fs.String("n", "", "")
	serviceRef := ""
	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		serviceRef = args[0]
		parseArgs = args[1:]
	}
	if err := fs.Parse(parseArgs); err != nil {
		return connectCLIRequest{}, err
	}
	if *namespace == "" {
		*namespace = *namespaceShort
	}
	if serviceRef == "" {
		switch fs.NArg() {
		case 0:
			if strings.TrimSpace(*token) == "" {
				return connectCLIRequest{}, connectUsageError()
			}
		case 1:
			serviceRef = fs.Arg(0)
		default:
			return connectCLIRequest{}, connectUsageError()
		}
	} else if fs.NArg() != 0 {
		return connectCLIRequest{}, connectUsageError()
	}
	return connectCLIRequest{
		ServiceRef: serviceRef,
		Local:      *local,
		ConfigPath: *configPath,
		Timeout:    *timeout,
		JSONOut:    *jsonOut,
		CachedOnly: *cachedOnly,
		Live:       *live,
		Token:      *token,
		Cluster:    *cluster,
		Namespace:  *namespace,
	}, nil
}

var startDetachedProcessWithTimeoutFn = startDetachedProcessWithTimeout

func detachConnectCommand(args []string, loggingOpts globalCLIOptions) error {
	req, err := parseConnectCLIArgs(args)
	if err != nil {
		return err
	}
	childArgs := append(connectLoggingArgs(loggingOpts), args...)
	spec, err := buildDetachedConnectSpec(req, childArgs)
	if err != nil {
		return err
	}
	previousPipe, pipeExisted, persisted, err := persistPipeDefinitionFromConnect(req.ConfigPath, req, spec.State)
	if err != nil {
		return err
	}
	state, err := startDetachedProcessWithTimeoutFn(spec, 5*time.Second)
	if err != nil {
		_ = restorePipeDefinition(req.ConfigPath, persisted.Cluster, persisted.Namespace, persisted.Name, previousPipe, pipeExisted)
		return err
	}
	if req.JSONOut {
		return printJSON(state)
	}
	printDetachedSummary("connect", state)
	return nil
}

func connectProcessState(req connectCLIRequest, result connectflow.Result, localAddr, resourceKind string) detachedProcessState {
	localAddr = normalizeConnectProcessLocal(localAddr)
	scopeCluster := strings.TrimSpace(req.Cluster)
	scopeNamespace := strings.TrimSpace(req.Namespace)
	if result.Scope != nil {
		if strings.TrimSpace(result.Scope.Cluster) != "" {
			scopeCluster = result.Scope.Cluster
		}
		if strings.TrimSpace(result.Scope.Namespace) != "" {
			scopeNamespace = result.Scope.Namespace
		}
	}
	name := detachedConnectProcessName(result.ServiceName, localAddr)
	statusURL := ""
	statsURL := ""
	if strings.EqualFold(strings.TrimSpace(result.ServiceKind), "tcp") {
		if adminHostPort, ok := connectAdminHostPort(localAddr); ok {
			statusURL = "http://" + adminHostPort + "/healthz"
			statsURL = "http://" + adminHostPort + "/statsz"
		}
	} else {
		statusURL = "http://" + connectStatusHostPort(localAddr) + "/healthz"
		statsURL = "http://" + connectStatusHostPort(localAddr) + "/statsz"
	}
	return detachedProcessState{
		ID:           "process/" + name,
		Kind:         "process",
		ResourceKind: resourceKind,
		Command:      "connect",
		Name:         name,
		Service:      result.ServiceName,
		ServiceKind:  result.ServiceKind,
		ServiceID:    result.ServiceID,
		PeerID:       result.ServicePeerID,
		Cluster:      scopeCluster,
		Namespace:    scopeNamespace,
		Local:        localAddr,
		Target:       connectDetachedTarget(req, result.ServiceName, result.ServiceID),
		Path:         result.Path,
		SelectedAddr: result.SelectedAddr,
		SelectedPath: result.Path,
		StatusURL:    statusURL,
		StatsURL:     statsURL,
	}
}

func buildDetachedConnectSpec(req connectCLIRequest, childArgs []string) (detachedSpec, error) {
	serviceName, serviceID, shareScope, err := connectServiceShareSetup(req.ServiceRef, req.Token, req.Cluster, req.Namespace)
	if err != nil {
		return detachedSpec{}, err
	}
	displayService := strings.TrimSpace(serviceName)
	if displayService == "" {
		displayService = strings.TrimSpace(req.ServiceRef)
	}
	if displayService != "" {
		parsed, err := parseServiceRef(displayService)
		if err == nil {
			displayService = parsed
		}
	}
	if serviceID == "" && isServiceID(displayService) {
		serviceID = displayService
	}
	if displayService == "" {
		displayService = serviceID
	}
	if displayService == "" {
		displayService = "invite"
		if strings.TrimSpace(req.Token) == "" {
			displayService = "service"
		}
	}
	localAddr := strings.TrimSpace(req.Local)
	if localAddr == "" {
		localAddr, _, err = chooseConnectLocal("")
		if err != nil {
			return detachedSpec{}, err
		}
		childArgs = append(append([]string(nil), childArgs...), "--local", localAddr)
	} else {
		childArgs = append([]string(nil), childArgs...)
	}
	name := detachedConnectProcessName(displayService, localAddr)
	statePath := filepath.Join(processStateDir(), name+".json")
	logPath := filepath.Join(processLogDir(), name+".log")
	pidPath := filepath.Join(processRunDir(), name+".pid")
	resolved := connectflow.Result{ServiceName: displayService, ServiceID: serviceID}
	if strings.TrimSpace(req.Token) == "" {
		if preflight, resolveErr := connectflow.Resolve(context.Background(), newConnectWorkflow(), connectflow.Request{ConfigPath: req.ConfigPath, ServiceRef: req.ServiceRef, Token: req.Token, Cluster: req.Cluster, Namespace: req.Namespace, Local: localAddr, Timeout: req.Timeout, CachedOnly: req.CachedOnly, Live: req.Live}); resolveErr == nil {
			resolved = preflight
		}
	} else if tokenInfo, parseErr := parseAndVerifyServiceShareToken(req.Token); parseErr == nil {
		resolved.ServiceKind = tokenInfo.ServiceKind
	}
	if resolved.ServiceName == "" {
		resolved.ServiceName = displayService
	}
	if strings.TrimSpace(req.Token) != "" {
		if tokenInfo, parseErr := parseAndVerifyServiceShareToken(req.Token); parseErr == nil {
			resolved.ServiceKind = tokenInfo.ServiceKind
		}
	}
	if resolved.ServiceName == "" {
		resolved.ServiceName = displayService
	}
	if resolved.ServiceID == "" {
		resolved.ServiceID = serviceID
	}
	state := connectProcessState(req, resolved, localAddr, "pipe")
	if state.Cluster == "" {
		state.Cluster = strings.TrimSpace(shareScope.Cluster)
	}
	if state.Namespace == "" {
		state.Namespace = strings.TrimSpace(shareScope.Namespace)
	}
	state.LogFile = logPath
	state.StateFile = statePath
	state.PIDFile = pidPath
	return detachedSpec{
		State:     state,
		ChildArgs: append([]string{"connect"}, childArgs...),
		HealthURL: state.StatusURL,
	}, nil
}

func detachedConnectProcessName(service, local string) string {
	name := "connect-" + sanitizeProcessName(service)
	local = normalizeConnectProcessLocal(local)
	if _, port, err := net.SplitHostPort(local); err == nil && strings.TrimSpace(port) != "" {
		return name + "-" + sanitizeProcessName(port)
	}
	local = sanitizeProcessName(local)
	if local == "" {
		return name
	}
	return name + "-" + local
}

func connectStatusHostPort(local string) string {
	if strings.HasPrefix(local, ":") {
		return "127.0.0.1" + local
	}
	return local
}

func connectAdminHostPort(local string) (string, bool) {
	local = connectStatusHostPort(local)
	host, port, err := net.SplitHostPort(local)
	if err != nil {
		return "", false
	}
	p, err := strconv.Atoi(port)
	if err != nil || p >= 65535 {
		return "", false
	}
	return net.JoinHostPort(host, strconv.Itoa(p+1)), true
}

func normalizeConnectProcessLocal(localURL string) string {
	localURL = strings.TrimSpace(localURL)
	for _, prefix := range []string{"tcp://", "http://", "https://"} {
		if strings.HasPrefix(localURL, prefix) {
			localURL = strings.TrimPrefix(localURL, prefix)
			break
		}
	}
	return localURL
}

func connectDetachedTarget(req connectCLIRequest, displayService, serviceID string) string {
	if displayService != "" {
		return displayService
	}
	if serviceID != "" {
		return serviceID
	}
	if strings.TrimSpace(req.Token) != "" {
		return "share-invite"
	}
	return strings.TrimSpace(req.ServiceRef)
}

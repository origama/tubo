package main

import (
	"errors"
	"flag"
	"net"
	"path/filepath"
	"strings"
	"time"
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

func detachConnectCommand(args []string) error {
	req, err := parseConnectCLIArgs(args)
	if err != nil {
		return err
	}
	spec, err := buildDetachedConnectSpec(req, args)
	if err != nil {
		return err
	}
	state, err := startDetachedProcessWithTimeout(spec, 5*time.Second)
	if err != nil {
		return err
	}
	if req.JSONOut {
		return printJSON(state)
	}
	printDetachedSummary("connect", state)
	return nil
}

func buildDetachedConnectSpec(req connectCLIRequest, childArgs []string) (detachedSpec, error) {
	cfg, err := loadLocalConfigOrError(req.ConfigPath)
	if err != nil {
		return detachedSpec{}, err
	}
	serviceName, serviceID, scope, err := connectServiceShareSetup(req.ServiceRef, req.Token, req.Cluster, req.Namespace)
	if err != nil {
		return detachedSpec{}, err
	}
	if strings.TrimSpace(req.Token) == "" {
		scope, err = resolveServiceScope(cfg, req.Cluster, req.Namespace, false)
		if err != nil {
			return detachedSpec{}, err
		}
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
	statusURL := "http://" + connectStatusHostPort(localAddr) + "/healthz"
	return detachedSpec{
		State: detachedProcessState{
			ID:           "process/" + name,
			Kind:         "process",
			ResourceKind: "pipe",
			Command:      "connect",
			Name:         name,
			Service:      displayService,
			ServiceID:    serviceID,
			Cluster:      scope.Cluster,
			Namespace:    scope.Namespace,
			Local:        localAddr,
			Target:       connectDetachedTarget(req, displayService, serviceID),
			LogFile:      logPath,
			StateFile:    statePath,
			PIDFile:      pidPath,
			StatusURL:    statusURL,
		},
		ChildArgs: append([]string{"connect"}, childArgs...),
		HealthURL: statusURL,
	}, nil
}

func detachedConnectProcessName(service, local string) string {
	name := "connect-" + sanitizeProcessName(service)
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

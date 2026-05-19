package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

func revokeCmd(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: tubo revoke <invite|session|service-access|publish> <id-or-service> [--config <config.yaml>] [--revocations <path>] [--reason <text>]")
	}
	kind := args[0]
	target := strings.TrimSpace(args[1])
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	revocationsPath := fs.String("revocations", grantspkg.DefaultRevocationStorePath(), "")
	reason := fs.String("reason", "", "")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	store := grantspkg.NewRevocationStore(*revocationsPath)
	switch kind {
	case "invite":
		inviteID := target
		if payload, err := parseAndVerifyServiceShareToken(target); err == nil {
			inviteID = payload.JTI
		}
		rec, err := store.RevokeInvite(inviteID, *reason)
		if err != nil {
			return err
		}
		fmt.Printf("revoked invite %s\n", rec.ID)
		return nil
	case "session":
		rec, err := store.RevokeSession(target, *reason)
		if err != nil {
			return err
		}
		fmt.Printf("revoked session %s\n", rec.ID)
		return nil
	case "service-access":
		serviceID := resolveRevocationServiceID(*configPath, target)
		epoch, err := store.RevokeServiceAccess(serviceID, *reason)
		if err != nil {
			return err
		}
		fmt.Printf("revoked service access %s access_epoch=%d\n", serviceID, epoch)
		return nil
	case "publish":
		serviceID := resolveRevocationServiceID(*configPath, target)
		epoch, err := store.RevokePublish(serviceID, *reason)
		if err != nil {
			return err
		}
		fmt.Printf("revoked publish %s publish_epoch=%d\n", serviceID, epoch)
		return nil
	default:
		return fmt.Errorf("unsupported revoke target %q", kind)
	}
}

func resolveRevocationServiceID(configPath, raw string) string {
	trimmed := strings.TrimSpace(raw)
	name := strings.TrimPrefix(trimmed, "service/")
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return name
		}
		return name
	}
	cluster, ok := cfg.Clusters[cfg.CurrentCluster]
	if !ok {
		return name
	}
	ns, ok := cluster.Namespaces[cfg.CurrentNamespace]
	if !ok {
		return name
	}
	if svc, ok := ns.Services[name]; ok && svc.ServiceID != "" {
		return svc.ServiceID
	}
	return name
}

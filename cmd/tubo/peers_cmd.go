package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/origama/tubo/internal/peers"
)

func peersCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo peers <alias>")
	}
	switch args[0] {
	case "alias":
		return peersAliasCmd(args[1:])
	default:
		return fmt.Errorf("unknown peers command %q", args[0])
	}
}

func peersAliasCmd(args []string) error {
	peerID, flagArgs := splitGrantIDArg(args)
	fs := flag.NewFlagSet("peers alias", flag.ContinueOnError)
	storePath := fs.String("store", peers.DefaultStorePath(), "")
	name := fs.String("name", "", "")
	note := fs.String("note", "", "")
	jsonOut := fs.Bool("json", false, "")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if peerID == "" {
		return errors.New("usage: tubo peers alias <peer-id> --name <label>")
	}
	alias, err := peers.NewStore(*storePath).Upsert(peerID, *name, *note)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(alias)
	}
	if strings.TrimSpace(alias.Note) != "" {
		fmt.Printf("saved alias %q for %s (%s)\n", alias.Name, alias.PeerID, alias.Note)
		return nil
	}
	fmt.Printf("saved alias %q for %s\n", alias.Name, alias.PeerID)
	return nil
}

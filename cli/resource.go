package main

import (
	"fmt"
	"log"

	"github.com/flynnbase/flynn/Godeps/_workspace/src/github.com/flynn/go-docopt"
	"github.com/flynnbase/flynn/controller/client"
	ct "github.com/flynnbase/flynn/controller/types"
)

func init() {
	register("resource", runResource, `
usage: flynn resource add <provider>

Manage resources for the app.

Commands:
	add  provisions a new resource for the app using <provider>.
`)
}

func runResource(args *docopt.Args, client *controller.Client) error {
	if args.Bool["add"] {
		return runResourceAdd(args, client)
	}
	return fmt.Errorf("Top-level command not implemented.")
}

func runResourceAdd(args *docopt.Args, client *controller.Client) error {
	provider := args.String["<provider>"]

	res, err := client.ProvisionResource(&ct.ResourceReq{ProviderID: provider, Apps: []string{mustApp()}})
	if err != nil {
		return err
	}

	env := make(map[string]*string)
	for k, v := range res.Env {
		s := v
		env[k] = &s
	}

	releaseID, err := setEnv(client, "", env)
	if err != nil {
		return err
	}

	log.Printf("Created resource %s and release %s.", res.ID, releaseID)

	return nil
}

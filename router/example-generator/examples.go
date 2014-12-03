package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	rc "github.com/flynn/flynn/router/client"
	rt "github.com/flynn/flynn/router/types"
)

type generator struct {
	client *rc.HTTPClient
	route  *rt.Route
}

type example struct {
	name string
	f    func()
}

func main() {
	log.SetOutput(os.Stderr)

	c, err := rc.New()
	if err != nil {
		log.Fatal(err)
	}
	if v, ok := c.(*rc.HTTPClient); ok {
		client = v
	} else {
		log.Fatal(fmt.Errorf("Failed to initialize router client: %T is not a %T", c, &rc.HTTPClient{}))
	}
	client.HTTP.Transport = &roundTripRecorder{roundTripper: &http.Transport{Dial: client.Dial}}

	e := &generator{
		client: client,
	}

	examples := []example{
		{"route_create", e.createRoute},
		{"route_set", e.setRoute},
		{"route_list", e.listRoutes},
		{"route_get", e.getRoute},
		{"route_delete", e.deleteRoute},
	}

	res := make(map[string]*compiledRequest)
	for _, ex := range examples {
		ex.f()
		res[ex.name] = compileRequest(getRequests()[0])
	}

	var out io.Writer
	if len(os.Args) > 1 {
		out, err = os.Create(os.Args[1])
		if err != nil {
			log.Fatal(err)
		}
	} else {
		out = os.Stdout
	}
	data, err := json.MarshalIndent(res, "", "\t")
	if err != nil {
		log.Fatal(err)
	}
	_, err = out.Write(data)
	if err != nil {
		log.Fatal(err)
	}
}

func (e *generator) createRoute() {
	route := (&rt.HTTPRoute{
		Domain:  "http://example.com",
		Service: "foo" + "-web",
	}).ToRoute()
	err := e.client.CreateRoute(route)
	if err == nil {
		e.route = route
	}
}

func (e *generator) setRoute() {
	route := (&rt.HTTPRoute{
		Domain:  "http://example.org",
		Service: "bar" + "-web",
	}).ToRoute()
	e.client.SetRoute(route)
}

func (e *generator) listRoutes() {
	e.client.ListRoutes("")
}

func (e *generator) getRoute() {
	e.client.GetRoute(e.route.ID)
}

func (e *generator) deleteRoute() {
	e.client.DeleteRoute(e.route.ID)
}

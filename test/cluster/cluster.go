package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/flynnbase/flynn/cli/config"
	"github.com/flynnbase/flynn/discoverd/client"
	"github.com/flynnbase/flynn/pkg/random"
)

type BootConfig struct {
	User     string
	Kernel   string
	Network  string
	NatIface string
	Backend  string
}

type Cluster struct {
	ID            string        `json:"id"`
	Instances     instances     `json:"instances"`
	BackoffPeriod time.Duration `json:"backoff_period"`
	ClusterDomain string        `json:"cluster_domain"`
	ControllerPin string        `json:"controller_pin"`
	ControllerKey string        `json:"controller_key"`
	RouterIP      string        `json:"router_ip"`

	bc     BootConfig
	vm     *VMManager
	out    io.Writer
	bridge *Bridge
	rootFS string
}

func (c *Cluster) ControllerDomain() string {
	return "controller." + c.ClusterDomain
}

type instances []*Instance

func (i instances) Get(id string) (*Instance, error) {
	for _, inst := range i {
		if inst.ID == id {
			return inst, nil
		}
	}
	return nil, fmt.Errorf("no such host: %s", id)
}

type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func New(bc BootConfig, out io.Writer) *Cluster {
	return &Cluster{
		ID:  random.String(8),
		bc:  bc,
		out: out,
	}
}

func BuildFlynn(bc BootConfig, rootFS, commit string, merge bool, out io.Writer) (string, error) {
	c := New(bc, out)
	defer c.Shutdown()
	return c.BuildFlynn(rootFS, commit, merge)
}

func (c *Cluster) log(a ...interface{}) (int, error) {
	return fmt.Fprintln(c.out, a...)
}

func (c *Cluster) logf(f string, a ...interface{}) (int, error) {
	return fmt.Fprintf(c.out, f, a...)
}

func (c *Cluster) BuildFlynn(rootFS, commit string, merge bool) (string, error) {
	c.log("Building Flynn...")

	if err := c.setup(); err != nil {
		return "", err
	}

	uid, gid, err := lookupUser(c.bc.User)
	if err != nil {
		return "", err
	}

	build, err := c.vm.NewInstance(&VMConfig{
		Kernel: c.bc.Kernel,
		User:   uid,
		Group:  gid,
		Memory: "2048",
		Cores:  8,
		Drives: map[string]*VMDrive{
			"hda": {FS: rootFS, COW: true, Temp: false},
		},
	})
	if err != nil {
		return "", err
	}
	c.log("Booting build instance...")
	if err := build.Start(); err != nil {
		return "", fmt.Errorf("error starting build instance: %s", err)
	}

	c.log("Waiting for instance to boot...")
	if err := buildFlynn(build, commit, merge, c.out); err != nil {
		build.Kill()
		return "", fmt.Errorf("error running build script: %s", err)
	}

	if err := build.Shutdown(); err != nil {
		return "", fmt.Errorf("error while stopping build instance: %s", err)
	}
	return build.Drive("hda").FS, nil
}

func (c *Cluster) Boot(rootFS string, count int) error {
	if err := c.setup(); err != nil {
		return err
	}

	c.log("Booting", count, "VMs")
	if err := c.startVMs(rootFS, count); err != nil {
		c.Shutdown()
		return err
	}

	c.log("Bootstrapping layer 1...")
	if err := c.bootstrapLayer1(); err != nil {
		c.Shutdown()
		return err
	}
	c.rootFS = rootFS
	return nil
}

func (c *Cluster) BridgeIP() string {
	if c.bridge == nil {
		return ""
	}
	return c.bridge.IP()
}

func (c *Cluster) AddHost() error {
	if c.rootFS == "" {
		return errors.New("cluster not yet booted")
	}
	c.log("Booting 1 VM")
	return c.startVMs(c.rootFS, 1)
}

func (c *Cluster) RemoveHost(id string) error {
	inst, err := c.Instances.Get(id)
	if err != nil {
		return err
	}
	c.log("shutting down instance", id)

	var cmd string
	switch c.bc.Backend {
	case "libvirt-lxc":
		cmd = "sudo start-stop-daemon --stop --pidfile /var/run/flynn-host.pid --retry 15"
	case "docker":
		cmd = "docker stop -t 15 flynn-host"
	}
	if err := inst.Run(cmd, nil); err != nil {
		fmt.Errorf("failed to stop flynn-host on %s", id)
	}

	return inst.Shutdown()
}

func (c *Cluster) Size() int {
	return len(c.Instances)
}

func (c *Cluster) startVMs(rootFS string, count int) error {
	tmpl, ok := flynnHostScripts[c.bc.Backend]
	if !ok {
		return fmt.Errorf("unknown host backend: %s", c.bc.Backend)
	}

	uid, gid, err := lookupUser(c.bc.User)
	if err != nil {
		return err
	}

	for i := 0; i < count; i++ {
		inst, err := c.vm.NewInstance(&VMConfig{
			Kernel: c.bc.Kernel,
			User:   uid,
			Group:  gid,
			Memory: "1024",
			Cores:  2,
			Drives: map[string]*VMDrive{
				"hda": {FS: rootFS, COW: true, Temp: true},
			},
		})
		if err != nil {
			return fmt.Errorf("error creating instance %d: %s", i, err)
		}
		if err = inst.Start(); err != nil {
			return fmt.Errorf("error starting instance %d: %s", i, err)
		}
		c.Instances = append(c.Instances, inst)

		var script bytes.Buffer
		data := hostScriptData{
			ID: inst.ID,
			IP: inst.IP,
		}
		if len(c.Instances) > 1 {
			data.Peers = fmt.Sprintf("%s:7001", c.Instances[0].IP)
		}
		tmpl.Execute(&script, data)

		c.logf("Starting flynn-host on %s [id: %s]\n", inst.IP, inst.ID)
		if err := inst.Run("bash", &Streams{Stdin: &script, Stdout: c.out, Stderr: os.Stderr}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cluster) setup() error {
	if _, err := os.Stat(c.bc.Kernel); os.IsNotExist(err) {
		return fmt.Errorf("cluster: not a kernel file: %s", c.bc.Kernel)
	}
	if c.bridge == nil {
		var err error
		name := "flynnbr." + random.String(5)
		c.logf("creating network bridge %s\n", name)
		c.bridge, err = createBridge(name, c.bc.Network, c.bc.NatIface)
		if err != nil {
			return fmt.Errorf("could not create network bridge: %s", err)
		}
	}
	c.vm = NewVMManager(c.bridge)
	return nil
}

func (c *Cluster) Run(command string, s *Streams) error {
	if len(c.Instances) == 0 {
		return errors.New("no booted servers in cluster")
	}
	return c.Instances[0].Run(command, s)
}

func (c *Cluster) CLIConfig() (*config.Config, error) {
	conf := &config.Config{}
	s := &config.Cluster{
		Name:    "default",
		URL:     "https://" + c.ControllerDomain(),
		Key:     c.ControllerKey,
		GitHost: c.ClusterDomain + ":2222",
		TLSPin:  c.ControllerPin,
	}
	if err := conf.Add(s); err != nil {
		return nil, err
	}
	return conf, nil
}

func (c *Cluster) Shutdown() {
	for i, inst := range c.Instances {
		c.logf("killing instance %d [id: %s]\n", i, inst.ID)
		if err := inst.Kill(); err != nil {
			c.logf("error killing instance %d: %s\n", i, err)
		}
	}
	if c.bridge != nil {
		c.logf("deleting network bridge %s\n", c.bridge.name)
		if err := deleteBridge(c.bridge); err != nil {
			c.logf("error deleting network bridge %s: %s\n", c.bridge.name, err)
		}
		c.bridge = nil
	}
}

var flynnBuildScript = template.Must(template.New("flynn-build").Parse(`
#!/bin/bash
set -e -x

export GOPATH=~/go
flynn=$GOPATH/src/github.com/flynnbase/flynn

if [ ! -d $flynn ]; then
  git clone https://github.com/flynnbase/flynn $flynn
fi

cd $flynn

# Also fetch Github PR commits
if ! git config --get-all remote.origin.fetch | grep -q '^+refs/pull'; then
  git config --add remote.origin.fetch '+refs/pull/*/head:refs/remotes/origin/pr/*'
fi

git fetch
git checkout --quiet {{ .Commit }}

{{ if .Merge }}
git config user.email "ci@flynn.io"
git config user.name "CI"
git merge origin/master
{{ end }}

make dev

sudo cp host/bin/flynn-* /usr/bin
sudo cp host/bin/manifest.json /etc/flynn-host.json
sudo cp bootstrap/bin/manifest.json /etc/flynn-bootstrap.json
`[1:]))

type buildData struct {
	Commit string
	Merge  bool
}

func buildFlynn(inst *Instance, commit string, merge bool, out io.Writer) error {
	var b bytes.Buffer
	flynnBuildScript.Execute(&b, buildData{commit, merge})
	return inst.Run("bash", &Streams{Stdin: &b, Stdout: out, Stderr: out})
}

type hostScriptData struct {
	ID    string
	IP    string
	Peers string
}

var flynnHostScripts = map[string]*template.Template{
	"libvirt-lxc": template.Must(template.New("flynn-host-libvirt").Parse(`
sudo start-stop-daemon \
  --start \
  --background \
  --no-close \
  --make-pidfile \
  --pidfile /var/run/flynn-host.pid \
  --exec /usr/bin/env \
  -- \
  ETCD_PEERS={{ .Peers }} \
  flynn-host \
  daemon \
  --id {{ .ID }} \
  --manifest /etc/flynn-host.json \
  --external {{ .IP }} \
  --force \
  --backend libvirt-lxc \
  &>/tmp/flynn-host.log
`[1:])),
}

type bootstrapMsg struct {
	Id    string          `json:"id"`
	State string          `json:"state"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

type controllerCert struct {
	Pin string `json:"pin"`
}

func (c *Cluster) bootstrapLayer1() error {
	inst := c.Instances[0]
	c.ClusterDomain = fmt.Sprintf("flynn-%s.local", random.String(16))
	c.ControllerKey = random.String(16)
	c.BackoffPeriod = 5 * time.Second
	rd, wr := io.Pipe()
	var cmdErr error
	go func() {
		command := fmt.Sprintf(
			"DISCOVERD=%s:1111 CLUSTER_DOMAIN=%s CONTROLLER_KEY=%s BACKOFF_PERIOD=%fs flynn-host bootstrap --json --min-hosts=%d /etc/flynn-bootstrap.json",
			inst.IP, c.ClusterDomain, c.ControllerKey, c.BackoffPeriod.Seconds(), len(c.Instances),
		)
		cmdErr = inst.Run(command, &Streams{Stdout: wr, Stderr: os.Stderr})
		wr.Close()
	}()

	// grab the controller tls pin from the bootstrap output
	var cert controllerCert
	dec := json.NewDecoder(rd)
	for {
		var msg bootstrapMsg
		if err := dec.Decode(&msg); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("failed to parse bootstrap JSON output: %s", err)
		}
		c.log("bootstrap ===>", msg.Id, msg.State)
		if msg.State == "error" {
			c.log(msg.Error)
		}
		if msg.Id == "controller-cert" && msg.State == "done" {
			json.Unmarshal(msg.Data, &cert)
		}
	}
	if cmdErr != nil {
		return cmdErr
	}
	if cert.Pin == "" {
		return errors.New("could not determine controller cert from bootstrap output")
	}
	c.ControllerPin = cert.Pin

	// grab the router IP from discoverd
	disc, err := discoverd.NewClientWithAddr(inst.IP + ":1111")
	if err != nil {
		return fmt.Errorf("could not connect to discoverd at %s:1111: %s", inst.IP, err)
	}
	defer disc.Close()
	set, err := disc.NewServiceSet("router-api")
	if err != nil {
		return fmt.Errorf("could not detect router ip: %s", err)
	}
	defer set.Close()
	leader := set.Leader()
	if leader == nil {
		return errors.New("could not detect router ip: no router-api leader")
	}
	if err = setLocalDNS([]string{c.ClusterDomain, c.ControllerDomain()}, leader.Host); err != nil {
		return fmt.Errorf("could not set cluster DNS entries: %s", err)
	}
	c.RouterIP = leader.Host
	return nil
}

func setLocalDNS(domains []string, ip string) error {
	command := fmt.Sprintf(
		`grep -q "^%[1]s" /etc/hosts && sed "s/^%[1]s.*/%[1]s %s/" -i /etc/hosts || echo %[1]s %s >> /etc/hosts`,
		ip, strings.Join(domains, " "),
	)
	cmd := exec.Command("bash", "-c", command)
	return cmd.Run()
}

func lookupUser(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uid, gid, nil
}

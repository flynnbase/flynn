package etcdrunner

import (
	"bufio"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/flynnbase/flynn/Godeps/_workspace/src/github.com/coreos/go-etcd/etcd"
	"github.com/flynnbase/flynn/pkg/attempt"
	"github.com/flynnbase/flynn/pkg/random"
)

var Attempts = attempt.Strategy{
	Min:   5,
	Total: 5 * time.Second,
	Delay: 200 * time.Millisecond,
}

type TestingT interface {
	Fatal(...interface{})
	Fatalf(string, ...interface{})
	Log(...interface{})
}

func RunEtcdServer(t TestingT) (string, func()) {
	killCh := make(chan struct{})
	doneCh := make(chan struct{})
	name := "etcd-test." + strconv.Itoa(random.Math.Int())
	dataDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal("tempdir failed: ", err)
	}
	port, err := RandomPort()
	if err != nil {
		t.Fatal("error getting random etcd port: ", err)
	}
	clusterPort, err := RandomPort()
	if err != nil {
		t.Fatal("error getting random cluster port: ", err)
	}
	go func() {
		cmd := exec.Command("etcd",
			"-name", name,
			"-data-dir", dataDir,
			"-addr", "127.0.0.1:"+port,
			"-bind-addr", "127.0.0.1:"+port,
			"-peer-addr", "127.0.0.1:"+clusterPort,
			"-peer-bind-addr", "127.0.0.1:"+clusterPort,
		)
		var stderr, stdout io.Reader
		if os.Getenv("DEBUG") != "" {
			stderr, _ = cmd.StderrPipe()
			stdout, _ = cmd.StdoutPipe()
		}
		if err := cmd.Start(); err != nil {
			t.Fatal("etcd start failed: ", err)
			return
		}
		cmdDone := make(chan error)
		go func() {
			if stdout != nil {
				LogOutput("etcd", stdout, stderr)
			}
			cmdDone <- cmd.Wait()
		}()
		defer close(doneCh)
		select {
		case <-killCh:
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal("failed to kill etcd: ", err)
				return
			}
			err := <-cmdDone
			t.Log("etcd process exited: ", err)
		case err := <-cmdDone:
			t.Log("etcd process exited unexpectedly: ", err)
			return
		}
		if err := os.RemoveAll(dataDir); err != nil {
			t.Log("etcd data removal failed: ", err)
		}
	}()
	addr := "http://127.0.0.1:" + port

	// wait for etcd to come up
	client := etcd.NewClient([]string{addr})
	err = Attempts.Run(func() (err error) {
		_, err = client.Get("/", false, false)
		return
	})
	if err != nil {
		t.Fatal("Failed to connect to etcd: ", err)
	}

	return addr, func() {
		close(killCh)
		<-doneCh
	}
}

func LogOutput(name string, rs ...io.Reader) {
	var wg sync.WaitGroup
	wg.Add(len(rs))
	for _, r := range rs {
		go func(r io.Reader) {
			scanner := bufio.NewScanner(r)
			for scanner.Scan() {
				log.Println(name+":", scanner.Text())
			}
			wg.Done()
		}(r)
	}
	wg.Wait()
}

func RandomPort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return port, err
}

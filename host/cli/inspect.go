package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynnbase/flynn/Godeps/_workspace/src/github.com/flynn/go-docopt"
	"github.com/flynnbase/flynn/host/types"
	"github.com/flynnbase/flynn/pkg/cluster"
)

func init() {
	Register("inspect", runInspect, `
usage: flynn-host inspect ID

Get low-level information about a job.`)
}

func runInspect(args *docopt.Args, client *cluster.Client) error {
	hostID, jobID, err := cluster.ParseJobID(args.String["ID"])
	if err != nil {
		return err
	}
	hostClient, err := client.DialHost(hostID)
	if err != nil {
		return fmt.Errorf("could not connect to host %s: %s", hostID, err)
	}
	job, err := hostClient.GetJob(jobID)
	if err != nil {
		return fmt.Errorf("no such job")
	}

	printJobDesc(job, os.Stdout)
	return nil
}

func printJobDesc(job *host.ActiveJob, out io.Writer) {
	w := tabwriter.NewWriter(out, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "ID", clusterJobID(*job))
	listRec(w, "Entrypoint", strings.Join(job.Job.Config.Entrypoint, " "))
	listRec(w, "Cmd", strings.Join(job.Job.Config.Cmd, " "))
	listRec(w, "Status", job.Status)
	listRec(w, "StartedAt", job.StartedAt)
	listRec(w, "EndedAt", job.EndedAt)
	listRec(w, "ExitStatus", job.ExitStatus)
	listRec(w, "IP Address", job.InternalIP)
	for k, v := range job.Job.Metadata {
		listRec(w, k, v)
	}
	for k, v := range job.Job.Config.Env {
		listRec(w, fmt.Sprintf("ENV[%s]", k), v)
	}
}

func listRec(w io.Writer, a ...interface{}) {
	for i, x := range a {
		fmt.Fprint(w, x)
		if i+1 < len(a) {
			w.Write([]byte{'\t'})
		} else {
			w.Write([]byte{'\n'})
		}
	}
}

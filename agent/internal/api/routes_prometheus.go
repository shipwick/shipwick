package api

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

// prometheusRoutes registers the metrics endpoint in the Prometheus text format.
// It is not under /api/v1: /metrics is where a scraper looks.
func (s *Server) prometheusRoutes(routes routeTable) {
	routes.read("GET /metrics", s.handlePrometheus)
}

func (s *Server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	scrape, err := s.engine.Scrape(r.Context(), s.now())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write(exposition(scrape))
}

// exposition renders a scrape in the Prometheus text format, version 0.0.4.
// The format is a few lines of rules, written out here rather than brought in
// with a client library and its registry. Families come in a fixed order and
// series sorted by their labels, so two scrapes of the same state are the
// same bytes.
func exposition(sc deploy.Scrape) []byte {
	var p promWriter

	p.family("shipwick_agent_info", "gauge", "The running agent; the value is always 1.")
	p.series(1, "version", version.Version)

	p.family("shipwick_application_status", "gauge", "The current status of each application, in lower case; the value is always 1.")
	for _, a := range sc.Applications {
		if a.Status != "" {
			p.series(1, "application", a.Name, "status", strings.ToLower(string(a.Status)))
		}
	}

	p.family("shipwick_application_replicas", "gauge", "Replicas of each application: desired, running, and healthy (running and not failing the health check).")
	for _, a := range sc.Applications {
		p.series(float64(a.Replicas.Desired), "application", a.Name, "state", "desired")
		if a.Observed {
			p.series(float64(a.Replicas.Healthy), "application", a.Name, "state", "healthy")
			p.series(float64(a.Replicas.Running), "application", a.Name, "state", "running")
		}
	}

	p.family("shipwick_replica_cpu_ratio", "gauge", "CPU usage of a replica in cores, from the last sample: 1 is one core kept busy.")
	sampled(&p, sc, func(r deploy.ScrapeReplica) (float64, bool) { return r.CPUCores, true })
	p.family("shipwick_replica_memory_bytes", "gauge", "Working set of a replica, from the last sample.")
	sampled(&p, sc, func(r deploy.ScrapeReplica) (float64, bool) { return float64(r.MemoryBytes), true })
	p.family("shipwick_replica_memory_limit_bytes", "gauge", "Memory limit of a replica; absent for a replica without one.")
	sampled(&p, sc, func(r deploy.ScrapeReplica) (float64, bool) {
		return float64(r.MemoryLimitBytes), r.MemoryLimitBytes > 0
	})

	p.family("shipwick_replica_restarts_total", "counter", "Restarts of a replica by the supervisor since its deployment.")
	for _, a := range sc.Applications {
		for _, r := range a.ReplicaStats {
			p.series(float64(r.Restarts), "application", a.Name, "replica", strconv.Itoa(r.Replica))
		}
	}

	p.family("shipwick_deployments_total", "counter", "Finished deployments of each application by outcome: succeeded, failed, rolled_back.")
	for _, a := range sc.Applications {
		p.series(float64(a.Deployments.Failed), "application", a.Name, "status", "failed")
		p.series(float64(a.Deployments.RolledBack), "application", a.Name, "status", "rolled_back")
		p.series(float64(a.Deployments.Succeeded), "application", a.Name, "status", "succeeded")
	}

	p.family("shipwick_deployment_last_duration_seconds", "gauge", "How long the most recently completed deployment of each application took.")
	for _, a := range sc.Applications {
		if a.Completed {
			p.series(a.LastDuration.Seconds(), "application", a.Name)
		}
	}

	p.family("shipwick_disk_bytes", "gauge", "The disk that holds the agent's data, images and volumes: total and used, counted as df does.")
	if sc.Disk != nil {
		p.series(float64(sc.Disk.TotalBytes), "state", "total")
		p.series(float64(sc.Disk.UsedBytes), "state", "used")
	}

	p.family("shipwick_alerts", "gauge", "Active alerts by kind and severity.")
	active := map[[2]string]int{}
	for _, a := range sc.Alerts {
		active[[2]string{a.Kind, a.Severity}]++
	}
	for _, kind := range []string{api.AlertDisk, api.AlertMemory, api.AlertRestarts, api.AlertUnhealthy} {
		for _, severity := range []string{api.SeverityCritical, api.SeverityWarning} {
			p.series(float64(active[[2]string{kind, severity}]), "kind", kind, "severity", severity)
		}
	}
	return p.buf.Bytes()
}

// sampled writes one series per replica that has a recent sample.
func sampled(p *promWriter, sc deploy.Scrape, value func(deploy.ScrapeReplica) (float64, bool)) {
	for _, a := range sc.Applications {
		for _, r := range a.ReplicaStats {
			if v, ok := value(r); ok && r.Sampled {
				p.series(v, "application", a.Name, "replica", strconv.Itoa(r.Replica))
			}
		}
	}
}

type promWriter struct {
	buf  bytes.Buffer
	name string // the family being written
}

func (p *promWriter) family(name, typ, help string) {
	p.name = name
	p.buf.WriteString("# HELP " + name + " " + helpEscaper.Replace(help) + "\n")
	p.buf.WriteString("# TYPE " + name + " " + typ + "\n")
}

// series writes one sample of the current family; labels are name, value pairs.
func (p *promWriter) series(value float64, labels ...string) {
	p.buf.WriteString(p.name)
	for i := 0; i+1 < len(labels); i += 2 {
		if i == 0 {
			p.buf.WriteByte('{')
		} else {
			p.buf.WriteByte(',')
		}
		p.buf.WriteString(labels[i] + `="` + labelEscaper.Replace(labels[i+1]) + `"`)
	}
	if len(labels) > 0 {
		p.buf.WriteByte('}')
	}
	p.buf.WriteByte(' ')
	p.buf.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	p.buf.WriteByte('\n')
}

// What the format asks to be escaped: backslash and line feed everywhere, and
// the double quote inside a label value.
var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
)

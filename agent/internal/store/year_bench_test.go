package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// A database of the size a busy year makes it, and what the queries the API
// and the agent's loops run cost against it. The shape follows the handbook's
// "The database in a year": 20 applications, a deployment of two replicas
// and 13 events every day of the year for each of ten of them, supervisor
// events up to the 500 kept, a week of 30-second samples of 40 replicas, a
// week of traffic minutes, and 100,000 audit entries.
type yearOld struct {
	s    *Store
	apps []Application
	// busy is an application with 365 deployments; quiet has one.
	busy, quiet Application
	now         time.Time
}

// fillYear fills a store with `days` days of that workload; 365 is the
// year, and a few days give the same query plans in a fraction of the time.
func fillYear(tb testing.TB, days int) *yearOld {
	tb.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(tb.TempDir(), "shipwick.db"), testOptions)
	if err != nil {
		tb.Fatalf("Open: %v", err)
	}
	tb.Cleanup(func() { s.Close() })
	y := &yearOld{s: s, now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	start := y.now.Add(-time.Duration(days) * 24 * time.Hour)

	deploy := func(name string, n int, at time.Time) {
		d, err := s.CreateDeployment(ctx, testApp(name, fmt.Sprintf("registry.example.com/%s:%d", name, n)), at)
		if err != nil {
			tb.Fatal(err)
		}
		for _, to := range []api.DeploymentStatus{api.StatusBuilding, api.StatusStarting, api.StatusHealthChecking, api.StatusHealthy} {
			if err := s.TransitionDeployment(ctx, d.ID, s.statusBefore(to), to, ""); err != nil {
				tb.Fatal(err)
			}
		}
		if _, err := s.ActivateDeployment(ctx, d.ID, at); err != nil {
			tb.Fatal(err)
		}
		if err := s.CompleteDeployment(ctx, d.ID, at.Add(10*time.Second)); err != nil {
			tb.Fatal(err)
		}
		for i := 1; i <= 2; i++ {
			r := Replica{DeploymentID: d.ID, Index: i, ContainerID: fmt.Sprintf("%s-%d-%d-%032x", name, n, i, d.ID), ContainerName: fmt.Sprintf("shipwick_%s_%d_%d", name, n, i)}
			if err := s.AddReplica(ctx, r, at); err != nil {
				tb.Fatal(err)
			}
		}
		for i := range 13 {
			if err := s.AddEvent(ctx, d.ApplicationID, &d.ID, api.LevelInfo, api.EventState, fmt.Sprintf("step %d of deployment %d", i, d.ID), at.Add(time.Duration(i)*time.Second)); err != nil {
				tb.Fatal(err)
			}
		}
	}
	for a := range 20 {
		name := fmt.Sprintf("app-%02d", a)
		if a < 10 {
			for day := range days {
				deploy(name, day+1, start.Add(time.Duration(day)*24*time.Hour))
			}
		} else {
			deploy(name, 1, start)
		}
	}
	apps, err := s.ListApplications(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	y.apps = apps
	y.busy, y.quiet = apps[0], apps[19]

	// Supervisor events: a replica that restarted now and then, up to the
	// 500 kept per application.
	for _, app := range apps {
		for i := range min(500, days) {
			if err := s.AddEvent(ctx, app.ID, nil, api.LevelWarn, api.EventSupervisor, "Replica 1 restarted (attempt 1)", start.Add(time.Duration(i)*17*time.Hour)); err != nil {
				tb.Fatal(err)
			}
		}
	}

	// A week of samples, in rounds of one per replica, as the sampler writes
	// them; and a week of traffic minutes for every application.
	weekAgo := y.now.Add(-time.Duration(min(days, 7)) * 24 * time.Hour)
	for at := weekAgo; at.Before(y.now); at = at.Add(30 * time.Minute) {
		var rows []MetricSample
		for t := at; t.Before(at.Add(30 * time.Minute)); t = t.Add(30 * time.Second) {
			for _, app := range apps {
				for r := 1; r <= 2; r++ {
					rows = append(rows, MetricSample{ApplicationID: app.ID, Replica: r, At: t, CPUPercent: 12.5, MemoryBytes: 200 << 20})
				}
			}
		}
		if err := s.AddSamples(ctx, rows); err != nil {
			tb.Fatal(err)
		}
	}
	latency := make([]int64, 15)
	latency[3] = 40
	for at := weekAgo; at.Before(y.now); at = at.Add(6 * time.Hour) {
		var rows []TrafficSample
		for t := at; t.Before(at.Add(6 * time.Hour)); t = t.Add(time.Minute) {
			for _, app := range apps {
				rows = append(rows, TrafficSample{ApplicationID: app.ID, At: t, Requests: 40, Status2xx: 40, Bytes: 20480, Latency: latency})
			}
		}
		if err := s.AddTrafficSamples(ctx, rows); err != nil {
			tb.Fatal(err)
		}
	}

	for i := range 274 * days {
		entry := api.AuditEntry{At: start.Add(time.Duration(i) * 5 * time.Minute), Actor: api.Actor{Kind: api.ActorToken, Name: fmt.Sprintf("ci-%d", i%7)},
			Address: "203.0.113.7", Action: "deployment.create", Application: fmt.Sprintf("app-%02d", i%20), Outcome: api.AuditOK, Status: 202}
		if _, err := s.AddAuditEntry(ctx, entry); err != nil {
			tb.Fatal(err)
		}
	}
	return y
}

// statusBefore is the status a happy deployment leaves for `to`.
func (s *Store) statusBefore(to api.DeploymentStatus) api.DeploymentStatus {
	switch to {
	case api.StatusBuilding:
		return api.StatusPending
	case api.StatusStarting:
		return api.StatusBuilding
	case api.StatusHealthChecking:
		return api.StatusStarting
	}
	return api.StatusHealthChecking
}

func (y *yearOld) count(tb testing.TB, table string) int {
	var n int
	if err := y.s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		tb.Fatal(err)
	}
	return n
}

func BenchmarkYearOldDatabase(b *testing.B) {
	y := fillYear(b, 365)
	ctx := context.Background()
	s := y.s
	for _, table := range []string{"deployments", "deployment_replicas", "events", "metric_samples", "traffic_samples", "audit_log"} {
		b.Logf("%-20s %8d rows", table, y.count(b, table))
	}
	must := func(err error) {
		if err != nil {
			b.Fatal(err)
		}
	}
	containerID := func() string {
		var id string
		must(s.db.QueryRowContext(ctx, `SELECT container_id FROM deployment_replicas ORDER BY id DESC LIMIT 1`).Scan(&id))
		return id
	}()

	b.Run("ListDeployments/all,limit=50", func(b *testing.B) {
		for b.Loop() {
			_, err := s.ListDeployments(ctx, DeploymentFilter{Limit: 50})
			must(err)
		}
	})
	b.Run("ListDeployments/busy,limit=25", func(b *testing.B) {
		for b.Loop() {
			_, err := s.ListDeployments(ctx, DeploymentFilter{Application: y.busy.Name, Limit: 25})
			must(err)
		}
	})
	b.Run("ListDeployments/quiet,limit=25", func(b *testing.B) {
		for b.Loop() {
			_, err := s.ListDeployments(ctx, DeploymentFilter{Application: y.quiet.Name, Limit: 25})
			must(err)
		}
	})
	b.Run("ListDeployments/in-flight", func(b *testing.B) {
		for b.Loop() {
			_, err := s.ListDeployments(ctx, DeploymentFilter{Statuses: []api.DeploymentStatus{api.StatusPending, api.StatusBuilding, api.StatusStarting, api.StatusHealthChecking, api.StatusHealthy, api.StatusRollback}})
			must(err)
		}
	})
	b.Run("ListDeployments/superseded,limit=1", func(b *testing.B) {
		for b.Loop() {
			_, err := s.ListDeployments(ctx, DeploymentFilter{Application: y.busy.Name, Statuses: []api.DeploymentStatus{api.StatusSuperseded}, Limit: 1})
			must(err)
		}
	})
	b.Run("ActiveDeployments", func(b *testing.B) {
		for b.Loop() {
			_, err := s.ActiveDeployments(ctx)
			must(err)
		}
	})
	b.Run("GetDeployment", func(b *testing.B) {
		for b.Loop() {
			_, err := s.GetDeployment(ctx, *y.busy.ActiveDeploymentID)
			must(err)
		}
	})
	b.Run("ListReplicas", func(b *testing.B) {
		for b.Loop() {
			_, err := s.ListReplicas(ctx, *y.busy.ActiveDeploymentID)
			must(err)
		}
	})
	b.Run("MarkReplicaRemoved/unknown", func(b *testing.B) {
		for b.Loop() {
			must(s.MarkReplicaRemoved(ctx, "no-such-container", y.now))
		}
	})
	b.Run("IncrementReplicaRestarts", func(b *testing.B) {
		for b.Loop() {
			must(s.IncrementReplicaRestarts(ctx, containerID))
		}
	})
	b.Run("ListApplicationEvents/busy,limit=50", func(b *testing.B) {
		for b.Loop() {
			_, err := s.ListApplicationEvents(ctx, y.busy.ID, 50)
			must(err)
		}
	})
	b.Run("ListDeploymentEvents", func(b *testing.B) {
		for b.Loop() {
			_, err := s.ListDeploymentEvents(ctx, *y.busy.ActiveDeploymentID)
			must(err)
		}
	})
	b.Run("PruneApplicationEvents", func(b *testing.B) {
		for b.Loop() {
			must(s.PruneApplicationEvents(ctx, y.busy.ID, 500))
		}
	})
	for _, window := range []struct {
		name  string
		since time.Duration
		step  time.Duration
	}{{"1h", time.Hour, 30 * time.Second}, {"24h", 24 * time.Hour, 5 * time.Minute}, {"7d", 7 * 24 * time.Hour, time.Hour}} {
		b.Run("MetricHistory/"+window.name, func(b *testing.B) {
			for b.Loop() {
				_, err := s.MetricHistory(ctx, y.busy.ID, y.now.Add(-window.since), window.step)
				must(err)
			}
		})
	}
	b.Run("RecentSamples", func(b *testing.B) {
		for b.Loop() {
			_, err := s.RecentSamples(ctx, y.busy.ID, y.now.Add(-90*time.Second))
			must(err)
		}
	})
	b.Run("TrafficSamples/7d", func(b *testing.B) {
		for b.Loop() {
			_, err := s.TrafficSamples(ctx, y.busy.ID, y.now.Add(-7*24*time.Hour))
			must(err)
		}
	})
	b.Run("AddSamples/40", func(b *testing.B) {
		rows := make([]MetricSample, 0, 40)
		for _, app := range y.apps {
			for r := 1; r <= 2; r++ {
				rows = append(rows, MetricSample{ApplicationID: app.ID, Replica: r, At: y.now, CPUPercent: 1, MemoryBytes: 1})
			}
		}
		for b.Loop() {
			must(s.AddSamples(ctx, rows))
		}
	})
	b.Run("PruneSamples/nothing-to-prune", func(b *testing.B) {
		for b.Loop() {
			_, err := s.PruneSamples(ctx, y.now.Add(-8*24*time.Hour))
			must(err)
		}
	})
	b.Run("PruneTrafficSamples/nothing-to-prune", func(b *testing.B) {
		for b.Loop() {
			_, err := s.PruneTrafficSamples(ctx, y.now.Add(-8*24*time.Hour))
			must(err)
		}
	})
	b.Run("AuditEntries/latest,limit=50", func(b *testing.B) {
		for b.Loop() {
			_, err := s.AuditEntries(ctx, AuditFilter{Limit: 50})
			must(err)
		}
	})
	b.Run("AuditEntries/application,limit=50", func(b *testing.B) {
		for b.Loop() {
			_, err := s.AuditEntries(ctx, AuditFilter{Application: "app-03", Limit: 50})
			must(err)
		}
	})
	b.Run("AuditEntries/actor+action,limit=50", func(b *testing.B) {
		for b.Loop() {
			_, err := s.AuditEntries(ctx, AuditFilter{Actor: "ci-2", Actions: []string{"deployment."}, Limit: 50})
			must(err)
		}
	})
	b.Run("AuditEntries/since,limit=50", func(b *testing.B) {
		for b.Loop() {
			_, err := s.AuditEntries(ctx, AuditFilter{Since: y.now.Add(-30 * 24 * time.Hour), Limit: 50})
			must(err)
		}
	})
	b.Run("AddAuditEntry", func(b *testing.B) {
		entry := api.AuditEntry{At: y.now, Actor: api.Actor{Kind: api.ActorToken, Name: "ci"}, Address: "203.0.113.7", Action: "deployment.create", Application: "app-00", Outcome: api.AuditOK, Status: 202}
		for b.Loop() {
			_, err := s.AddAuditEntry(ctx, entry)
			must(err)
		}
	})
	b.Run("GetTokenByHash/miss", func(b *testing.B) {
		hash := make([]byte, 32)
		for b.Loop() {
			if _, err := s.GetTokenByHash(ctx, hash); err != ErrNotFound {
				must(err)
			}
		}
	})
	b.Run("GetApplication", func(b *testing.B) {
		for b.Loop() {
			_, err := s.GetApplication(ctx, y.busy.Name)
			must(err)
		}
	})
}

// BenchmarkPruneSamplesAnHour is the hourly prune with an hour's worth of
// samples to remove, as the sampler runs it: a week of 40 replicas in the
// table, 4,800 rows due.
func BenchmarkPruneSamplesAnHour(b *testing.B) {
	y := fillYear(b, 365)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		// One more hour of rows, dated before the window so that the prune
		// has something to remove, then the prune.
		b.StopTimer()
		var rows []MetricSample
		for t := range 120 {
			for _, app := range y.apps {
				for r := 1; r <= 2; r++ {
					rows = append(rows, MetricSample{ApplicationID: app.ID, Replica: r, At: y.now.Add(-8 * 24 * time.Hour).Add(time.Duration(t) * 30 * time.Second), CPUPercent: 1, MemoryBytes: 1})
				}
			}
		}
		if err := y.s.AddSamples(ctx, rows); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := y.s.PruneSamples(ctx, y.now.Add(-7*24*time.Hour)); err != nil {
			b.Fatal(err)
		}
	}
}

// TestQueryPlansOfAYearOldDatabase prints how SQLite answers the queries the
// agent runs most, against the row counts of a year, so that a change to a
// query or an index is judged by the plan and not by a guess.
func TestQueryPlansOfAYearOldDatabase(t *testing.T) {
	y := fillYear(t, 2)
	ctx := context.Background()
	queries := map[string]string{
		"ListDeployments(application)": deploymentSelect + ` WHERE a.name = 'app-00' ORDER BY d.id DESC LIMIT 25`,
		"ListDeployments(in flight)":   deploymentSelect + ` WHERE d.status IN ('PENDING', 'BUILDING') ORDER BY d.id DESC`,
		"ListDeployments(superseded)":  deploymentSelect + ` WHERE a.name = 'app-00' AND d.status IN ('SUPERSEDED') ORDER BY d.id DESC LIMIT 1`,
		"ActiveDeployments":            deploymentSelect + ` WHERE d.id IN (SELECT active_deployment_id FROM applications WHERE active_deployment_id IS NOT NULL)`,
		"ActiveReplicas": `SELECT deployment_id FROM deployment_replicas
			 WHERE deployment_id IN (SELECT active_deployment_id FROM applications WHERE active_deployment_id IS NOT NULL)
			   AND removed_at IS NULL ORDER BY deployment_id, replica_index`,
		"ListDeployments(application,by sequence)": deploymentSelect + ` WHERE a.name = 'app-00' ORDER BY d.sequence DESC LIMIT 25`,
		"ListDeployments(superseded,by sequence)":  deploymentSelect + ` WHERE a.name = 'app-00' AND d.status IN ('SUPERSEDED') ORDER BY d.sequence DESC LIMIT 1`,
		"PruneSamples(per application)":            `DELETE FROM metric_samples WHERE application_id = 1 AND at < 'x'`,
		"ListReplicas":                             `SELECT deployment_id FROM deployment_replicas WHERE deployment_id = 1 AND removed_at IS NULL ORDER BY replica_index`,
		"MarkReplicaRemoved":                       `UPDATE deployment_replicas SET removed_at = 'x' WHERE container_id = 'c' AND removed_at IS NULL`,
		"ListApplicationEvents":                    `SELECT id FROM events WHERE application_id = 1 AND deployment_id IS NULL ORDER BY id DESC LIMIT 50`,
		"ListDeploymentEvents":                     `SELECT id FROM events WHERE deployment_id = 1 ORDER BY id`,
		"PruneApplicationEvents":                   `DELETE FROM events WHERE application_id = 1 AND deployment_id IS NULL AND id NOT IN (SELECT id FROM events WHERE application_id = 1 AND deployment_id IS NULL ORDER BY id DESC LIMIT 500)`,
		"MetricHistory":                            `SELECT replica, (CAST(strftime('%s', at) AS INTEGER) / 3600) * 3600 AS bucket, AVG(cpu_percent), MAX(memory_bytes) FROM metric_samples WHERE application_id = 1 AND at >= 'x' GROUP BY replica, bucket ORDER BY replica, bucket`,
		"PruneSamples":                             `DELETE FROM metric_samples WHERE at < 'x'`,
		"PruneTrafficSamples":                      `DELETE FROM traffic_samples WHERE at < 'x'`,
		"TrafficSamples":                           `SELECT at FROM traffic_samples WHERE application_id = 1 AND at >= 'x' ORDER BY at`,
		"AuditEntries(latest)":                     `SELECT id FROM audit_log ORDER BY id DESC LIMIT 50`,
		"AuditEntries(application)":                `SELECT id FROM audit_log WHERE application = 'a' ORDER BY id DESC LIMIT 50`,
		"AuditEntries(actor,action)":               `SELECT id FROM audit_log WHERE actor = 'a' AND (substr(action, 1, 11) = 'deployment.') ORDER BY id DESC LIMIT 50`,
		"AuditEntries(since)":                      `SELECT id FROM audit_log WHERE at >= 'x' ORDER BY id DESC LIMIT 50`,
		"AuditPrune(by age)":                       `DELETE FROM audit_log WHERE at < 'x'`,
		"LogArchivedThrough":                       `SELECT ended_at, last_at FROM log_archives WHERE container_id = 'c' ORDER BY id DESC LIMIT 1`,
	}
	names := make([]string, 0, len(queries))
	for name := range queries {
		names = append(names, name)
	}
	// Stable output, whatever the map order.
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	for _, name := range names {
		rows, err := y.s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN `+queries[name])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		t.Logf("%-30s %s", name, strings.Join(plan, " | "))
	}
}

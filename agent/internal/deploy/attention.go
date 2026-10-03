package deploy

import (
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// A list of applications should be able to say which of them wants a look
// without asking about each one. What an application's own page shows in full
// — the certificate of every hostname, every alert — is summed up here from
// what the agent already holds in memory: the last look of the certificate
// watch and the alert book. Nothing is probed or read for it.

// certificateRank orders the certificate states that are not in order, worst
// first: a hostname that is not served at all and waits for its operator, a
// certificate that runs out unless somebody acts, one the proxy is still
// obtaining and usually has within seconds. "unknown" is not among them:
// nothing is known against the certificate, and an agent without a proxy
// would mark every application.
var certificateRank = map[string]int{
	api.CertWaitingForDNS: 3,
	api.CertExpiring:      2,
	api.CertObtaining:     1,
}

// certificateProblem is the hostname of the application whose certificate is
// furthest from in order, or nil when none is known to be out of order. Of
// two that are equally bad the first in deploy.yaml's order is named.
func (e *Engine) certificateProblem(a spec.App) *api.CertificateProblem {
	if e.opts.Proxy == nil {
		return nil
	}
	var worst *api.CertificateProblem
	for _, c := range e.certificates(hostnamesOf(a)) {
		if certificateRank[c.Status] == 0 {
			continue
		}
		if worst == nil || certificateRank[c.Status] > certificateRank[worst.Status] {
			worst = &api.CertificateProblem{Hostname: c.Hostname, Status: c.Status, Message: c.Message}
		}
	}
	return worst
}

// alertSummary counts the active alerts about an application and names the
// highest severity among them, "" when there is none.
func (e *Engine) alertSummary(application string) (count int, severity string) {
	e.alerts.locked(func() {
		for _, a := range e.alerts.active {
			if a.Application != application {
				continue
			}
			count++
			if a.Severity == api.SeverityCritical || severity == "" {
				severity = a.Severity
			}
		}
	})
	return count, severity
}

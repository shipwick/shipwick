# Roadmap

What the current version does is in [README.md](README.md); what changed
between versions is in [CHANGELOG.md](CHANGELOG.md). This is what comes next,
one theme per minor version, in the order it is likely to ship. Nothing here
is a promise; an item moves when a real installation shows that something
else matters more.

## 0.5 — The proxy

What sits in front of the applications: Cloudflare, headers and
authentication, and what the proxy could tell about the traffic it carries.
Before that, what 0.4 — the first half hour, from an empty server to a running
application — left unfinished.

### Left over from 0.4

Small things that the first real installations turned up, none of them
blocking, each a question a new user should not have to ask.

**The CLI on the server knows its own agent.** The installer puts `shipwick`
on the server but not how to reach the agent, whose port is deliberately not
published; the first command run there fails with a tunnel hint meant for
laptops. The installer will save a context for root — the API hostname and
the token it already has — so that `shipwick ps` works on the server as it
does anywhere else.

**Commands that know `shipwick.yaml`.** `deploy` and `validate` read it;
`status`, `logs`, `stop` and the others that take the application's name
from `deploy.yaml` do not, and ask for a name in a directory that has only a
`shipwick.yaml`. `shipwick init` in such a directory should add an entry to
the file instead of writing a second one next to it.

**Sending only the layers the server lacks.** With `build: .` the whole image
travels on every deployment, base layers included: 59 MB for a ten-line Node
application whose own layer is a few kilobytes. The agent already has most of
it after the first time.

**Asking the agent before building.** A document the agent will refuse — a
port already published, a secret that is not stored — is refused after the
image was built and sent. The CLI checks the domain itself today; a dry run
of the agent's own validation would cover every case with one request.

**A failed deployment cleans up after itself.** The image sent for a
deployment that then fails stays until the application's next successful
deployment sweeps it; it should go when the failure is recorded.

**A quieter build.** `docker build` prints forty lines for a build that takes
two seconds. Show a progress line while it runs and the full output only when
it fails.

**`logs -f` that says it is waiting.** Following an application that has
printed nothing yet shows an empty screen, which reads as a hang.

**Why replacing one replica takes fourteen seconds.** A first deployment of
the example application takes 2.4 s, every later one 13.7 s. Find where the
time goes when a single replica is replaced, and whether it has to.

**More kinds of project for `init`.** SvelteKit, Remix and Astro with a
server, a Next.js static export, Python projects locked with `uv`; a warning
when there is no lock file and the build is therefore not reproducible; and
`build: {dockerfile: X}` without `context` meaning the directory of the file.

**Static sites: a fallback page and a clean switch.** A single-page
application needs unknown paths to answer with `index.html`
(`static: {dir: dist, fallback: index.html}`). And an application that turns
from a folder into a container leaves its folders in the proxy until it is
deleted.

**Compression for the API and the dashboard.** Application routes are
compressed; the agent's own API and the dashboard are not, because the
encoder may hold back the first lines of a followed log. Measure it against a
real proxy, and compress everything that is not a stream.

**The dashboard's address, where the CLI can find it.** The agent does not
report the dashboard's hostname, so `shipwick open --dashboard` does not exist
and a first deployment cannot end with a link to it.

**Dashboard details.** A `429` says how long to wait and the dashboard polls
on regardless; the log viewer offers static applications, which have no logs;
a deployment refused for a missing secret could offer to add it.

**`doctor` recognises Cloudflare.** The agent names Cloudflare's proxy when a
record points at it; `doctor` says "a CDN". One list of ranges, one wording.

**The GitHub Action against a real server.** `shipwick/deploy` is tested for
downloading and verifying the CLI on every runner; a deployment from a
workflow to a live agent has not been run end to end.

### The proxy

**Cloudflare in front of the server.** Today an application's DNS record must
point straight at the server (Cloudflare "DNS only"), because Caddy proves
ownership of a hostname over HTTP and the proxy in between breaks that. With a
Cloudflare API token on the agent (`SHIPWICK_CLOUDFLARE_API_TOKEN`, DNS edit
rights on the zone), Caddy will prove ownership through a DNS record instead,
so the orange cloud can stay on. That needs a Caddy image of Shipwick's own,
built with the Cloudflare DNS module; Cloudflare's address ranges as trusted
proxies, so that applications see the visitor's address rather than
Cloudflare's; and a note on the zone's SSL mode, which must be *Full (strict)*.
Wildcard certificates come with it.

**Custom proxy behaviour per application.** Response headers, basic
authentication on a path, a redirect from one path to another. A small set of
options in `deploy.yaml`, modelled as data and rendered into Caddy's
configuration like everything else; never a raw Caddyfile.

**Request logs and traffic from the proxy.** What the proxy sees — status
codes, latency, requests per second per application — is not visible today;
`shipwick logs` shows only what the application writes. The proxy's access
log, kept per application and summarised in the dashboard.

**Paths on one domain.** `example.com/api` served by `api`, everything else
by `web`: a `path` next to `domain`, the longest match wins, and the two
applications keep their own replicas, health checks and rollouts. Today a
hostname belongs to one application.

**Certificate status.** `shipwick status` and the dashboard say when a
hostname is served but its certificate is still being obtained, and why.

**A certificate of your own.** A hostname whose certificate comes from
somewhere else — a corporate authority, a wildcard bought years ago — served
with the certificate and key you give the agent, kept encrypted like secrets
are, and reported before it expires. Automatic certificates stay the default.

**Time to finish for long connections.** A replica that is being replaced
leaves the proxy first and is then given a moment to finish what it has in
hand; that moment is the same for every application. `deploy.stop_timeout`
in `deploy.yaml`, for the application that holds WebSockets or long uploads.

## 0.6 — Running it for a year

The things an installation needs once it has been up for months: backups
that happen without anyone remembering, alerts before a limit is hit, keys
that can be rotated, and nothing that depends on the agent never restarting.

**Scheduled backups.** `backups` in `deploy.yaml`: a schedule, how many to
keep, and where to put them — a directory on the server first, an S3-compatible
bucket after. `shipwick backup` stays for the one you take by hand.

**Backups a database can be restored from.** An archive of a volume taken
while Postgres writes to it may not be one Postgres can start from. A
command run in the replica before the archive is taken (`backups.before`:
`pg_dump`, a checkpoint, a lock), or stopping the application for the
duration, chosen per application.

**Backups that are known to restore.** `shipwick backup verify`: restore the
latest backup into a scratch volume, start the image against it, run the
health check, throw it away. A backup nobody has restored is a hope.

**The agent's own state, backed up without being asked.** `shipwick.db` and
`encryption.key` go with the scheduled backups, encrypted under a passphrase
that is not on the server, and the agent warns in `shipwick doctor` and the
dashboard while no copy of the key exists anywhere else. Losing the key today
loses every secret, and only the documentation says so.

**Alerts from metrics.** A replica near its memory limit, a disk filling up,
a health check failing for longer than a threshold: posted to the webhook like
deployment outcomes are.

**Registry credentials the agent keeps.** `shipwick registry login ghcr.io`
stores a read-only token on the server, encrypted like environment values, and
the agent uses it when it pulls. Today a private image needs `docker login` on
the server and a mount in `compose.override.yml`. Credential helpers
(`credsStore`) in `~/.docker/config.json` are read as well.

**Key rotation.** `shipwick-agent rotate-key` re-encrypts every stored
environment value under a new key, without a stop.

**Moving to a new server.** `shipwick export` writes every application's
configuration and secrets and a backup of every volume into one archive;
`shipwick import` on the new server deploys them in dependency order and
restores the data. The same command backs up the agent's own `shipwick.db`
and `encryption.key`, which today the documentation asks you to copy by hand.

**Deployments that survive an agent restart.** An interrupted deployment is
marked `FAILED` and cleaned up; the containers it had started are removed.
Resuming where it stopped would cost a restart nothing.

**What happens when things break, written down and tested.** A full disk, a
Docker daemon that stops answering, a network that drops in the middle of a
pull, an agent killed half-way through a rollout: each with a test that
produces it, the behaviour that test pins down, and a page that says what the
operator sees and does. Some of this is tested today; none of it is in one
place.

**More than one distribution in CI.** The integration tests run on one Ubuntu
image with one Docker version. A matrix over the distributions the installer
claims to support and the Docker versions still in use, on amd64 and arm64.

**Metrics for whoever already has a Prometheus.** `GET /metrics` on the
agent, in the Prometheus text format: replicas, health, restarts, CPU and
memory per application, deployment outcomes. A week of history in SQLite
stays for those who have nothing else; this is for those who do.

## 0.7 — Teams

More than one person, more than one server, and a record of who did what.

**Roles per application.** A token that may deploy one application and read
the others.

**An audit trail.** Deployments record who made them and stop/start events name
the token; a delete, a restore, a token created or revoked are only in the
agent's log. Every action a token takes, with who and when, in one place.

**The API reachable only from the proxy and the dashboard.** Application
containers share the `shipwick` network with the agent, which needs it for
health checks, so today they can reach the API and try tokens against it. The
agent should answer only the proxy, the dashboard and the server itself.

**Tokens that expire.** `shipwick token create ci --role deploy --expires 90d`;
an expired token is refused with a message that says so, and `token ls` shows
what is about to lapse.

**Containers locked down further, on request.** Replicas already run without
new privileges, without host mounts and without ports they did not ask for.
`security` in `deploy.yaml` for the rest: a read-only root filesystem,
dropped capabilities, a refusal to run as root.

**Less than the whole Docker socket.** The agent holds the Docker socket,
which is root on the server. Running it against a socket proxy that allows
only the calls it makes, and against rootless Docker, each documented and
tested, for installations where that matters more than convenience.

**Behind a corporate proxy.** An agent that pulls images and reaches
webhooks through `HTTPS_PROXY`, trusts an internal certificate authority, and
can be installed from files copied to a server that has no way out.

**A new application from the dashboard.** Paste a `deploy.yaml`, deploy. Today
the dashboard redeploys and rolls back what the CLI created.

**Several servers in one dashboard.** The CLI already switches between servers
with contexts; the dashboard should too, one sign-in per server.

## 0.8 — Logs and distribution

**Log archiving.** A run's output and a replica's last log lines kept beyond
the container's life, searchable from the dashboard.

**Distribution.** A Windows arm64 build of the CLI; Debian and RPM packages for
the agent as a plain binary; an update notice in the dashboard when a newer
release exists.

**Signed releases.** Every binary and image signed at release time, with a
software bill of materials and build provenance, so that what a server runs as
root can be traced to a commit in this repository.

**`winget install` in the documentation.** The package is submitted to
winget-pkgs and waits for review; once it is accepted, `winget install
Shipwick.Shipwick` replaces "download the .exe and put it on your PATH" as the
way to install on Windows.

**The winget submission from the release workflow.** Today a release is
followed by two commands (`packaging/winget/update-manifests.sh`, then
`wingetcreate submit`) run by a maintainer. Doing it from the workflow needs a
GitHub token with write access to a fork of winget-pkgs stored in this
repository, which the Homebrew tap deliberately avoids; worth it once the
cadence makes the two commands a chore.

## Documentation, as it becomes true

Not tied to a version; written when someone needs them.

**From Docker Compose.** A guide for the commonest starting point: a
`docker-compose.yml` on a server, and what each of its parts becomes in
`deploy.yaml` and `shipwick.yaml`.

**GitLab CI.** The GitHub Action has a page; the same three lines for a
`.gitlab-ci.yml`.

**Where your data is.** One page for whoever has to answer a security
questionnaire: what the agent stores and where, what is encrypted and with
which key, what leaves the server (nothing, unless a webhook is set), and
what a backup contains.

## 1.0

1.0 is a promise, not a feature: from then on `deploy.yaml`, the API and the
on-disk format change only with a migration and a documented upgrade, and a
CLI of one 1.x version works against an agent of any other. It is cut when
the 0.x series has run real installations for long enough that the last two
minor versions changed nothing anyone had to relearn, when every item above
has either shipped or been dropped on purpose, and when the limits of one
server are written down with numbers that were measured.

## Out of scope

Three things are not on this list and will not be. They are the lines that
keep everything above simple; a request that crosses one of them is answered
with no, however good the request.

**Scheduling across machines.** Placing applications on several servers,
moving them when one fails, balancing replicas between machines. That is what
an orchestrator is for, and it costs cluster membership, discovery across
hosts, distributed state and split brains — the moment they arrive, "one
process and one SQLite file" is over. Managing several independent servers
from one CLI or one dashboard is a different thing and is on the list.

**Building images.** Turning a Dockerfile into an image is Docker's job; the
agent runs images that exist. A build on the production server takes the CPU
and memory the applications are there to use, brings a build cache and base
images to manage, and widens what the agent can be made to run. `build: .`
in 0.4 does not cross this line: the build happens on the developer's machine,
and the agent only loads the result.

**Anything that needs an external database, queue or cache.** Shipwick's own
state is one SQLite file, its queue is a goroutine, its cache is memory, and
it stays that way: the installation is one command and one directory, the
backup is two files, and "first set up a database" is a sentence nobody has to
read. Applications on Shipwick use Postgres and Redis as much as they like;
this is about what Shipwick itself depends on, and [AGENTS.md](AGENTS.md)
holds the same rule for the code.

When one server is no longer enough, you have outgrown Shipwick, and that is
fine: the `deploy.yaml` you wrote says everything about the application that
the next platform will ask.

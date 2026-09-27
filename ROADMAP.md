# Roadmap

What the current version does is in [README.md](README.md); what changed
between versions is in [CHANGELOG.md](CHANGELOG.md). This is what comes next,
one theme per minor version, in the order it is likely to ship. Nothing here
is a promise; an item moves when a real installation shows that something
else matters more.

## 0.4 — The first half hour

A developer with two APIs, a frontend and one server should get from nothing
to three running applications without opening an account anywhere else,
without learning what a registry is, and without being told to run something
"on the server" without knowing which one.

**Images without a registry.** `build: .` in `deploy.yaml`: `shipwick deploy`
builds the image on the developer's machine, where Docker already is, and
sends it straight to the agent, which loads it. No registry account, no
token, no `docker login` on the server, no public-or-private question. A
registry stays the right tool for CI and stays supported; it stops being a
prerequisite. The agent still builds nothing itself. A first version sends
the whole image; sending only the layers the server lacks comes after.

**`shipwick init` that knows the project.** It recognises what is in the
directory — a Node, Nuxt or Next application, a .NET or Go service, a folder
of static files — and writes a Dockerfile, a `.dockerignore` and a
`deploy.yaml` that work as they are. `static: dist/` serves a built frontend
straight from the proxy, with no container at all.

**Secrets kept on the server.** `shipwick secret set my-api DATABASE_PASSWORD`
stores a value on the server, encrypted like environment values are, and
`${DATABASE_PASSWORD}` in `deploy.yaml` is filled in by the agent. The
`--env-file` on every laptop and in every pipeline becomes optional.

**Installing the server from the laptop.** `shipwick server install
root@203.0.113.10 --domain example.com` connects over SSH, installs Docker if
it is missing, runs the installer, saves the token as a context, and prints
the DNS records to create. Nothing to type on the server.

**DNS spelled out.** The agent knows the server's addresses now; a deploy whose
hostname is not ready prints the exact record to add: name, type, address, and
that it must not be proxied. Cloudflare's own addresses are recognised, so the
message says "turn the proxy off" rather than "does not point here".

**Several applications in one file.** `apps:` in one `shipwick.yaml`, deployed
in dependency order with one command, so that a project with three services
has one file to read. `-f` repeated stays.

**Faster deployments.** Independent applications of one command deploy at the
same time, not one after the other; `after:` in the file says which must wait.
Image transfer sends only the layers the server does not have.

**Small things that remove a question.** `shipwick deploy` in a directory
without a `deploy.yaml` starts `init` instead of failing. `shipwick doctor`
checks DNS, ports 80 and 443, Docker, the token and the versions, and says
what is wrong in one screen. `shipwick open` opens the application in the
browser. A first deployment ends with what to do next: the address, the logs,
the dashboard.

**Compressed responses.** The proxy sends what the application sends; a 42 KB
page leaves as 42 KB. Caddy compresses with zstd and gzip when asked, and
leaves alone what the application already compressed. One line of
configuration, for every route.

**A GitHub Action.** `uses: shipwick/deploy@v1` with the agent's address, a
`deploy` token and the image just built: installs the CLI and runs `shipwick
deploy`. The same thing a pipeline does by hand today, in three lines.

**Volumes of deleted applications.** They stay, on purpose; today only
`docker volume rm` on the server removes one. `shipwick volumes` to list them,
with what they belonged to and how much they hold, and to remove one.

**A grace period for slow starters.** `health.start_period`: a JVM that needs a
minute should not have to say `retries: 12`.

**Rate limiting on the API.** A token is tried at most a few times a minute
from one address; today nothing slows a guess down.

## 0.5 — The proxy

What sits in front of the applications: Cloudflare, headers and
authentication, and what the proxy could tell about the traffic it carries.

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

## 0.6 — Running it for a year

The things an installation needs once it has been up for months: backups
that happen without anyone remembering, alerts before a limit is hit, keys
that can be rotated, and nothing that depends on the agent never restarting.

**Scheduled backups.** `backups` in `deploy.yaml`: a schedule, how many to
keep, and where to put them — a directory on the server first, an S3-compatible
bucket after. `shipwick backup` stays for the one you take by hand.

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

**The winget submission from the release workflow.** Today a release is
followed by two commands (`packaging/winget/update-manifests.sh`, then
`wingetcreate submit`) run by a maintainer. Doing it from the workflow needs a
GitHub token with write access to a fork of winget-pkgs stored in this
repository, which the Homebrew tap deliberately avoids; worth it once the
cadence makes the two commands a chore.

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

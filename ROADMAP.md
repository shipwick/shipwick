# Roadmap

What the current version does is in [README.md](README.md); what changed
between versions is in [CHANGELOG.md](CHANGELOG.md). This is what comes next,
in the order it is likely to ship. Nothing here is a promise; an item moves
when a real installation shows that something else matters more.

## 0.4

The first half hour is what 0.4 is about: a developer with two APIs, a
frontend and one server should get from nothing to three running applications
without opening an account anywhere else, without learning what a registry is,
and without being told to run something "on the server" without knowing which
one.

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

**Registry credentials the agent keeps.** `shipwick registry login ghcr.io`
stores a read-only token on the server, encrypted like environment values, and
the agent uses it when it pulls. Today a private image needs `docker login` on
the server and a mount in `compose.override.yml`. Credential helpers
(`credsStore`) in `~/.docker/config.json` are read as well.

**Key rotation.** `shipwick-agent rotate-key` re-encrypts every stored
environment value under a new key, without a stop.

**Scheduled backups.** `backups` in `deploy.yaml`: a schedule, how many to
keep, and where to put them — a directory on the server first, an S3-compatible
bucket after. `shipwick backup` stays for the one you take by hand.

**Alerts from metrics.** A replica near its memory limit, a disk filling up,
a health check failing for longer than a threshold: posted to the webhook like
deployment outcomes are.

**Certificate status.** `shipwick status` and the dashboard say when a
hostname is served but its certificate is still being obtained, and why.

## Later

**Several servers in one dashboard.** The CLI already switches between servers
with contexts; the dashboard should too, one sign-in per server.

**Roles per application.** A token that may deploy one application and read
the others.

**The API reachable only from the proxy and the dashboard.** Application
containers share the `shipwick` network with the agent, which needs it for
health checks, so today they can reach the API and try tokens against it. The
agent should answer only the proxy, the dashboard and the server itself.

**Log archiving.** A run's output and a replica's last log lines kept beyond
the container's life, searchable from the dashboard.

## Out of scope

Multi-node scheduling, building images, anything that needs an external
database or queue. Shipwick is for one server; when you outgrow that, you have
outgrown Shipwick, and that is fine.

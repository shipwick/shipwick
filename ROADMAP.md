# Roadmap

What Shipwick does is in [README.md](README.md) and, in full, in
[docs/handbook.md](docs/handbook.md); what changed between versions is in
[CHANGELOG.md](CHANGELOG.md); what a release may and may not change is the
compatibility promise in the handbook under
[Versions and compatibility](docs/handbook.md#versions-and-compatibility).
This page is what comes next. Minors are cut from what installations ask for,
in issues, against the principles in [CONTRIBUTING.md](CONTRIBUTING.md) and
the two lists at the end of this page; an item here moves when a real
installation shows that something else matters more.

## Next

**The usual services, written for you.** `shipwick init postgres` adds an
entry to `shipwick.yaml` that is right the first time: the volume at the path
this version of the image keeps its data in, `deploy.strategy: recreate`, a
health check that is not HTTP, a `backups` block whose `before` takes a
consistent dump, and the address other applications reach it at. The same for
Redis, MySQL and MariaDB, and no further: a handful of services every
application needs, each started in a container by the tests, not a catalogue
to keep up with. What is written is plain `deploy.yaml`; the agent learns
nothing new, and a database remains an application like any other.

**Capacity on rented servers.** The handbook's numbers for what one server
carries were measured on a developer's machine held to a server's size
([What one server carries](docs/handbook.md#what-one-server-carries)). The
same script on two sizes of cloud server adds a second column: the smallest
offer most people start on, and the one they move to.

## Later

A major version is the only release that may remove what a minor deprecated,
and it comes with a written upgrade. Nothing is planned for one; the
compatibility promise says why a 1.x release cannot be the place for it.

## Not planned

Asked for, considered, and declined. Unlike what is out of scope below, these
could change if real installations show the need; until then the answer is no,
and this is why.

**Blue/green and canary deployments.** Blue/green needs room for two full
copies of an application, which is what a small server does not have; rolling
replacement with health checks and automatic rollback is the strategy, and
`recreate` the exception for what cannot run twice. Canary releases need
traffic splitting and a way to judge the canary, for a benefit that shows at
a scale one server does not reach.

**Credential helpers.** A helper is a program on the server that the agent
would have to execute, and the agent executes nothing. Registries whose
credentials expire, such as Amazon ECR and Google Artifact Registry, are
served by piping the cloud CLI's token into `shipwick registry login` on a
schedule; the handbook has the commands.

**Automatic failover.** See *A second server kept ready* in the handbook for
what is offered instead, and *Scheduling across machines* below for why.

**User accounts, SAML, LDAP.** The dashboard accepts identities from an
OpenID Connect provider and keeps no passwords and no directory of its own. SAML and LDAP are reached through a
provider that speaks OpenID Connect, which every one of them does.

**Approval steps inside Shipwick.** A second person approving a production
deployment is a feature of the pipeline that calls `shipwick deploy`, where
the code review already happened.

**A built-in image scanner, and connectors to secret managers.** Each would be
a dependency to track and a product to keep up with. Scanning runs in the
pipeline; a secret manager supplies environment variables or the encryption
key at start-up. Both are documented rather than built in.

**Long-term support releases.** Only the latest release is supported, and a
fix — a security fix included — is the next patch of the latest minor. An
older minor kept alive would be a second line to fix, test and release, and
what makes it unnecessary is the compatibility promise: the upgrade to the
latest release is one step from any 1.x and relearns nothing. Decided again if real
upgrade data says otherwise.

## Out of scope

Three things are not on this list and will not be. They are the lines that
keep everything above simple; a request that crosses one of them is answered
with no, however good the request.

**Scheduling across machines.** Placing applications on several servers,
moving them when one fails, balancing replicas between machines. That is what
an orchestrator is for, and it costs cluster membership, discovery across
hosts, distributed state and split brains — the moment they arrive, "one
process and one SQLite file" is over. Managing several independent servers
from one CLI or one dashboard is a different thing, and it exists: contexts in
the CLI, `SHIPWICK_AGENTS` in the dashboard.

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

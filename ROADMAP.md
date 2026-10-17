# Roadmap

What the current version does is in [README.md](README.md); what changed
between versions is in [CHANGELOG.md](CHANGELOG.md). This is what comes next.
Nothing here is a promise except what *1.0* says; an item moves when a real
installation shows that something else matters more.

## 1.0

1.0 is a promise, not a feature. From it on, a `deploy.yaml` that worked
keeps working; the API under `/api/v1` keeps every path, field and code it
has; the data directory changes only by a migration the agent applies when
it starts; and a CLI of any 1.x version works against an agent of any other.
A patch fixes and a minor adds; nothing is removed or renamed before 2.0, and
what goes then is marked deprecated in the changelog of a minor first and
keeps working until then. Only the latest release is supported, and the
upgrade to it is one step from any 1.x. The whole policy, with what it does
not promise, is in the handbook under
[Versions and compatibility](docs/handbook.md#versions-and-compatibility);
what is frozen, surface by surface, and why, is in
[docs/architecture.md](docs/architecture.md#what-10-freezes).

## 1.x

A minor adds and removes nothing. What it adds is decided by what
installations ask for, in issues, against the principles in
[CONTRIBUTING.md](CONTRIBUTING.md) and the two lists at the end of this page.
What is already in view, in no order:

**Guides for the other pipelines.** The GitHub Action has a page; the same
three lines for a `.gitlab-ci.yml` and an `azure-pipelines.yml`, each run
against a real server before it is published.

**Coming from another tool.** The guide that exists for Docker Compose, for
the tools people most often arrive from: Dokku and Coolify. What each of their
concepts becomes here and how to move a running application across, without
a comparison of merits.

**`winget install Shipwick.Shipwick`.** The manifests of every release are in
[packaging/winget](packaging/winget) and the package is submitted to
winget-pkgs; when it is accepted, the handbook names it as the way to install
on Windows, and the submission moves from two commands run by a maintainer
into the release workflow.

**Numbers from more machines.** What one server carries is measured on a
developer's machine at a server's size; the same script on rented servers of
two sizes puts a second column next to the first. The packages and rootless
Docker, run in containers today, on hosts that enforce SELinux or AppArmor;
the CLI started on Windows on Arm, where it is built and chosen but has not
yet been run; the dashboard used for an hour by someone who works with a
screen reader. Each result goes into the handbook as a number or a
correction, and is welcome as an issue from anyone who has the machine.

**A security review from outside.** Before 1.0 the code was reviewed three
times against its own threat model — the Docker Engine API calls the agent
makes, the proxy configuration it generates, the handling of tokens, secrets
and uploads — and the findings were fixed; the changelog's 0.10.0 section
says what. The next review is by someone who did not write the code, and its
summary is published with the release that carries its findings.

## 2.x

Not before 1.0 has been out long enough to say what 2.0 is for. 2.0 is the
first release that may remove what a 1.x deprecated, and it comes with a
written upgrade. One item is already known.

**The usual services, written for you.** `shipwick init postgres` adds an
entry to `shipwick.yaml` that is right the first time: the volume at the path
this version of the image keeps its data in, `deploy.strategy: recreate`, a
health check that is not HTTP, a `backups` block whose `before` takes a
consistent dump, and the address other applications reach it at. The same for
Redis, MySQL and MariaDB, and no further: a handful of services every
application needs, each started in a container by the tests, not a catalogue
to keep up with. What is written is plain `deploy.yaml`; the agent learns
nothing new, and a database remains an application like any other.

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
what makes it unnecessary is the promise above: the upgrade to the latest
release is one step from any 1.x and relearns nothing. Decided again if real
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

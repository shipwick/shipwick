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

## Not yet run

1.0 ships without these. Each is a place where Shipwick was built and tested
in a container, or checked by a tool, and has not been used on the machine or
by the person the handbook paragraph is about; the paragraph says so and says
what was done instead. None is a feature and none is promised: each needs a
machine or a person that was not at hand, and what one shows goes into the
handbook as a number or a correction. If you have the machine, the result is
welcome as an issue.

**Rootless Docker on a real server.** Run in containers, alone and behind
the socket proxy, with a daemon under systemd and without one. Not run: a
server that reboots, SELinux or AppArmor enforcing, and `slirp4netns` as the
port driver that keeps a client's address. Needs a server or a virtual
machine of its own with a rootless daemon, and a day of use. Handbook:
[Rootless Docker](docs/handbook.md#rootless-docker), *Not covered*.

**Packages on a hardened host.** The `.deb` and the `.rpm` install and run
on Debian and Rocky Linux in containers, where nothing enforces. With SELinux
enforcing they were not run: the handbook's paragraph is reasoned from the
policy's types and names the two denials it expects and how to lift them;
`ausearch` on a host that enforces would confirm or correct it. The same for
a host firewall between Docker's networks and the bridge address. Handbook:
[Installation from a package](docs/handbook.md#installation-from-a-package),
*SELinux*.

**Windows on Arm.** The build exists, the Windows installer picks it and
`shipwick upgrade` chooses it; it has been compiled and never started on
such a machine. Needs one, and `shipwick --version`, `login` and `deploy`
run from it. Handbook: [Installation](docs/handbook.md#4-installation), the
CLI on Windows.

**The limits on a rented server.** What one server carries was measured in a
container held to a server's size on a developer's machine: in full at two
processors and four gigabytes, and at twice that the requests and the
restores only. Its disk and its network are not a server's. Needs
`scripts/measure-limits.sh` run on the smallest and on a mid-sized virtual
server, and the numbers written next to the ones there. Handbook:
[What one server carries](docs/handbook.md#what-one-server-carries).

**The dashboard, listened to.** Every page works with the keyboard, passes
an automated check of names, roles and contrast, and says what its tooltips
say without a pointer. Nobody who works with a screen reader every day has
used it. Needs such a person and an hour; what they find is fixed in the
dashboard. Handbook: [Dashboard](docs/handbook.md#dashboard), *Without a
mouse*.

## Loose ends

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

## Looked at by someone else

**An independent security review.** Before 1.0 the code was reviewed three
times against its own threat model — the Docker Engine API calls the agent
makes, the proxy configuration it generates, the handling of tokens, secrets
and uploads — and the findings were fixed; the changelog's 0.10.0 section
says what. A reviewer from outside is still wanted: the same ground, examined
by someone who did not write it, the findings fixed, and a summary published
with the release.

## Documentation, as it becomes true

Not tied to a version; written when someone needs them.

**GitLab CI and Azure Pipelines.** The GitHub Action has a page; the same
three lines for a `.gitlab-ci.yml` and an `azure-pipelines.yml`, each run
against a real server before it is published.

**Coming from another tool.** The same guide as for Compose, for the tools
people most often arrive from: Dokku and Coolify. What each of their concepts
becomes here and how to move a running application across, without a
comparison of merits.

## 1.x

A minor adds and removes nothing. What it adds is decided by what
installations ask for, in issues, against the principles in
[CONTRIBUTING.md](CONTRIBUTING.md) and the two lists at the end of this page.
No minor is named here yet: what is above is verification and documentation,
not features, and goes into whichever release it is done for.

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

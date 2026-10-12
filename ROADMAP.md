# Roadmap

What the current version does is in [README.md](README.md); what changed
between versions is in [CHANGELOG.md](CHANGELOG.md). This is what comes next,
one theme per minor version, in the order it is likely to ship. Nothing here
is a promise; an item moves when a real installation shows that something
else matters more.

## 0.9 and 0.10 — Stability and ease, then the last look before 1.0

0.9 took what a real installation — a bare server to a live application, by
hand, every mistake written down — taught, and what 0.8 left open, and built
the answers. 0.10 followed with three security reviews of the code and their
fixes, the agent at half the processor time it spent watching applications,
the API's names and lists made consistent before they are frozen, and every
document read against the code; the changelog lists both. What is below
could not be done from where they were built: each needs a machine, a person
or a review that was not at hand. None is a feature; 1.0 is cut when they are
done or dropped on purpose.

### Run where it has not run

**Rootless Docker on a real server.** It was run in containers. A server
that reboots, SELinux or AppArmor enforcing, and the port driver that is said
to keep the client's address were not tried; the handbook says so.

**Packages on a hardened host.** The `.deb` and the `.rpm` are built and
installed on Debian and Rocky Linux by `scripts/test-package.sh`, with the
API's network of its own. With SELinux enforcing they were not run: the
handbook's paragraph on it is reasoned from the policy's types and says so,
and wants a host that enforces. The same for a host firewall between Docker's
networks and the bridge address.

**Windows on Arm, run.** The build exists and `shipwick upgrade` chooses it;
it has been compiled and never started on such a machine.

**The limits on a rented server.** What one server carries was measured in a
container held to a server's size on a developer's machine, in full at two
processors and four gigabytes; at twice that, the requests and the restores
only. Its disk and its network are not a server's: the same script on the
smallest and on a mid-sized virtual server, and the numbers next to the ones
in the handbook.

**The dashboard, listened to.** Every page works with the keyboard, passes an
automated check of names, roles and contrast, and says what its tooltips say
without a pointer. Nobody has used it with a screen reader.

### Loose ends

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

### Looked at by someone else

**An independent security review.** The agent's use of the Docker Engine API,
the proxy configuration it generates, and the handling of tokens, secrets and
uploads, examined by an outside reviewer; the findings fixed, and a summary
published with the release.

## Documentation, as it becomes true

Not tied to a version; written when someone needs them.

**GitLab CI and Azure Pipelines.** The GitHub Action has a page; the same
three lines for a `.gitlab-ci.yml` and an `azure-pipelines.yml`, each run
against a real server before it is published.

**Coming from another tool.** The same guide as for Compose, for the tools
people most often arrive from: Dokku and Coolify. What each of their concepts
becomes here and how to move a running application across, without a
comparison of merits.

## 1.0

1.0 is a promise, not a feature: from then on `deploy.yaml`, the API and the
on-disk format change only with a migration and a documented upgrade, and a
CLI of one 1.x version works against an agent of any other. It is cut when
the 0.x series has run real installations for long enough that the last two
minor versions changed nothing anyone had to relearn, when every item above
has either shipped or been dropped on purpose, and when the limits of one
server are written down with numbers that were measured.

## 2.x

Not before 1.0 has been out long enough to say what 2.0 is for. One item is
already known.

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

**Long-term support releases.** Before 1.0 there is one supported version, the
latest. What 1.0 promises is under *1.0* above; a support policy for older
1.x versions is decided then, with real upgrade data.

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

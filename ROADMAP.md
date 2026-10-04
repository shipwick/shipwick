# Roadmap

What the current version does is in [README.md](README.md); what changed
between versions is in [CHANGELOG.md](CHANGELOG.md). This is what comes next,
one theme per minor version, in the order it is likely to ship. Nothing here
is a promise; an item moves when a real installation shows that something
else matters more.

## 0.9 — What hardening left open

0.8 went looking for what the earlier releases got wrong, found it and fixed
it. What it could not finish from where it was done is here: things that need
a machine, a person or a release that 0.8 did not have. No new features.

### Run where it has not run

**The release workflow, signing.** Releases are signed without a key, carry
bills of materials and provenance, and the installer and the CLI verify the
signature when `cosign` is there. The workflow that does it was linted and
its parts were tried by hand; it had not run when it was written. The first
release candidate is its test, and `docs/releasing.md` says what to watch.

**The matrix in CI.** The installer on ten distributions and the integration
tests on eight Docker versions passed on one amd64 machine. The same on
GitHub's runners, arm64 included, has not run once.

**Rootless Docker on a real server.** It was run in containers. A server
that reboots, SELinux or AppArmor enforcing, and the port driver that is said
to keep the client's address were not tried; the handbook says so.

**Packages on a hardened host.** The `.deb` and the `.rpm` were installed
and run on Debian and Rocky Linux. With SELinux enforcing, or with a host
firewall between Docker's networks and the bridge address, they were not;
nor were they built and installed with the network of its own that the API
has since 0.8.

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

**A socket proxy that is a boundary.** The proxy in front of the Docker
socket allows the calls the agent makes and no others, and refuses host
mounts. It cannot look into the body of a request that creates a container,
so a privileged one passes; no maintained proxy can. Docker's authorization
plugins can, and were not tried.

**An API that is closed on every Docker.** With the API's port published on
a Docker before 28, the agent has to listen where applications can reach it,
and says so. Either a way to keep the port without that, or the published
port gone from what the installer writes.

**A proxy on both its networks, checked.** A proxy container whose start was
refused once can come back on one of its networks only, and then serves
nothing for some applications. `shipwick doctor` should see it.

**An export an older agent reads in full.** An agent before 0.8 importing an
export from 0.8 drops the `security` block without a word, because an
unknown key in the file is skipped. The next change to the export's format
should make an older agent refuse what it cannot keep.

**A full disk and a rollback.** While the agent's database cannot be written,
a rollout that fails half-way is not rolled back, because the rollback
cannot be recorded: the old version runs short of the replicas already
replaced until there is room.

**A supervisor whose pass costs the same at any size.** Every second the
agent asks Docker for the list of all containers three times and for each
application's once more. At ten applications that is nothing; from about a
hundred, on two processors, the pass no longer fits in its second, the
server idles at one and a half processors, and a replica that died is away
for nine seconds instead of three. One list per pass, shared.

**Hostnames looked up side by side.** Whether each hostname still points at
the server is asked one hostname after the other, in the path of a rollout.
With a resolver that answers in a millisecond it costs nothing; with one
that takes 180 ms and two hundred hostnames, a rolling deployment took 57
seconds.

**A server near the kernel's limit, said.** Between 300 and 400 containers
the kernel's table of neighbours fills and containers stop reaching each
other. The handbook names the setting; `shipwick doctor` should say when a
server is close.

**The second and a half.** Three applications deployed together take about a
second and a half longer than one alone. It is not the rest of addresses;
what it is has not been found.

**The editor says what it masks.** A value changed in the dashboard's
configuration editor is masked from then on, until the CLI deploys the file
again. The page should say so before the deployment, not after.

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

**From Docker Compose.** A guide for the commonest starting point: a
`docker-compose.yml` on a server, and what each of its parts becomes in
`deploy.yaml` and `shipwick.yaml`.

**GitLab CI and Azure Pipelines.** The GitHub Action has a page; the same
three lines for a `.gitlab-ci.yml` and an `azure-pipelines.yml`, each run
against a real server before it is published.

**Where your data is.** One page for whoever has to answer a security
questionnaire: what the agent stores and where, what is encrypted and with
which key, what leaves the server (nothing, unless a webhook is set), and
what a backup contains.

**Coming from another tool.** The same guide as for Compose, for the tools
people most often arrive from: Dokku and Coolify. What each of their concepts
becomes here and how to move a running application across, without a
comparison of merits.

**Staging and production.** Two servers are two contexts, and promoting a
version is deploying the image that ran on one to the other:
`shipwick --context production deploy --image <the one staging runs>`. An
approval before production is the pipeline's to ask for (a protected
environment in GitHub or GitLab); the page shows both.

**Scanning an image before it is deployed.** A scanner belongs in the
pipeline, before `shipwick deploy`, where its result can stop the job: a
worked example with Trivy next to the GitHub Action.

**Keeping the encryption key off the server's disk.** `SHIPWICK_ENCRYPTION_KEY`
already lets the key come from the environment; a page on supplying it from a
secret manager at start-up, and what that does to an unattended reboot.

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

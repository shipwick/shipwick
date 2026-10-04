# Roadmap

What the current version does is in [README.md](README.md); what changed
between versions is in [CHANGELOG.md](CHANGELOG.md). This is what comes next,
one theme per minor version, in the order it is likely to ship. Nothing here
is a promise; an item moves when a real installation shows that something
else matters more.

## 0.8 — Hardening

No new features. The release before 1.0 is for finding out what the earlier
ones got wrong: stability under failure, the last checks, and security looked
at by someone other than the people who wrote it.

### Left over from 0.7

Small things 0.7 says in its documentation rather than hides. None of them is
a feature; each is a loose end of one that exists.

**A configuration that comes back whole.** `shipwick config` returns
references to stored secrets as references and every other value of `env` as
a mask, a log level as much as a password: the agent cannot tell which of the
values it was sent were secret. The CLI knows which ones it filled in, and
should say.

**The dashboard, listened to.** Every page works with the keyboard and passes
an automated check of names, roles and contrast. Nobody has used it with a
screen reader, and what a tooltip explains — an absolute time, why a button
is disabled — is still out of reach without a pointer.

**Packages on a hardened host.** The `.deb` and the `.rpm` were installed
and run on Debian and Rocky Linux. With SELinux enforcing, or with a host
firewall between Docker's networks and the bridge address, they were not.

**Windows on Arm, run.** The build exists and `shipwick upgrade` chooses it;
it has been compiled and never started on such a machine.

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

### Hardening

**What happens when things break, written down and tested.** A full disk, a
Docker daemon that stops answering, a network that drops in the middle of a
pull, an agent killed half-way through a rollout: each with a test that
produces it, the behaviour that test pins down, and a page that says what the
operator sees and does. Some of this is tested today; none of it is in one
place.

**More than one distribution in CI.** The integration tests run on one Ubuntu
image with one Docker version. A matrix over the distributions the installer
claims to support and the Docker versions still in use, on amd64 and arm64.

**Every upgrade path, walked.** From each 0.x release to the current one on a
server with real applications and data: the migrations, the proxy's
configuration, the volumes, the secrets. An upgrade that needs a manual step
gets it written down or removed.

**The limits, measured.** How many applications, replicas and requests per
second one server of a given size carries before deployments slow down or the
proxy does; how large the database grows in a year; how long a restore takes.
Numbers from tests anyone can run, in the documentation, with the point at
which the honest answer is "you have outgrown Shipwick".

**The API reachable only from the proxy and the dashboard.** Application
containers share the `shipwick` network with the agent, which needs it for
health checks, so today they can reach the API and try tokens against it. The
agent should answer only the proxy, the dashboard and the server itself.

**Less than the whole Docker socket.** The agent holds the Docker socket,
which is root on the server. Running it against a socket proxy that allows
only the calls it makes, and against rootless Docker, each documented and
tested, for installations where that matters more than convenience.

**Containers locked down further, on request.** Replicas already run without
new privileges, without host mounts and without ports they did not ask for.
`security` in `deploy.yaml` for the rest: a read-only root filesystem,
dropped capabilities, a refusal to run as root.

**Signed releases.** Every binary, package and image signed at release time, with a
software bill of materials and build provenance, so that what a server runs as
root can be traced to a commit in this repository.

**An independent security review.** The agent's use of the Docker Engine API,
the proxy configuration it generates, and the handling of tokens, secrets and
uploads, examined by an outside reviewer; the findings fixed, and a summary
published with the release.

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

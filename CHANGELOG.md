# Changelog

Notable changes to Shipwick. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/). Before 1.0, a minor version may
change the API, `deploy.yaml` or the on-disk format; when it does, the entry
says so under **Changed** and explains how to upgrade.

## [Unreleased]

## [0.6.0] - 2026-10-04

### Added

- A `deploy` token can be limited to applications: `shipwick token create ci
  --role deploy --app my-api --app web`. It deploys, stops, starts and runs
  commands in those, reads everything, and is refused anything else with
  `403 TOKEN_LIMITED` and a message that names its applications.
- Tokens can expire: `--expires 90d`, `12h` or a date. An expired token is
  refused with `401 TOKEN_EXPIRED` and a message that says when it expired;
  `shipwick token ls` shows each token's expiry and marks what expires within
  14 days, and `shipwick doctor` and `shipwick server status` say when the
  token they run with does. The root token does not expire.
- An audit trail: every request that changes something is recorded with the
  token, the time, the address, what it was about and how it was answered,
  refusals included. `shipwick audit [--app] [--actor] [--since] [-n]` and
  `GET /audit` (admin) read it; it is kept for a year.
- `GET /server` → `token` carries `kind`, `applications` and `expires_at`,
  so that a client knows what its token may do before it tries.
- **Signing in to the dashboard with the company's accounts.** With an OpenID
  Connect provider configured on the agent — `SHIPWICK_OIDC_ISSUER`,
  `SHIPWICK_OIDC_CLIENT_ID`, `SHIPWICK_OIDC_CLIENT_SECRET`, and optionally
  `SHIPWICK_OIDC_SCOPES` and `SHIPWICK_OIDC_GROUPS_CLAIM` — a person signs in
  at Google Workspace, Microsoft Entra, Okta, Keycloak or any other such
  provider, and a table on the agent says what they may do: `shipwick access
  grant ada@example.com --role admin`, `group:backend --role deploy --app
  my-api`, `'*@example.com' --role read`; `shipwick access ls`, `revoke`,
  `sessions` and `signout`. The most specific rule decides — address, then
  groups, then domain — and nobody without one gets in. A session lasts ten
  hours, is the same kind of caller a token is (`kind: "user"` in
  `GET /server` and in the audit trail, which records sign-ins too), and
  ends with its next request when the rule it rests on is revoked or
  changed. The flow is the authorization code with PKCE; the client secret
  stays in the agent. Shipwick keeps no passwords and no users; tokens work
  as before. API: `GET /auth`, `POST /auth/exchange`, `DELETE /auth/session`,
  `/access/rules`, `/access/sessions`; new codes `SIGN_IN_NOT_CONFIGURED`,
  `SIGN_IN_FAILED`, `SIGN_IN_UNAVAILABLE`, `ACCESS_NOT_GRANTED`,
  `SESSION_EXPIRED`, `SESSION_ENDED`.
- The dashboard signs people in through the agent's OpenID Connect provider:
  a button above the token field, and under *Access* the rules that give an
  address, a group or a domain a role, and who is signed in now.
- The dashboard's *Access* page creates tokens for some applications and
  tokens that expire, lists both, and reads the audit trail with its filters.
  What a token may not do on an application is said on that application's
  page, per application; a token that expired, or a session that was ended,
  returns to the sign-in page with the reason.
- **A new application from the dashboard.** *New application* takes a pasted
  `deploy.yaml`, has the agent check it, lists every field the agent refuses
  with what it expects, and deploys it when it is in order; the same page
  deploys a changed configuration of an existing application. For an image in
  a registry: a document with `build:` or `static:` is told that it is
  deployed with `shipwick deploy` from the project.
- **Several servers in one dashboard.** `SHIPWICK_AGENTS` on the dashboard
  lists servers as `name=URL` pairs. Each has its own sign-in; the box under
  the logo switches between them, `/servers` lists them, and every address
  carries its server (`?server=staging`), so a shared link opens on the
  server it was copied from. With `SHIPWICK_AGENT_URL` alone nothing changes.
- The dashboard follows a standby's promotion as it runs, marks applications
  that have an alert or a certificate problem in its lists, shows a container
  that is stopping as such from the agent's own word, names the proxy and
  the other network settings on the server's page, and adopts backups.
- The dashboard's server passes the browser's address to the agent as
  `X-Forwarded-For`, so that the audit trail names who asked and not the
  dashboard.
- Behind a corporate proxy: the agent's requests to the webhook and the bucket
  go through `HTTPS_PROXY` / `HTTP_PROXY` (unless `NO_PROXY` covers the host),
  and so do Caddy's to the certificate authority and to Cloudflare. Both
  compose files pass the three variables on, and the installer uses them and
  keeps them in `.env`. Names on the server's own networks, health checks and
  requests to replicas never go through the proxy. A proxy that refuses is
  named in the log with the destination and its answer; a password in the
  proxy's URL is never logged.
- `SHIPWICK_CA_FILE`: certificate authorities of your own, trusted by the
  agent and by `shipwick` in addition to the system's. The agent checks the
  file when it starts and says what is wrong with it.
- `SHIPWICK_ACME_DIRECTORY`: Caddy obtains certificates from an ACME server of
  your own instead of Let's Encrypt.
- `SHIPWICK_DNS_RESOLVERS`: who is asked whether a hostname points at the
  server — `system`, or name servers by address.
- `shipwick server bundle` makes one file that installs or upgrades a server
  with no connection: the release's compose file, installer and checksums,
  the CLI and the three images. `install.sh --bundle <file>` installs from it
  without a download or a pull. The release publishes `install.sh`.
- A pull that fails on the way to the registry says that images are pulled by
  the Docker daemon, and what to set in `/etc/docker/daemon.json` or
  `/etc/docker/certs.d`. `GET /server` reports the agent's proxy next to the
  daemon's (`network`); `shipwick doctor` and `shipwick server status` show it.
- **Backups larger than 5 GB to a bucket.** An archive of more than 64 MiB,
  a scheduled export included, goes to the bucket as a multipart upload, in
  parts read from the file on the server; the limit is now the service's own
  for one object, 5 TiB on S3. An upload that fails or is interrupted is
  aborted, and what an agent did not live to abort is aborted by the next
  one before its first upload, so no parts are left to be paid for.
- **`shipwick backups adopt [app]`** records the backups that the directory
  and the bucket hold and the database does not know: after the agent's
  state was restored from a backup of it, the backups taken since. They are
  listed with the trigger `adopted` and can be verified, restored, downloaded
  and pruned like any other. `POST /server/backups/adopt`, `admin`.
- **`backups.before_timeout`** in `deploy.yaml`: how long `backups.before`
  may run, 1s–24h, one hour by default as before. A backup whose command
  runs past it fails with an error that names the key.
- A promotion can be followed. `shipwick standby promote` starts it and
  prints each application as it comes up; the promotion runs on the server
  and has a record (`GET /standby/promotion`, and `promotion` in
  `GET /standby`). A lost connection no longer loses the answer, an agent
  that is restarted in the middle goes on with the promotion where it was,
  and the command run again follows the one that is under way.
  `POST /standby/promote?wait=false` answers `202` at once; without the
  parameter the request is held and answered as before, which is what a
  `shipwick` before 0.6 expects. A second promotion, or an import, while one
  runs is refused with `409 PROMOTION_IN_PROGRESS`.
- A standby remembers what it imported across restarts of its agent: the
  newest export is no longer imported once more after every restart, and
  `shipwick import --status` and `shipwick standby` answer as before it. An
  import the agent was restarted under is shown as failed.
- An import validates the configuration of every application by all the
  rules a `deploy.yaml` is held to. Health paths, the `proxy` block,
  `publish` and logging options were taken as the export had them.
- **An init process, on request.** `init: true` in `deploy.yaml` runs Docker's
  init process as the first process of every container of the application —
  replicas, the pre-deploy hook, jobs, one-off commands — so that a process
  without a handler for `SIGTERM`, such as Node started as `node server.js`,
  ends when it is asked to instead of being killed when its grace period is
  over: the same ten-line Node server stopped in 0.3 seconds with it and in
  10.4 without. Not the default, and not for images that bring their own
  init. Refused for a static application. `shipwick init` writes it for a Node
  project whose Dockerfile it writes, and the event about a container that had
  to be killed names it — an event `shipwick stop` now writes too.
- A container that is being stopped after a deployment replaced it carries
  `stopping: true` in `GET /applications/:name`, and `shipwick status` lists
  it as `stopping`, below the replicas.
- `GET /applications` says of each application whether a certificate is not
  in order (`certificate_problem`: the hostname, its status and message) and
  how many alerts about it are active (`alert_count`, `alert_severity`), and
  `shipwick ps` ends the line of such an application with a few words:
  `certificate waiting for DNS, 2 alerts`.

### Changed

- Dashboard: an application's page is tabs with addresses of their own —
  Overview, Metrics, Logs, Deployments, Jobs, Backups, Configuration — instead
  of one long page. The overview tab says first what is wrong and where to
  look: a replica that keeps crashing, a deployment that failed while the old
  version still runs, a certificate that is waiting for DNS.
- Dashboard: the server's page is tabs as well (Status, Backups, Export and
  standby, Encryption key), and the overview opens with the answer to "is
  everything fine?" and, on an empty server, the three commands that deploy
  the first application.
- Dashboard: the navigation is grouped into applications, server and
  settings. Tokens are under *Access* (`/settings/access`); secrets and
  registries moved to `/settings/secrets` and `/settings/registries`. The old
  addresses (`/tokens`, `/secrets`, `/registries`) redirect.
- Dashboard: a read token is told on the page that it cannot change anything,
  traffic numbers are named in words, and a deployment's timeline names its
  phases the way the rest of the dashboard does.

### Fixed

- The installer no longer untags the images that are in use when it removes
  those of earlier releases. Its list of images to keep never matched, and
  only Docker's refusal to remove an image a container runs on protected
  them; after an install from a bundle that refusal did not apply, and the
  next `docker compose up` on a server without a way out had nothing to start
  from.
- A hostname under `redirects` of an application with a `path` redirects to
  the domain and that path — `https://example.com/api/users` for a request
  for `/users` — instead of to the same path on the domain, which may belong
  to another application.
- The agent finds the proxy's container in the compose project it runs in
  itself, whatever that project is called. An installation started under
  another project name (`docker compose -p`, `COMPOSE_PROJECT_NAME`) had no
  traffic figures, and its static applications failed to deploy.
- On a server whose firewall drops DNS to the public resolvers, no hostname
  was ever routed: the first resolver used up the whole lookup and the
  server's own was never asked. Each now has a share of the time, and
  resolvers that do not answer are left alone for five minutes.
- With `HTTP_PROXY` in Caddy's environment, requests to applications went to
  that proxy. They never do now.
- An application that was being imported stopped when the agent restarted
  is no longer started by the deployment that resumes: an application that
  was stopped where it came from stays stopped, and one on a standby is
  served after its promotion.
- The volumes a backup verification restores into are removed after an agent
  restart interrupted it. They were swept before the container that mounted
  them was gone, and stayed.

## [0.5.1] - 2026-10-03

### Changed

- `shipwick doctor` and `shipwick server status` count what the proxy serves
  as routes, not domains: two applications may share a hostname by path.
- A registry password that is too large is told the limit, not its size.

### Fixed

- Starting a stopped application — with `shipwick start`, or by promoting a
  standby — no longer sends `application.down` and `application.recovered`
  for the second its first health check takes. An application that was
  started and does not come up is still reported down.
- `shipwick backups download -o <dir>` creates the folder instead of failing
  when it does not exist.
- `shipwick doctor` no longer says that Caddy is still obtaining a certificate
  for a hostname served with one you supplied: it says that this machine does
  not trust it. A hostname two applications share by path is looked up once.
- The agent names the basic-auth accounts it has hashed by a keyed digest
  held in memory, and checks the names that become paths under its upload
  directory where the paths are made.

## [0.5.0] - 2026-10-03

### Added

- **Cloudflare in front of the server.** With `SHIPWICK_CLOUDFLARE_API_TOKEN`
  on the agent — a Cloudflare API token with *Zone → Zone → Read* and
  *Zone → DNS → Edit* — Caddy obtains certificates through a DNS record
  instead of from the server itself, so a hostname's record can stay proxied
  (the orange cloud). A hostname that resolves to Cloudflare is then served
  instead of held back, and Cloudflare's address ranges are trusted proxies,
  so applications find the visitor's address in `X-Forwarded-For`. Set the
  zone's SSL/TLS mode to *Full (strict)*. Give the variable to the installer,
  or add it to `/opt/shipwick/.env` and run `docker compose up -d` there.
  Without it nothing changes, except that the message for a proxied record
  now names the variable. Handbook §11, *Behind Cloudflare*.
- **Wildcard hostnames.** `domain` and `aliases` may be `"*.example.com"`.
  A deployment that names one is refused unless the agent has the Cloudflare
  token or a certificate of your own covers it. `api.example.com` and
  `*.example.com` may belong to different applications; the exact name wins.
- **A certificate of your own.** `shipwick cert set example.com --cert
  fullchain.pem --key privkey.pem` serves the hostnames a certificate covers
  with it instead of one Caddy obtains; `cert ls` shows issuer, expiry and
  names, `cert rm` goes back to automatic certificates. The agent checks the
  chain, the key and the hostname before storing them and says why it
  refuses; the key is encrypted in the database and never returned. API:
  `GET /certificates`, `PUT` and `DELETE /certificates/:hostname`; a new
  error code, `INVALID_CERTIFICATE`.
- **The proxy is Shipwick's own build of Caddy**, `ghcr.io/shipwick/caddy`:
  Caddy 2.11.6 with the Cloudflare DNS module and nothing else, pinned to the
  release like the agent and the dashboard (`SHIPWICK_CADDY_IMAGE` overrides
  it). The upgrade replaces the Caddy container, so the proxy is away for the
  moment that takes; certificates and configuration are on volumes and stay.
- `shipwick doctor` names Cloudflare's proxy when a record points at it, in
  the agent's words, and accepts it when the agent has the Cloudflare token.
  It also reports a supplied certificate that has expired or will within 30
  days.
  `GET /server` reports that as `proxy.dns_challenge`.
- The CLI on the server knows its own agent. With a hostname for the API, the
  installer saves the URL and the token as a context of the user who runs it,
  through `shipwick login --token-stdin --no-check`, so `shipwick ps` works on
  the server as it does on a laptop. Run again, it puts the current token back
  into a context that points at this server and leaves every other context,
  and which one is current, as they were. Without a hostname nothing is saved,
  and a command that cannot reach the agent there no longer suggests an SSH
  tunnel: it says the agent publishes no port and how to give it a hostname.
- `shipwick login --no-check` saves the URL and the token without asking the
  agent, for a hostname whose DNS record or certificate does not exist yet.
- `GET /server` reports `dashboard_url`. `shipwick open --dashboard` opens it,
  and so does `shipwick open` in a directory without a `deploy.yaml`;
  `shipwick server status` shows it, and an application's first deployment
  ends with it next to the two commands to run next.
- `status`, `logs`, `stop`, `start`, `rollback`, `redeploy`, `run`, `jobs`,
  `open`, `backup`, `backups` and `restore` take the application's name from a
  `shipwick.yaml` when there is no `deploy.yaml`: its one application, or,
  when it describes several, an error that lists them and shows the command
  with a name.
- `shipwick init [dir]` in a directory with a `shipwick.yaml` appends an entry
  to it (`build: ./<dir>`, or `static: <dir>/dist/`) and writes the Dockerfile
  into `dir`, instead of writing a `deploy.yaml` next to it. The file's
  comments and formatting are kept; where the entry cannot be placed with
  certainty, the file is left alone and the entry is printed.
- `shipwick init` recognises SvelteKit (`adapter-node` as a server,
  `adapter-static` as a folder), Remix, Astro (with the Node adapter as a
  server), Next.js with `output: "export"` (the folder `out/`) and Python
  projects locked with uv (`uv sync --frozen`). A Node project without a lock
  file is told that its build is not reproducible until one is committed.
- `build: {dockerfile: docker/Dockerfile.prod}` without `context` builds in
  the directory of `deploy.yaml`; it was an error.
- `shipwick deploy` asks the agent to validate the file before it builds an
  image or uploads a folder, so that a port already published or a secret
  that is not stored is reported before the work, not after it. An older
  agent is asked about the domain only, as before.
- In a terminal, `docker build` is one progress line with the time and the
  last line it printed; its whole output is shown when it fails, or as it
  runs with `shipwick deploy --verbose`. Off a terminal nothing changes.
- `shipwick logs -f` says, after two seconds without a line, that it is
  following and that Ctrl-C stops it.
- `shipwick deploy` with `build:` sends only the layers the server does not
  have. The first deployment sends the whole image; after that a change to the
  application costs its own layer: `✓ Sent image to the server (22 KB; the
  server had the rest of 57.9 MB)`. The CLI asks the agent
  (`POST /applications/:name/images/missing`) and leaves the other layers out
  of the archive; with an older agent, or whenever the reduced archive cannot
  be built or is refused (`409 IMAGE_INCOMPLETE`), the whole image is sent as
  before. Works with the classic and the containerd image store, on either
  side.
- Alerts: a replica at 90% of its memory limit, the server's disk 85% full
  (critical at 95%), a replica restarted three times within ten minutes, and
  an application that has not been healthy for five minutes (again after an
  hour). Each is raised once and cleared once, on the webhook as
  `alert.raised` and `alert.cleared`, in the application's events, and as
  `alerts` in `GET /server`; `shipwick server status` and `shipwick doctor`
  print the active ones. The thresholds are `SHIPWICK_ALERT_MEMORY_PERCENT`
  and `SHIPWICK_ALERT_DISK_PERCENT`.
- `GET /server` and `shipwick server status` report how full the server's
  disk is.
- `GET /metrics` on the agent, in the Prometheus text format, for a `read`
  token: application status and replicas, CPU, memory and restarts of each
  replica, deployments by outcome, the disk and the active alerts.
- **Registry credentials the agent keeps.** `shipwick registry login ghcr.io
  --username octocat` stores a credential on the server, encrypted like a
  secret, and the agent sends it with every pull from that registry:
  deployments, rollbacks, jobs. The password is asked without echo or piped
  in, never an argument, and the agent checks it against the registry before
  storing it. `shipwick registry ls` and `registry logout` complete the set;
  the API is `GET /registries`, `PUT` and `DELETE /registries/:registry`.
  `docker login` on the server with a mount in `compose.override.yml` keeps
  working for registries without a stored credential. A pull refused for
  authentication now fails the deployment with the command to run.
- **Key rotation.** `shipwick server rotate-key` (`POST /server/rotate-key`)
  has the running agent generate a new encryption key and re-encrypt every
  stored env value, secret and registry password under it, without a
  deployment or a restart, and in an order that survives a crash at any
  point. With the key in `SHIPWICK_ENCRYPTION_KEY`, the new key is printed
  once for you to put into `/opt/shipwick/.env`.
- A job or one-off command whose image has been pruned from the server pulls
  it again, as a replica does, instead of failing.
- `path` in deploy.yaml: an application serves one path of its domain and
  everything under it, so that several applications share a hostname —
  `example.com/api` from one, the rest from another. The longest path wins.
  Each keeps its own replicas, health checks and rollouts. A hostname is now
  taken per path; the same path twice is refused.
- `proxy` in deploy.yaml: response headers, basic authentication for the
  application or a path of it, redirects from one path to another, and
  `strip_prefix` to remove `path` before the application sees the request.
  A basic-auth password is a secret like an `env` value: `${NAME}` is filled
  in from the environment, `--env-file` or `shipwick secret set`, it is
  encrypted at rest and never shown, and the proxy is given a bcrypt hash.
- `static: {dir, fallback}`: a static application answers the paths that name
  no file with a page of its folder, as a single-page application needs.
  `shipwick init` writes it for a project built by Vite.
- `shipwick ps`, `status`, `open` and the last line of `deploy` show an
  application's address with its path.
- An application that was a folder and is deployed as a container no longer
  leaves its folders in the proxy until it is deleted: the folder it replaced
  stays while a rollback would return to it, and goes with the next
  container version.
- `shipwick traffic`: what the proxy saw. Without an application, a table of
  every application's requests per minute, 5xx, 95th percentile and bytes
  over the last hour; with one, its totals for `--since 1h|24h|7d` and the
  slowest and the failing paths among its recent requests; `--requests`
  lists those, `-f` follows them. The agent reads Caddy's access log, keeps
  per-minute counts and a latency histogram for seven days and the last 200
  requests of each application in memory. The log carries no headers and no
  query strings. `GET /applications/:name/traffic` and `…/requests`.
- Certificate status: `shipwick status` names every hostname whose
  certificate is still being obtained, is waiting for DNS, or has 14 days or
  less to go (`--verbose` lists the ones in order too), and the application
  detail carries `certificates`. A certificate being obtained and one running
  out are events; a webhook is told `certificate.expiring` at 14 days and
  again at 3. `SHIPWICK_PROXY_TLS_ADDR` (default `caddy:443`) is where the
  agent looks.
- The agent's API and the dashboard are compressed by the proxy like
  application routes are. A followed log is not: the encoder would hold its
  response header back until the application printed something.
- Replacing a replica no longer waits for the old one to exit. A deployment
  is done when every new replica serves; the replaced containers get their
  `SIGTERM` and their grace period in the background. Replacing the single
  replica of an application that ignores `SIGTERM` — Node started as
  `CMD ["node", "server.js"]`, for one — took 13.5–14.0s and now takes
  2.5–3.0s. A container that had to be killed at the end of its grace period
  is reported in the application's events, with what to do about it.
- `deploy.stop_timeout` in `deploy.yaml`: how long a replica that is being
  stopped gets between `SIGTERM` and `SIGKILL`, from 1s to 10m (default 10s),
  for applications that hold WebSockets or long uploads. It applies wherever
  Shipwick stops a replica gracefully: deployments, `stop`, `delete`, restarts
  of an unhealthy replica.
- Deployments survive a restart of the agent. A deployment that is running
  when the agent stops — an upgrade of Shipwick restarts it — is no longer
  marked `FAILED`: the agent resumes it at its next start, keeps the
  containers it had created and the replicas that were already serving, and
  does what was left to do. Its events say `Resumed after the agent
  restarted`, and `shipwick deploy` waits through the restart. A `pre_deploy`
  command that was running at that moment is not run a second time: that
  deployment fails and says so.
- A deployment that fails or is rolled back removes the image it named, unless
  it is the running version's or the rollback target's. An image sent with
  `build:` for a deployment that then failed used to stay until the
  application's next successful deployment.
- `POST /api/v1/applications/{name}/validate` answers what `deploy` would
  answer for a `deploy.yaml` — a hostname or a published port another
  application holds, a secret that is not stored — without deploying it and
  without waiting for a deployment that is running. It can be asked before an
  image is built: `build` without `image` is valid there.
- **Backups the server takes.** `backups` in deploy.yaml — `schedule`, `keep`,
  `before`, `stop` — has the agent archive an application's volumes on a cron
  schedule: `before` runs a command in the replica first (a dump, a
  checkpoint) and fails the backup if it fails, `stop: true` stops the
  application for the archive and starts it again whatever happens. The
  backups are kept on the server, under `<data dir>/backups`, and the oldest
  beyond `keep` are removed; a failed scheduled backup is posted to the
  webhook as `backup.failed`. `shipwick backups <app>` lists them, with `run`,
  `restore`, `download` and `rm`, and `shipwick status` has a line for them.
  The existing `shipwick backup` and `restore` are unchanged: they move an
  archive between the server and your machine.
- **Backups off the server, encrypted.** With the `SHIPWICK_BACKUP_S3_*`
  variables the agent also sends every backup to a bucket on any
  S3-compatible service, and restores from it when the server's copy is gone.
  With `SHIPWICK_BACKUP_PASSPHRASE` everything is encrypted before it is
  written, in either place; `shipwick backups decrypt` reads such a file on
  your machine. The format is described in docs/architecture.md.
- **`shipwick backups verify`** restores a backup into scratch volumes, starts
  one container of the application's current image on them and holds it to
  the health check, then removes both. The application is not touched, and
  the backup is marked verified or not, with the container's last output.
- **The agent backs up its own state**: a consistent copy of `shipwick.db` and
  `encryption.key`, daily and with `shipwick server backup`, kept like
  application backups and only ever encrypted. Without
  `SHIPWICK_BACKUP_PASSPHRASE` the key is written nowhere, and
  `shipwick doctor` and `GET /server` (`backups`) say so. The handbook has the
  procedure for restoring it on a new server.
- **Moving to a new server.** `shipwick export` writes every application's
  configuration and secrets, the stored secrets, registry credentials and
  certificates, the images built by `shipwick deploy`, the folders of static
  applications and an archive of every volume into one file, encrypted with a
  passphrase. `shipwick import` on the new server stores them under that
  server's own key, restores each application's volumes before it first
  starts, and deploys the applications one after the other: those without a
  domain first, then the rest, oldest first. Nothing that exists is replaced
  without `--overwrite`. Both take an `admin` token. Handbook §7, *Moving to a
  new server*.
- **A second server kept ready.** With `SHIPWICK_EXPORT_SCHEDULE` the server
  writes an export to its backup bucket on a schedule; a second server with
  `SHIPWICK_STANDBY_SCHEDULE` and the same bucket imports the newest one with
  every application deployed and stopped. `shipwick standby promote` starts
  them in order and prints the DNS records to change; `shipwick standby` shows
  what is waiting. Shipwick does not fail over: a person runs the command, and
  the data is as old as the last export. `shipwick import --stopped` does the
  same from a file. Handbook §7, *A second server kept ready*.
- Deployments have two more kinds, `import` and `standby`, and four more
  error codes: `INVALID_EXPORT`, `IMPORT_IN_PROGRESS`, `EXPORT_IN_PROGRESS`,
  `STANDBY_NOT_CONFIGURED`.
- Dashboard: a **Traffic** panel on every application's page, static ones
  included: requests per step with the 5xx among them, the 95th percentile of
  their durations, the window's totals over 1h, 24h or 7d, and the most
  recent requests one by one.
- Dashboard: the **certificate** of each hostname on the application's page —
  a badge and the agent's sentence for one that waits for DNS, is being
  obtained or is about to expire — and a **Certificates** page that lists the
  supplied ones, marks their last 30 days, and lets an admin add or remove
  one.
- Dashboard: a **Registries** page to log the server in to a registry and out
  of it, and **Rotate encryption key** on the server page; with the key in
  the agent's environment the new key is shown once, with the line to put
  into `/opt/shipwick/.env`.
- Dashboard: a **Backups** panel for applications with volumes — the backups
  taken, *Back up now*, *Verify* with the container's output, a download per
  volume, *Restore* into a stopped application and *Remove* — and on the
  server page where backups go, how the agent's own state is backed up, and
  *Back up state now*.
- Dashboard: the server's **disk** and the active **alerts** on the server
  page; alerts are also marked in the navigation, listed on the overview and
  shown on the page of the application they are about.
- Dashboard: **Export to backups** and the list of exports on the server
  page, and on a standby what waits to be started, the scheduled fetch,
  *Import newest now* and *Promote*, which answers with the DNS records to
  change. A running import is shown while it runs, with each application's
  outcome afterwards.
- Dashboard: an application's address is its domain and `path` wherever it is
  shown; its page shows the `proxy` block (headers, redirects, the accounts
  by name), the fallback page of a static application, `deploy.stop_timeout`
  and the `backups` schedule. Deployments made by an import read *imported*
  or *imported, stopped*.

### Fixed

- `shipwick deploy` waiting for a deployment no longer gives up with
  "unexpected HTTP 503" when the agent restarts behind the proxy — during an
  upgrade, for one. A `502`, `503` or `504` that the proxy answers in the
  agent's place is the agent being away: the CLI waits on, the deployment
  resumes, and other commands say that the agent is not answering and what
  to look at.
- Dashboard: stopping or deleting an application whose replicas take their
  `deploy.stop_timeout` to exit no longer ends in "the agent did not answer
  within 60s"; the dashboard waits as long as the agent does.
- Dashboard: a replica that a finished deployment replaced and that is still
  stopping reads *Stopping* instead of counting as one replica too many.
- Dashboard: after a `429` the dashboard waits as long as `Retry-After` says
  before it asks again, instead of polling on.
- Dashboard: the log viewer no longer offers static applications, which have
  no logs.
- Dashboard: a deployment refused for a secret that is not stored links to
  the Secrets page with the name filled in, and one refused by a registry to
  the Registries page.

- The installer's removal of earlier releases' images, repaired in 0.4.1,
  was never run: the function existed and nothing called it. It now runs
  once the upgraded agent is healthy. Run the installer again to reclaim the
  space; it changes nothing else on a server that is up to date.

## [0.4.1] - 2026-09-28

What a first install from an empty server and an empty laptop found.

### Changed

- The dashboard's mark and favicon are Wick, the project's mascot.

### Fixed

- A static entry of a `shipwick.yaml` was deployed without its folder being
  uploaded first, which the agent refused; it is now uploaded like a
  `deploy.yaml`'s, relative to the `shipwick.yaml`.
- `shipwick server install` printed "once the records exist, check the setup"
  even when it had just confirmed the records exist, and echoed Compose's
  progress lines twice.
- A failed TCP health check said `TCP connect to :5432`; it now says
  `TCP connect to port 5432`.

- A static application deployed from the folder that holds its `deploy.yaml`
  (`static: .`, which is what `shipwick init` writes for a folder with an
  `index.html` at its root) uploaded the file too, and the proxy served it at
  `/deploy.yaml`. The CLI now leaves `deploy.yaml`, `shipwick.yaml`, `.git`
  and `.env` files out of the upload, wherever they are in the folder. Deploy
  such a site again to replace the folder on the server.
- `shipwick init` refused a `package.json` (or `.csproj`, `go.mod`,
  `pyproject.toml`) that starts with a UTF-8 byte-order mark, which is what
  PowerShell's `Set-Content` and some Windows editors write: `invalid
  character '\ufeff'`. The mark is now ignored, as the projects' own tools do.
- An image the CLI built and sent for a deployment the agent then refused
  (a domain another application serves, say) stayed on the server for good:
  no deployment named it, so no sweep considered it. The sweep after a
  successful deployment now also removes the application's local images that
  no deployment names and that are older than ten minutes; deleting the
  application removes all of them.
- `shipwick deploy` with `build:` asks the server whether another
  application already serves the domain before building, instead of finding
  out after the image was built and sent.
- A static application's first deployment ended with `shipwick logs -f` as
  the next step, which it then refused; it now suggests `shipwick open`.
- `shipwick server install` failed on every server nobody had connected to
  before: `ssh` in batch mode refuses an unknown host key, and the message
  suggested copying a login key. The key of a new server is now accepted on
  first contact; a key that changed is still refused, and the message says
  to forget the old one with `ssh-keygen -R` if the server was reinstalled.
- `shipwick server install` gave up when Docker was installed but not
  answering yet, which is where a server created a minute ago from an image
  that installs Docker at first boot often is. It now waits up to a minute
  for the daemon before saying so.
- The installer's removal of earlier releases' images did nothing: it asked
  `docker images` for two repositories at once, which it refuses. It now asks
  for each in turn. Run the installer again to reclaim the space.

## [0.4.0] - 2026-09-27

### Added

- `build: .` in deploy.yaml, in place of `image`: `shipwick deploy` builds the
  image on your machine with `docker build`, for the server's architecture,
  and sends it to the agent, which loads it — no registry, no `docker login`.
  The image is tagged `shipwick.local/<name>:<stamp>`; the agent never pulls
  from that host and never builds. `POST /applications/:name/images` takes
  the archive.
- `static: dist/` in deploy.yaml: a built frontend served by Caddy itself, with
  no container. `shipwick deploy` uploads the folder (`PUT …/static`, up to
  512 MB) and deploys it by its digest; the agent copies it into the proxy,
  checks for `index.html` and routes the domain to it. Rollback and redeploy
  re-use the kept folder. New error code `STATIC_APPLICATION` for logs, metrics
  and commands, which such an application does not have. The compose files
  give Caddy a `caddy-static` volume for the folders.
- `shipwick init` recognises the project in the current directory — a Nuxt,
  Next or Node application, a .NET project, a Go program, a Python application
  — and writes a multi-stage `Dockerfile` (small runtime image, non-root user,
  port exposed), a `.dockerignore` and a `deploy.yaml` with `build: .`. A
  folder of static files, or a Vite or Astro project that builds one, gets
  `static: <dir>` and no Dockerfile. Existing `Dockerfile` and `.dockerignore`
  files are kept; `--image` keeps the old behaviour; `--static <dir>` forces
  the static kind.
- `shipwick server install user@host` installs or upgrades the server over
  SSH from your machine: Docker when it is missing, then the installer with
  `--agent-domain` and `--dashboard-domain`. The token is saved as a context
  and the DNS records to create are printed.
- `shipwick doctor` checks the setup in one screen — CLI and agent versions
  against the latest release, token, Docker, proxy, ports 80 and 443, and for
  every domain whether DNS points at the server and HTTPS answers — and exits
  non-zero when something is broken.
- `shipwick open [app]` opens the application's address in the browser.
- `shipwick deploy` in a directory without a `deploy.yaml` writes one first,
  the way `init` does, when a terminal is attached.
- A first deployment ends with the two commands to run next: `logs -f` and
  `status`.
- **Several applications in one file.** `shipwick.yaml` holds an `apps` list
  in which every entry is a complete `deploy.yaml`; `after: [postgres]` names
  the entries one must wait for. `shipwick deploy` uses it when there is no
  `deploy.yaml`, `shipwick validate` checks it and prints the order.
  Annotated example: `configs/shipwick.example.yaml`.
- **Faster deployments.** The applications of a `shipwick.yaml` deploy at the
  same time, in dependency order, up to four at once (`--parallel N`); every
  line of output carries its application's name. An application whose
  dependency did not deploy is skipped, the rest finishes, and the command
  exits non-zero if any failed or was skipped. Several `deploy.yaml` files
  given with `-f` still deploy one after the other.
- Secrets kept on the server. `shipwick secret set DATABASE_PASSWORD` stores a
  value, encrypted like `env` values are, and `${DATABASE_PASSWORD}` in an
  `env` value of `deploy.yaml` is filled in by the agent when you deploy;
  `--env-file` becomes optional. `shipwick secret ls` lists names and dates,
  `shipwick secret rm` removes one. A name that is set neither where `shipwick`
  runs nor on the server is refused before anything is recorded, with the
  command to run. `$${NAME}` in an `env` value reaches the agent as written
  and becomes the literal `${NAME}` there. API: `GET /secrets`,
  `PUT /secrets/:name`, `DELETE /secrets/:name`.
- `health.start_period`: extra time, up to 30 minutes, that a replica gets to
  come up before failed health checks count — during a deployment and after a
  restart by the supervisor — so a slow starter no longer has to raise
  `retries`.
- `shipwick volumes` lists every volume on the server with the application it
  belongs to, how much it holds and whether that application still exists;
  `shipwick volumes rm <name>` removes a volume of a deleted application. The
  API gained `GET /volumes` and `DELETE /volumes/:name` (`409 VOLUME_IN_USE`
  while the application exists).
- The proxy compresses application responses with zstd or gzip when the
  client asks for it, for compressible content types from a kilobyte up.
- A deployment whose hostname is not ready spells out the record to create:
  `add an A record: api.example.com → 62.238.109.115 (DNS only, not proxied)`,
  with an AAAA record when the server has an IPv6 address; a hostname behind
  Cloudflare's proxy is told to turn the proxy off for the record instead.
- The API limits failed authentications: after 20 within a minute from one
  address, its wrong tokens are answered `429 RATE_LIMITED` with `Retry-After`
  for the next minute. A valid token is never refused.
- Dashboard: a **Secrets** page lists the secrets kept on the server, and an
  admin adds, replaces or removes one there; values are never shown.
- Dashboard: a **Volumes** page lists every volume on the server with its
  application and size, and an admin removes the volume of a deleted
  application there.
- Dashboard: a static application shows what it serves (`42 files, 3.1 MB,
  served by the proxy`) in place of replicas, logs and metrics; an application
  with `build` says its image is built by `shipwick deploy`, and the deploy
  dialog offers no image field for either. `health.start_period` is shown with
  the health check.

### Changed

- The database gains two migrations, applied when the 0.4 agent first starts:
  the `secrets` table and the columns of static deployments. A 0.3 agent
  refuses a database that has them, so upgrade with the installer and do not
  go back. The 0.3 CLI keeps working against the 0.4 agent; the new commands
  and `deploy.yaml` keys need the 0.4 CLI (`shipwick upgrade`).
- The compose file gives Caddy a `caddy-static` volume for the folders of
  static applications, so the upgrade recreates the Caddy container once:
  connections open at that moment are reset, applications keep running.
- The installer removes the agent and dashboard images of earlier releases
  after an upgrade; they used to stay behind, a few hundred megabytes per
  release.

### Fixed

- An empty `entrypoint` or `command` argument (`command: ""`) is refused instead
  of being handed to Docker as an empty argument.
- After a rollback, and after `shipwick start`, a replica that had just passed
  its health check could be reported as `starting` — and the application as
  `DOWN` — until the supervisor's next probe. A passed check now counts at once.
- `shipwick restore --volume nope` and `shipwick jobs logs` for an unknown job
  said the server did not know the application; they now say what was not
  found.

## [0.3.1] - 2026-09-27

Found on a real server the day 0.3.0 shipped. Upgrade by running the installer
again; nothing else changes.

### Fixed

- The DNS check that holds a hostname back until it points at the server asks
  public resolvers (1.1.1.1, 8.8.8.8, 9.9.9.9) instead of the server's own,
  which remembers that a record did not exist for the zone's negative TTL —
  half an hour on Cloudflare — and kept the hostname waiting that long after
  the record was created.
- An application could show as `FAILED` for one poll while its first deployment
  was being recorded or the moment a deployment succeeded: the application and
  its in-flight deployments were read separately, and a commit in between made
  it look as if it had neither. They are read in one transaction now.

## [0.3.0] - 2026-09-27

Before 1.0 a minor version may change how things work under the hood. This one
adds to the database and to the data directory. Upgrading is still running the
installer again: the agent applies four migrations on its first start, creates
`encryption.key` next to `shipwick.db` and encrypts the environment values of
earlier deployments once. **Back up `encryption.key` with the database**;
without it the database cannot be read. The token from the installer keeps
working as the root token, application containers are not touched, and the
compose file gains two optional variables for notifications.

### Added

- **A hostname is served once its DNS points at the server.** Deploying before
  the record exists no longer costs the certificate: Caddy is told about a
  domain, alias or redirect only when it resolves to this server, so Let's
  Encrypt's five-failures-per-hour limit is never spent on a hostname that
  cannot pass yet. Until then `shipwick deploy` warns — `does not resolve yet`,
  or `resolves to 104.21.5.6, not to this server` — instead of claiming the
  domain is routed; the record is checked again every 10 seconds, and the
  application's events say when the hostname is being served. The server's own
  addresses are learned from `SHIPWICK_AGENT_DOMAIN` and
  `SHIPWICK_DASHBOARD_DOMAIN`; with neither set, resolving at all is enough.
- **Old images are removed.** After a successful deployment, and after `shipwick
  delete`, the images only retired deployments refer to are untagged. The running
  version and the rollback target are always kept, and so is any image another
  application or a container outside Shipwick uses.
- winget manifests for the CLI under `packaging/winget/`, generated from a
  release's checksums.
- **`publish`** in `deploy.yaml`: a container port published on a port of the
  server itself, for services the proxy cannot serve because they are not
  HTTP — a database reached from a laptop, a game server. TCP or UDP, on one
  address of the server or on all; needs the recreate strategy and one replica.
  A port the agent, the proxy or another application already holds is refused
  before anything is started.
- **Several hostnames per application.** `aliases` lists hostnames served
  exactly like `domain`; `redirects` lists hostnames answered with a `308` to
  `https://<domain>` with the same path and query — `www.example.com`, an old
  domain. Redirects work while the application is stopped. A hostname in use
  anywhere, in any role, is refused, and the error names the offending line
  (`aliases[1]`).
- **Health checks for applications that are not HTTP.** `health.tcp: 5432`
  counts a replica healthy when the port accepts a connection;
  `health.command: ["pg_isready", "-U", "postgres"]` runs the command inside
  the replica and reads its exit code. `path`, `tcp` and `command` are
  alternatives; `interval`, `timeout` and `retries` apply to all three. A
  failed command check reports the exit code and the last line it printed.
- **`env` values are encrypted in the database.** Every deployment's environment
  values are stored as AES-256-GCM ciphertext; names, images and everything else
  stay readable. A copy of `shipwick.db` without the key reveals no secrets. The
  values of deployments made by earlier releases are encrypted on the first
  start after upgrading. The key comes from `SHIPWICK_ENCRYPTION_KEY` (64 hex
  characters) when set.
- **`entrypoint`, `command` and `user`** in `deploy.yaml` replace what the image
  declares, for running a worker or a second program from the same image. A
  string is one argument, a list is several; nothing goes through a shell.
- **`logging`** in `deploy.yaml` ships replica logs through a Docker logging
  driver (`gelf`, `syslog`, `fluentd`, `awslogs`, `splunk`, `journald` or
  `local`) instead of the server's disk. `shipwick logs` keeps working through
  the local copy Docker keeps.
- **Several API tokens, with roles.** `shipwick token create ci --role deploy`
  makes a token that can deploy, roll back, stop and start but not delete
  applications or manage tokens; `read` only looks; `admin` does everything.
  The token from the installer is the root token, admin, and stays as it was.
  `shipwick token ls` shows when each token was last used, `shipwick token
  revoke` ends it. A token asked to do more than its role allows is told which
  role it needs.
- Deployments record which token made them (`by` in the API and the dashboard's
  history), and a stop or start by a token other than root says so in the
  application's events.
- **`shipwick upgrade`** replaces the CLI with the latest release, verified
  against the release's checksums, and says when the server is behind (the
  server is still upgraded by running the installer there). A CLI installed
  with Homebrew or winget is left to the package manager; the command prints
  the line to run. `--check` only reports.
- **Several servers.** `shipwick login --context staging` saves a second server
  under a name; `--context` or `SHIPWICK_CONTEXT` picks one for a command, and
  `shipwick context ls | use | rm | current` manage them. An existing config
  file keeps working as the context `default`.
- **Volume backups.** `shipwick backup` downloads an application's volumes as
  tar archives, `shipwick restore` puts one back into a stopped application.
  The agent reads and writes the volume through the replica's container, so
  nothing needs to be installed on the server. New endpoints
  `GET`/`PUT /applications/:name/volumes/:volume/archive` and
  `GET /applications/:name/volumes`; a restore of a running application is
  `409 APPLICATION_RUNNING`.
- **Notifications.** Set `SHIPWICK_WEBHOOK_URL` on the agent and it posts
  every deployment's outcome — succeeded, failed, rolled back — and every
  application that goes down or recovers to a Slack or Discord webhook, or as
  JSON to any HTTPS endpoint; `SHIPWICK_WEBHOOK_SECRET` signs the requests.
  `shipwick server status` shows whether one is configured.
- **Metrics history.** The agent records the CPU and memory of every running
  replica every 30 seconds and keeps a week; `GET
  /applications/:name/metrics/history?since=1h|24h|7d` serves it per replica,
  aggregated to chart size, for the dashboard to draw.
- **`pre_deploy`** in `deploy.yaml`: a command run from the new image, with the
  application's environment, before any replica of the new version starts —
  database migrations. If it fails, the deployment fails before anything was
  touched, and its last output lines are shown with the error.
- **`jobs`** in `deploy.yaml`: commands on a cron schedule (UTC), each in a
  one-off container from the application's image. `shipwick jobs` lists them
  with their last and next run, `shipwick jobs run` starts one now,
  `shipwick jobs logs` shows a run's output.
- **`shipwick run <app> -- <command>`** runs a command in a one-off container
  of the application, prints its output and exits with its exit code.

### Changed

- The agent creates `encryption.key` in its data directory on first start; back
  it up together with `shipwick.db`. Without it the database cannot be read, and
  the agent refuses to start against it.

### Fixed

- The agent, Caddy and dashboard containers of the production compose file now
  cap their logs at 3 × 10 MB each, like application replicas always did.
  Applied on the next `curl -fsSL https://get.shipwick.com | sh`.

## [0.2.0] - 2026-09-27

Before 1.0 a minor version may change how things work under the hood. This one
does: routing. Upgrading is still running the installer again, but this
upgrade recreates the Caddy container once, because it joins a second network.
Established connections through the proxy are cut at that moment; nothing else
is, and applications keep running throughout.

### Added

- **Applications reach each other by name.** Every application is `http://<name>:<port>`
  for the other applications on the server, no domain needed: `orders` calls
  `payments` at `http://payments:8080` and gets a healthy replica of the current
  version. Nothing outside the server can reach these names.
- **`volumes`** in `deploy.yaml`: named Docker volumes mounted into the
  replica, for databases and everything else that must keep its data. A volume
  belongs to the application and survives redeployments, rollbacks and
  `shipwick delete`.
- **`deploy.strategy: recreate`**: the running version is stopped before the
  new one starts, for applications whose two versions cannot run side by side.
  Required with `volumes`. A failed deployment starts the old containers again.
- **`${NAME}` placeholders** in `deploy.yaml` are filled in by the CLI from its
  environment or from `--env-file` before the file is validated or sent, so
  that secrets stay out of the file and the repository. An unset name is an
  error, never an empty value; `$${NAME}` is a literal.
- **Several applications in one command**: `shipwick deploy -f a/deploy.yaml
  -f b/deploy.yaml` deploys them in order and stops at the first failure.
  `validate` takes several files too.

### Changed

- **Rollouts no longer reload the proxy.** Caddy is told an application's
  name and asks Docker's DNS who carries it for every request; replicas take
  the name when they are ready and lose it when they stop. A rollout, a crash
  or a restart therefore changes nothing in Caddy's configuration, which used to
  be reloaded once per replica replaced — and a reload resets connections that
  are being established at that instant. Measured with a new connection per
  request and added latency, 18 consecutive rolling redeploys answered 15,774
  of 15,774 requests. A crashed replica now leaves the rotation at once instead
  of at the supervisor's next look.
- The names `agent`, `caddy`, `dashboard` and `localhost` can no longer be
  application names: they are Shipwick's own on the network applications share.
- The agent's Docker network is joined by a second one, `<network>-services`,
  created on startup.

## [0.1.1] - 2026-09-21

Found by installing 0.1.0 on real servers. The agent, the CLI and the dashboard
are unchanged; upgrade by running the installer again.

### Added

- The CLI can be installed with Homebrew: `brew install shipwick/tap/shipwick`.

### Fixed

- The installer checks that ports 80 and 443 are free before it changes
  anything, and says what holds them. It used to write its files, start half of
  the services and stop at Docker's error.
- Run again after a first run that did not finish, the installer says where the
  API token is; it used to show it only in the run that generated it.
- `SHIPWICK_HTTP_PORT`, `SHIPWICK_HTTPS_PORT` and the image overrides given to
  the installer are kept in `.env`, so an upgrade does not move the proxy back
  to the default ports.
- The installer works with BusyBox `wget` (Alpine without curl).

### Documentation

- What a deployment costs is stated as measured on a real server: connections
  being established at the instant Caddy's configuration is reloaded are reset —
  5 of 233 requests in the worst case, a new connection per request from 100 ms
  away. The earlier figure, 100 of 100, was measured over loopback, where this
  cannot be seen.

## [0.1.0] - 2026-09-20

The first release: production deployments on a single server, without
Kubernetes.

### Added

- **Agent** (`shipwick-agent`): a single static binary that drives the Docker
  Engine API directly, keeps its state in SQLite, and exposes a token-protected
  REST API. Deployment records are immutable.
- **`deploy.yaml`**: name, image, port, domain, replicas, env, health check,
  CPU and memory limits, restart policy. Strictly validated, with errors that
  name the field and what was expected.
- **Rolling deployments**: replicas are replaced one at a time, each only after
  it proved healthy, with at most one container above the desired count. A
  failed rollout is undone automatically; the application keeps serving.
- **Rollback** to any earlier successful deployment, with its full stored
  configuration, through the same engine as a deployment.
- **Health checks** over HTTP, at deploy time and continuously afterwards.
- **Supervision**: crashed and unhealthy replicas are restarted with backoff
  (1s, 2s, 5s, 10s, 30s), then marked `CRASH_LOOP`; replicas that disappear are
  recreated within about a second.
- **Caddy integration**: automatic HTTPS and routing for every application's
  domain, load balancing across replicas, configuration verified and restored
  continuously. Optionally serves the agent's API and the dashboard over HTTPS.
- **Metrics**: CPU and memory per replica and per application, against their
  limits.
- **CLI** (`shipwick`): `init`, `validate`, `deploy`, `redeploy`, `rollback`,
  `status`, `ps`, `logs`, `stop`, `start`, `delete`, `login`, `server status`.
  Made for terminals and for CI (`shipwick deploy --image …:$GIT_SHA`).
- **Dashboard**: overview, applications, deployments, servers, live logs;
  redeploy and rollback. The token never reaches the browser.
- **Installer**: `curl -fsSL https://get.shipwick.com | sh` sets up the agent,
  Caddy and the dashboard on a server, or only the CLI with `--cli`. Release
  binaries are verified against their checksums.

### Known limitations

- Secrets in `env` are stored unencrypted in the agent's SQLite file.
- One token per agent; no users or roles.
- When a replica crashes, one in-flight request may receive a 502.
- No volumes, and no custom Caddy directives.

[Unreleased]: https://github.com/shipwick/shipwick/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/shipwick/shipwick/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/shipwick/shipwick/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/shipwick/shipwick/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/shipwick/shipwick/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/shipwick/shipwick/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/shipwick/shipwick/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/shipwick/shipwick/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/shipwick/shipwick/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/shipwick/shipwick/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/shipwick/shipwick/releases/tag/v0.1.0

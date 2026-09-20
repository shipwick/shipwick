# Security policy

Shipwick's agent holds the Docker socket, which makes it root on the server it
runs on. We treat security reports accordingly.

## Reporting a vulnerability

**Please do not open a public issue.** Report privately, either way:

- Email **security@shipwick.com**
- GitHub: [Report a vulnerability](https://github.com/shipwick/shipwick/security/advisories/new)
  (private; only maintainers see it)

Helpful to include: the version (`shipwick --version`, or `shipwick server status` for the agent's),
how you run the agent (the installer, your own compose file, a bare process),
what an attacker needs to start with, and steps to reproduce. A proof of
concept is welcome but not required.

What to expect:

- an acknowledgement within 3 days;
- an assessment — whether we can reproduce it, and how severe we think it is —
  within 10 days;
- a fix released before the details are published, and credit in the release
  notes unless you prefer otherwise. We will agree the disclosure date with you.

Shipwick is maintained by volunteers and has no bug bounty.

## Supported versions

Until 1.0, only the latest release receives security fixes. Upgrading is
running the installer again.

## What counts

The trust model is described in the [Security section of the README](README.md#12-security).
In short: **the API token is equivalent to root on the server, by design**, and
Shipwick is not a multi-tenant sandbox.

Vulnerabilities — we want to hear about these:

- reaching the API, or the dashboard's session, without a valid token;
- recovering the token or an application's env values from logs, API
  responses, error messages, the dashboard, or files readable by others;
- input in `deploy.yaml` or an API request that executes a command, escapes
  the fields it belongs to (proxy configuration, container names, file paths),
  or yields a privileged container, a host mount or a published host port;
- an application container that can reconfigure the proxy, reach the agent's
  data, or claim a domain served for another application;
- the CLI sending a saved token somewhere other than the agent it was saved for;
- the installer fetching or running something it did not verify.

Expected behavior — not vulnerabilities:

- a holder of a valid token running arbitrary images, reading env values of
  containers through Docker, or otherwise controlling the server;
- anyone with access to the Docker socket, the agent's data directory or
  Caddy's admin socket doing the same;
- secrets being unencrypted in the SQLite file (documented; on the roadmap);
- exposing port 9000 to the internet against the documentation's advice.

If you are unsure which list something belongs to, report it privately anyway.

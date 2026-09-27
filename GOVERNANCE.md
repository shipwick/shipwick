# Governance

Shipwick is small, and so is the way it is run. This page says who decides
what, and how that changes as people join.

## Roles

**Maintainers** merge pull requests, cut releases and answer for the project's
direction. Today there is one, listed in [MAINTAINERS](#maintainers).

**Contributors** are everyone whose change was merged. There is no
membership, form or invitation; the first merged pull request makes one.

## How decisions are made

Changes come in as pull requests and are discussed there, in the open. Small
things are decided by whoever reviews them. Anything that changes behaviour a
user can see, the API, `deploy.yaml` or the on-disk format is discussed in an
issue first, as [CONTRIBUTING.md](CONTRIBUTING.md) asks, against the
principles written there: one server, nothing to operate besides Docker, few
dependencies, no shell, one way to do a thing. The [roadmap](ROADMAP.md) says
what is planned and, under *Out of scope*, what will not be built.

When maintainers disagree, they talk until they agree; if that fails, the
change waits. A change nobody is sure about is not merged.

## Becoming a maintainer

A contributor who has, over a few months, sent changes that were merged as
they came, reviewed other people's changes carefully and shown the judgement
the principles above ask for, is invited by the existing maintainers. The
invitation and its acceptance are recorded in a pull request that adds the
name below. A maintainer who has been away for a year is asked whether they
want to stay, and moved to *Emeritus* if not; nothing is held against anyone
for stepping back.

## Releases

Any maintainer may cut a release, following [docs/releasing.md](docs/releasing.md).
Version numbers follow semantic versioning; before 1.0, a minor version may
change the API or the file formats, and the changelog says so.

## Security

Vulnerabilities are handled privately, as [SECURITY.md](SECURITY.md)
describes, by the maintainers alone until a fix is released.

## Maintainers

| Name | GitHub | Since |
|---|---|---|
| Mert Gündoğan | [@mertgundoganx](https://github.com/mertgundoganx) | 2026 |

## Changes to this document

By pull request, like everything else; a change to *Becoming a maintainer* or
*How decisions are made* needs every current maintainer's approval.

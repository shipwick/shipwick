#!/bin/sh
# Builds shipwick-agent_<arch>.rpm for amd64 and arm64 from the files in /src,
# into /out. Run by scripts/build-packages.sh in a Rocky Linux container,
# where it installs rpm-build: an rpm built there installs on older releases
# too. The binaries arrive built, so nothing is compiled and one machine
# builds both architectures.
#
#   sh build-rpm.sh 0.7.0

set -eu

VERSION="${1:?usage: build-rpm.sh <version without v>}"
# A hyphen separates version and release in an RPM; 0.7.0~rc.1 sorts before 0.7.0.
RPM_VERSION="$(printf '%s' "$VERSION" | sed 's/-/~/')"
SRC="${SRC:-/src}"
OUT="${OUT:-/out}"

command -v rpmbuild >/dev/null 2>&1 || dnf install -y -q rpm-build >/dev/null 2>&1 \
    || { echo "could not install rpm-build in the container (dnf install rpm-build)" >&2; exit 1; }

# The scripts become scriptlets of the spec, where a percent sign is a macro.
for script in postinst prerm postrm; do
    if grep -q '%' "$SRC/$script.sh"; then
        echo "$script.sh contains a percent sign, which rpm would expand" >&2
        exit 1
    fi
done

mkdir -p "$OUT"
for arch in amd64 arm64; do
    case "$arch" in
        amd64) rpm_arch=x86_64 ;;
        arm64) rpm_arch=aarch64 ;;
    esac
    top="$(mktemp -d)"
    spec="$top/shipwick-agent.spec"
    cat > "$spec" <<EOF
Name:           shipwick-agent
Version:        $RPM_VERSION
Release:        1
Summary:        Production deployments on your own server (the agent)
License:        Apache-2.0
URL:            https://shipwick.com
Recommends:     systemd
Recommends:     (docker-ce or moby-engine or docker)

# The binary is built already, static and stripped.
%global debug_package %{nil}
%global __strip /bin/true
%global _build_id_links none
%global __os_install_post %{nil}

%description
The Shipwick agent as a service of the host: it runs Docker applications on
this server with health checks, rolling deployments and rollbacks, and
answers the shipwick command-line client and the dashboard.

The package holds the agent, its systemd unit and its settings file. The
reverse proxy and the dashboard run as containers, from the compose file in
/usr/share/shipwick. Docker Engine is needed and is not installed by this
package.

%install
install -D -m 0755 $SRC/shipwick-agent_$arch %{buildroot}/usr/bin/shipwick-agent
install -D -m 0644 $SRC/shipwick-agent.service %{buildroot}/usr/lib/systemd/system/shipwick-agent.service
install -D -m 0644 $SRC/agent.env %{buildroot}/usr/share/shipwick/agent.env
install -D -m 0644 $SRC/compose.yml %{buildroot}/usr/share/shipwick/compose.yml
install -D -m 0644 $SRC/LICENSE %{buildroot}/usr/share/licenses/shipwick-agent/LICENSE
install -D -m 0644 $SRC/NOTICE %{buildroot}/usr/share/doc/shipwick-agent/NOTICE

%files
/usr/bin/shipwick-agent
/usr/lib/systemd/system/shipwick-agent.service
%dir /usr/share/shipwick
/usr/share/shipwick/agent.env
/usr/share/shipwick/compose.yml
%dir /usr/share/licenses/shipwick-agent
%license /usr/share/licenses/shipwick-agent/LICENSE
%dir /usr/share/doc/shipwick-agent
%doc /usr/share/doc/shipwick-agent/NOTICE
# /etc/shipwick/agent.env is not listed: the scriptlet below writes it on the
# first installation, and removing the package must leave the token in place.

%post
if [ "\$1" -ge 2 ]; then set -- upgrade; else set -- install; fi
$(sed '1d' "$SRC/postinst.sh")

%preun
[ "\$1" -eq 0 ] || exit 0
$(sed '1d' "$SRC/prerm.sh")

%postun
[ "\$1" -eq 0 ] || exit 0
set -- remove
$(sed '1d' "$SRC/postrm.sh")
EOF
    rpmbuild --quiet -bb --target "$rpm_arch" --define "_topdir $top" --define "_rpmdir $top/out" "$spec"
    mv "$top/out/$rpm_arch"/shipwick-agent-*.rpm "$OUT/shipwick-agent_$arch.rpm"
    rm -rf "$top"
    echo "built shipwick-agent_$arch.rpm ($RPM_VERSION)"
done

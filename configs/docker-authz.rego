# What whoever holds the Docker socket may ask of the daemon, for Docker's
# authorization plugin opa-docker-authz (https://github.com/open-policy-agent/opa-docker-authz).
# The daemon asks it about every request — the agent's, docker compose's,
# the docker command's — and the ones a rule below names are answered
# "request rejected by administrative policy". Everything else is allowed.
# docs/handbook.md, "Less than the whole socket", says how to install it,
# what it adds to the socket proxy and what it costs; scripts/test-docker-authz.sh
# runs Shipwick through it and then tries each refusal.
#
# The agent never asks for any of this: what is refused here was not the
# agent's request, or is a fault in it.
package docker.authz

import rego.v1

default allow := false

allow if count(deny) == 0

# The part of the Engine API a path addresses, with the version in front or
# without it: "containers" for /v1.47/containers/create.
section := [s | some s in input.PathArr; s != ""; not startswith(s, "v1.")][0]

creating_container if {
	input.Method == "POST"
	endswith(input.PathPlain, "/containers/create")
}

running_command if {
	input.Method == "POST"
	regex.match(`/containers/[^/]+/exec$`, input.PathPlain)
}

creating_volume if {
	input.Method == "POST"
	endswith(input.PathPlain, "/volumes/create")
}

host := object.get(input, ["Body", "HostConfig"], {})

# The plugin decodes a body sent as `Content-Type: application/json` and no
# other spelling, while the daemon reads `application/json; charset=utf-8`
# as well. A request that would be judged by its body and arrives without
# one the policy can read is refused, not waved through.
deny contains "a body the policy cannot read" if {
	judged_by_body
	not is_object(input.Body)
}

judged_by_body if creating_container

judged_by_body if running_command

judged_by_body if creating_volume

deny contains "a privileged container" if {
	creating_container
	host.Privileged == true
}

# A directory of the server mounted, by either way of asking for it. A named
# volume has no slash in front.
deny contains "a directory of the server mounted" if {
	creating_container
	some bind in host.Binds
	source := split(bind, ":")[0]
	startswith(source, "/")
	not allowed_binds[source]
}

deny contains "a directory of the server mounted" if {
	creating_container
	some m in host.Mounts
	m.Type == "bind"
	not allowed_binds[m.Source]
}

# The paths of the server a container may be given: the daemon's own socket,
# which the agent's container holds and which leads back to this policy. A
# file of your own in compose.override.yml — a CA file, a Docker config.json —
# goes here too, or its mount is refused.
allowed_binds := {"/var/run/docker.sock", "/run/docker.sock"}

deny contains "another container's volumes" if {
	creating_container
	count(host.VolumesFrom) > 0
}

# A volume made of a directory of the server (`-o type=none -o o=bind
# -o device=/`) is that directory under a volume's name. A container's mount
# can carry the same options (`--mount type=volume,volume-opt=o=bind,…`), and
# the daemon then creates the volume on the way, with no request of its own
# for the policy to see. The agent creates its volumes first, without options.
deny contains "a volume made of a directory of the server" if {
	creating_volume
	count(input.Body.DriverOpts) > 0
}

deny contains "a volume made of a directory of the server" if {
	creating_container
	some m in host.Mounts
	count(object.get(m, ["VolumeOptions", "DriverConfig", "Options"], {})) > 0
}

deny contains "a namespace of the server" if {
	creating_container
	some mode in [
		object.get(host, "PidMode", ""),
		object.get(host, "NetworkMode", ""),
		object.get(host, "IpcMode", ""),
		object.get(host, "UsernsMode", ""),
		object.get(host, "UTSMode", ""),
		object.get(host, "CgroupnsMode", ""),
	]
	mode == "host"
}

# Docker's default set, which `security.capabilities` in deploy.yaml can only
# narrow. Anything added beyond it — SYS_ADMIN, SYS_PTRACE, ALL — is refused.
deny contains "a capability beyond Docker's default set" if {
	creating_container
	some cap in host.CapAdd
	not default_capabilities[trim_prefix(upper(cap), "CAP_")]
}

default_capabilities := {
	"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "MKNOD",
	"NET_BIND_SERVICE", "NET_RAW", "SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYS_CHROOT",
}

deny contains "a device of the server" if {
	creating_container
	count(host.Devices) > 0
}

deny contains "a device of the server" if {
	creating_container
	count(host.DeviceCgroupRules) > 0
}

deny contains "a container without Docker's confinement" if {
	creating_container
	some opt in host.SecurityOpt
	regex.match(`^((seccomp|apparmor|systempaths)[=:]unconfined|label[=:]disable)$`, opt)
}

# `systempaths=unconfined` does not arrive as that string: the docker command
# sends it as an empty MaskedPaths and an empty ReadonlyPaths, and any list
# given in either replaces the daemon's default set (/proc/kcore,
# /proc/sysrq-trigger, /proc/sys, /sys/firmware, …). Absent or null means
# that set, which is all the agent ever asks for.
deny contains "a container without Docker's confinement" if {
	creating_container
	some field in ["MaskedPaths", "ReadonlyPaths"]
	is_array(host[field])
}

deny contains "a privileged command in a container" if {
	running_command
	input.Body.Privileged == true
}

# Swarm starts containers by another request, and a plugin runs with the
# mounts it asks for: neither has a place on a server Shipwick runs.
deny contains "swarm or plugins" if {
	section in {"swarm", "services", "tasks", "nodes", "secrets", "configs", "plugins"}
}

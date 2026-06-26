# Orca

A container runtime written in Go.  
Orca can pull OCI/Docker images from a registry, run containers with Linux namespaces, cgroups, and a custom bridge network, all without dockerd or containerd.

## Features

- **Image management** - `pull`, `verify`, `images`, `rm` (remove image or container)
- **Container lifecycle** - `run` (foreground or detached), `containers`, `rm`
- **OverlayFS** - efficient layered root filesystem
- **Networking** - bridge network (`orca0`) with automatic IP allocation and port mapping
- **Resource limits** - CPU, memory, and PID limits via cgroups v2
- **Garbage collection** - removes unused image layers and blobs

## Requirements

- Linux
- iproute2
- iptables
- Root privileges
- Go to build from source

## Build

```bash
git clone https://github.com/ahmed0427/orca.git
cd orca
make # go build
```

## Usage

All commands require root. Run `orca` without arguments to see help.

```bash
sudo ./orca pull alpine
sudo ./orca run alpine echo "Hello from container"

sudo ./orca pull busybox:musl
sudo ./orca run -d -p 8080:8000 busybox:musl httpd -f -p 8000 -h .

sudo ./orca images
sudo ./orca containers
sudo ./orca rm <container-id>
sudo ./orca gc # remove unused blobs and layers
```

### Run options

| Flag         | Description                          |
| ------------ | ------------------------------------ |
| `-i`         | Keep stdin open (default true)       |
| `-t`         | Allocate a pseudo-TTY (default true) |
| `-d`         | Run container in background          |
| `-p`         | Port mapping (`host:container`)      |
| `--name`     | Assign a name to the container       |
| `--hostname` | Container host name                  |
| `--memory`   | Memory limit (e.g. `256m`)           |
| `--cpu`      | CPU limit (e.g. `1.5`)               |
| `--pids`     | Max number of processes              |

## Storage

All data is stored under `/var/orca`:

- `/var/orca/blobs` - compressed image layers and config blobs
- `/var/orca/layers` - extracted layer directories
- `/var/orca/tags` - image tag manifests
- `/var/orca/containers` - container state and overlay mounts

## Networking

Orca creates a bridge `orca0` (`10.200.0.0/16`) and places each container in its own network namespace with a veth pair. Port mapping uses iptables DNAT rules.
To tear down the bridge and all iptables rules, remove all containers with `orca rm all`.

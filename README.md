# Orca

**Orca** is a lightweight, educational container runtime built in Go.
It aims to demonstrate how containers work under the hood by implementing core containerization features from scratch.

## Current State & Features

The project currently supports basic container operations by interacting with OCI-compliant registries (like Docker Hub)
and using Linux-native features for isolation.

### Image Management

- **Pulling Images**: Download manifests, configurations, and layers directly from container registries.
- **Layer Extraction**: Unpacks compressed layers into a local store.
- **Storage Management**:
  - `images`: List locally available image tags.
  - `verify`: Check the structural integrity and hash consistency of downloaded images.
  - `rm`: Remove specific image tags.
  - `gc`: Garbage collection to prune unused layers and blobs.

### Container Runtime

- **Process Isolation**: Uses Linux **Namespaces** (UTS, PID, NS, NET) to isolate the containerized process.
- **Root Filesystem**: Implements **Chroot** to change the root directory for the container process.
- **Layered Filesystem**: Uses **OverlayFS** to combine multiple image layers into a single, unified, and writable root filesystem for each container.
- **Resource Limits**: Initial support for **Cgroups (v2)** to manage memory, CPU, and PID limits.
- **Hostname Control**: Automatically sets the container hostname to a unique ID.

## Project Structure

- `cmd/orca/`: The main entry point and CLI command definitions.
- `pkg/image/`: Logic for interacting with registries, managing the local image store, and layer extraction.
- `pkg/container/`: Core runtime logic including namespace setup, cgroups, and mounting filesystems.
- `pkg/progress/`: Utilities for rendering download and extraction progress bars.

## Roadmap / Planned Work

- [ ] Improved Cgroup v2 resource limit configuration.
- [ ] Network interface setup (veth pairs/bridge) for container connectivity.
- [ ] Support for interactive terminal sessions (`-it`).
- [ ] Volume mounting support.
- [ ] Better handling of OCI image specifications.

## Prerequisites

- Linux (required for Namespaces, Cgroups, and OverlayFS).
- Root privileges (required for mounting and namespace operations).
- Go 1.21+ (for building).

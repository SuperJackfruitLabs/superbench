---
title: Install v0.0.1
description: Download SuperBench v0.0.1 from its GitHub release, verify it and stage the Linux service for review. An experimental single-host release for Linux amd64, on a disposable or staging host.
status: wet paint
---

SuperBench v0.0.1 is the first release, tagged on 2026-11-22. It is an experimental single-host
release, not production software: install it on a disposable or staging Linux host, for trusted
repositories only. It is not a supported installation, and there is no promise of compatibility
between releases. Read [Status](/start/status/), the [Security model](/start/security-model/) and
[Limits](/start/limits/) first.

## What the release contains

The [GitHub release](https://github.com/SuperJackfruitLabs/superbench/releases/tag/v0.0.1) has
two files:

| File | What it is |
| --- | --- |
| `superbench_linux_amd64.tar.gz` | `bench`, `benchd`, `bench-quota` and `bench-payload`, the four commands the managed installer needs. Built with CGO off, so every binary is static, and reproducible from the tag. |
| `SHA256SUMS` | The archive's SHA-256 digest. |

A build-provenance attestation for the archive is published with the release. The archive is for
Linux amd64 only: there is no macOS, arm64 or Windows build, and no package for any package
manager. The installer, `deploy/linux/install.py`, is not in the archive; it comes from the
source at the tag.

## Before you start

- A disposable or staging Linux amd64 host with systemd, delegated cgroup v2, Python 3, Git and
  sudo. Supervised runs and setup need the delegated cgroup.
- For managed storage (per-bench byte and inode caps), a separately mounted XFS filesystem with
  enforcing project quotas, and the XFS tools. Without it, quotas are recorded and not enforced.
- Direct root (a root login, not `sudo`) for the live installation steps.
- Trusted repositories. Leases are cooperative, and nothing in SuperBench isolates untrusted code.

## Download and verify

With the GitHub CLI:

```sh
mkdir -p /absolute/build && cd /absolute/build
gh release download v0.0.1 -R SuperJackfruitLabs/superbench \
  -p superbench_linux_amd64.tar.gz -p SHA256SUMS
sha256sum --check SHA256SUMS
gh attestation verify superbench_linux_amd64.tar.gz -R SuperJackfruitLabs/superbench
```

Or with curl, and the same `sha256sum --check SHA256SUMS`:

```sh
curl -fLO https://github.com/SuperJackfruitLabs/superbench/releases/download/v0.0.1/superbench_linux_amd64.tar.gz
curl -fLO https://github.com/SuperJackfruitLabs/superbench/releases/download/v0.0.1/SHA256SUMS
sha256sum --check SHA256SUMS
```

Stop if either check fails. Then unpack the four commands and confirm the version:

```sh
mkdir -p /absolute/build/bin
tar -xzf superbench_linux_amd64.tar.gz -C /absolute/build/bin
/absolute/build/bin/bench version     # bench 0.0.1
```

## Stage the service for review

Fetch the installer from the source at the same tag, then stage the managed installation under a
private directory. Staging writes only below that root, creates no account and starts no
service:

```sh
git clone --depth 1 --branch v0.0.1 https://github.com/SuperJackfruitLabs/superbench.git /absolute/superbench-v0.0.1
cd /absolute/superbench-v0.0.1
python3 deploy/linux/install.py --bin-dir /absolute/build/bin \
  --root /absolute/private/staging --managed
```

Review the staged files, owners, modes, the systemd unit, the wrapper and the sudo rule before
applying anything. Omit `--managed` for an unmanaged host, which installs neither the payload
helper nor its sudo grant.

## Install on the host

The live installation is the repository's
[pilot installation guide](https://github.com/SuperJackfruitLabs/superbench/blob/v0.0.1/docs/pilot-installation.md)
at the tag, with `/absolute/build/bin` as `--bin-dir`. Read it before installing anything. In
outline:

1. Mount the dedicated XFS data volume persistently, without `nouuid`.
2. Write the reviewed byte and inode budgets to a root-owned file.
3. As direct root, run the installer with `--root /` and `--managed --prepare --identity --activation --storage`.
   It prepares the `superbench` and `superbench-payload` accounts and the directories, writes the
   activation file with every v2 switch off, and does not enable or start the service.
4. Start the service yourself with every switch off, and turn on a switch only after the guide's
   checks for it pass.

A ledger initialized by an earlier, unreleased revision is refused; the message points to the
guide's "Reinitialize the indexed ledger". To replace the four executables later, the guide's
"Upgrade the executables" uses the installer's `--upgrade` with `--previous`, `--sums` and
`--backup`. A successful install and matching digests are not evidence that a task works end to
end.

## What v0.0.1 does not do

- It runs on one host. Moving a bench to another host is a reviewed administrative step, not
  network coordination.
- It does not isolate untrusted code. Supervised runs are fenced in a cgroup, which is not a
  sandbox, and the separate payload user stays off until an operator turns on its switch; it is
  not proven end to end.
- Leases are cooperative and never expire on their own.
- Remote authentication, a web interface and suite integrations are not built.

[Status](/start/status/) lists everything that is partial or not built.

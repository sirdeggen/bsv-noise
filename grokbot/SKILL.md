---
name: bsv-noise
description: Deploy or operate a self-hosted BSV noise / covering-fire service on the owner's VPS. Use when the owner wants this repository installed on a fresh Ubuntu LTS box, help with DNS and HTTPS, funding the identity via Message Box or PeerPay, optional handle registration, health checks, backups, upgrades, or rollback. Do not use for unrelated BSV wallet work, for spending the owner's funds except into this service's identity, or for changing some other already-running deployment the owner did not name.
---

# BSV Noise — deploy and operate

This skill is for installing **this** repository on a VPS the owner controls, then helping them fund and run it. The product keeps a trickle of ordinary-looking BRC-29 payments moving on chain — covering fire, a blanket of background traffic so real commerce is a needle in a haystack. It is a temporary measure until everyday BSV cash adoption provides that traffic on its own.

This is privacy, not anonymity. Both parties know who they are dealing with, and traditional tracing still works. Do not describe the service as a way to hide counterparties or to present recycled coins as sales.

Keep this skill generic. Do not hard-code another operator's domain, IP, identity public key, or wallet.

## Hard rules

- Never print, copy, or paste `SERVER_PRIVATE_KEY` or the file at `secrets/server_private_key`. Generate the key on the VPS, mode `600`. If a command would dump it, stop.
- Do not register a paymail handle, send funds, or enable mainnet broadcast unless the owner explicitly approved that step in this conversation.
- Do not acknowledge, delete, or drop Message Box messages except by running this service's own internalization path (it acks only after `InternalizeAction` is accepted).
- Do not point the console port `8741` at `0.0.0.0`. Stay on loopback and put Caddy in front.
- Default install is paused: BSV **testnet**, `BROADCAST=false`, `IMPORT_FUNDING=false`, `ALLOW_MAINNET_BROADCAST=false`. Mainnet broadcast requires all three of `NETWORK=main`, `ALLOW_MAINNET_BROADCAST=true`, and `BROADCAST=true`.
- Do not modify some other live deployment of this software unless the owner named that host and asked you to change it.

## 1. Ask the owner before you touch a box

Collect, and wait for, all of:

- SSH access to the VPS (host, user, port, and an SSH key or another approved login method). Prefer a key the owner provides or that already exists on the machine you are working from. Do not invent credentials.
- Confirmation the box is a fresh (or acceptable) **Ubuntu LTS** VPS and that you may install Docker and Caddy.
- Optional domain name for HTTPS (for example `noise.example.com`).
- Which network: testnet (default) or mainnet (opt-in). Even on mainnet, start paused until funded unless they say otherwise.
- How they will fund the identity (their BRC-29 / PeerPay wallet). A plain P2PKH address is the wrong funding target.

If any of that is missing, ask. Do not start the installer.

## 2. Get the code onto the VPS

The repository is often **private**. Do not advertise an unauthenticated `curl | bash` one-liner.

- Clone with the owner's GitHub credentials or deploy key (`git clone git@github.com:<owner>/bsv-noise.git`), or copy this checkout with `scp` / `rsync`.
- Run everything as a sudoer, from the checkout:

```bash
sudo ./install.sh --domain noise.example.com
```

Omit `--domain` if they have no hostname yet. Add `--network main` only if they chose mainnet (broadcast still stays off unless they also asked for it). Add `--import-funding` if they want the process to internalize Message Box payments immediately after install.

The installer:

- Installs Docker Engine, the Compose plugin, and Caddy (unless `--skip-caddy`).
- Writes files under `/opt/bsv-noise` by default.
- Generates the server key on the VPS if one is not already there, mode `600`, and never prints it.
- Builds `bsv-noise:local` and starts the compose file with paused defaults.
- Prints the console URL and the **identity public key** from `/healthz`.

Re-running the installer must not rotate an existing key.

## 3. DNS and HTTPS

If they gave a domain:

- Show the public IPv4 (and IPv6, if any) of the VPS.
- Ask them to create an A record, and an AAAA record when they have IPv6, from the domain to those addresses.
- Wait until the name resolves before expecting a Caddy certificate to succeed.
- Caddy site file: `/etc/caddy/sites/bsv-noise.caddy`, imported from `/etc/caddy/Caddyfile`. Validate and `systemctl reload caddy` after DNS works.
- Confirm `https://<domain>/` serves the console and that `8741` is not reachable on the public interface.

Without a domain, use an SSH tunnel: `ssh -L 8741:127.0.0.1:8741 user@vps` and open `http://127.0.0.1:8741/`.

## 4. Verify health

```bash
curl -fsS http://127.0.0.1:8741/healthz
curl -fsS http://127.0.0.1:8741/api/stats
docker compose --env-file /opt/bsv-noise/.env -f /opt/bsv-noise/docker-compose.yml ps
```

`/healthz` must be HTTP 200 and include `identity_key`. The document may also include an internal build marker used by the container health check — that is how the binary identifies itself, not a product name.

Report to the owner: console URL, identity public key, network, whether broadcast and import are on, and any health error. Do not report the private key.

## 5. Help them fund

1. Turn on `IMPORT_FUNDING=true` in `/opt/bsv-noise/.env` (keep `BROADCAST=false`) and recreate: `docker compose --env-file /opt/bsv-noise/.env up -d`.
2. Give them the identity public key and the Message Box host (`MESSAGE_BOX_URL`, box name `payment_inbox`).
3. They pay that identity from a BRC-29 / PeerPay / BRC-100 wallet. The console top-up button can do this from their local wallet.
4. Watch `/api/stats` or the console until the balance reflects the payment. Do not ack or delete inbox messages by hand.
5. Only after the owner confirms the fund landed, and only if they asked to go live, set `BROADCAST=true`. On mainnet also require `NETWORK=main` and `ALLOW_MAINNET_BROADCAST=true`. Recreate the container. See `deploy/mainnet.env.example`.

Do not send funds yourself unless the owner clearly asked you to pay **this** identity, named the source, and approved the amount.

## 6. Optional handle registration

Handles are optional. Funding works with the raw identity key.

Prerequisites, which you should dry-run and show the owner first:

- DNS SRV `_bsvalias._tcp.<domain>` pointing at the Message Box host (for the public host, `messagebox.bsvblockchain.tech`).
- The registry operator must allow that domain.
- The handle must be available.

```bash
cd /opt/bsv-noise/scripts/register-handle
npm ci
node register-handle.mjs --handle <name> --domain <example.com> \
  --key-file /opt/bsv-noise/secrets/server_private_key
```

Default is dry-run (no PUT). Add `--execute` only after the owner explicitly approves registration. Never print the key file.

## 7. Routine health checks

If the owner asks for a recurring check, look at:

- Container running and `/healthz` 200.
- Console or `/api/stats`: balance, in-flight vs `MaxInFlight`, seen vs confirmed, recent failures, accepted TPS.
- Disk for `/opt/bsv-noise/data`.
- Caddy and the certificate expiry if a domain is in use.
- Whether daily spend/fee caps are close to being hit.

Alert on: container restart loops, rising failures, `in_doubt` sending state (this **blocks new sends** on purpose), balance below `reserve_sat`, certificate errors, or a public `8741`. Do not rotate the key or restore a backup without approval.

A reasonable cadence is every few hours or daily, as a Grok Bot routine the owner enables. Keep the routine generic to **their** host and domain, which they must supply.

## 8. Backup, upgrade, rollback

- Backup: `sudo /opt/bsv-noise/scripts/backup.sh`. For a consistent SQLite copy, `docker compose stop` first. Backups include `data/` and a mode-`600` copy of `secrets/` — do not dump those files into chat.
- Upgrade: backup, `docker tag bsv-noise:local bsv-noise:previous`, `git pull`, rebuild with `docker buildx build --network=host --load -t bsv-noise:local .`, `docker compose up -d --force-recreate`. Keep the data volume and secrets.
- Rollback: `docker tag bsv-noise:previous bsv-noise:local` and recreate. To restore files, stop, copy `data/` and `secrets/` back, start.

Details and known limitations (single-wallet linkability, Benford change not yet on-chain, dust sweep shape, confirmed lagging seen, partial GorillaPool Arcade failover) are in the repository `README.md`. Read that before promising on-chain privacy properties.

## What not to change

Do not loosen the mainnet opt-in flags, do not commit `.env` or `secrets/`, and do not "helpfully" switch `UTXO_STRATEGY` to throughput or raise caps without the owner asking. This skill deploys and operates the published tree; it is not a license to redesign the wallet.

This repository is licensed under the Apache License 2.0. Copyright 2026 deggen.

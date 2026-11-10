# BSV Noise

A self-hosted BSV service that keeps a trickle of ordinary-looking BRC-29 payments moving on chain — covering fire, a blanket of background traffic so real commerce is a needle in a haystack.

The process is a **dry-run by default**. It does not broadcast, and it does not call a Message Box host, until you turn those switches on. Mainnet broadcast is an explicit opt-in (`NETWORK=main` **and** `ALLOW_MAINNET_BROADCAST=true` **and** `BROADCAST=true`).

A health console is served at `/`. Internal command names may still say `brc29-loadgen`; that is the binary inside the image, not the product name.

## Why

BSV's privacy comes from pseudonymity through obscurity. A payment is a needle in a haystack, which makes it impractical for a third party to follow someone's spending. That is the privacy this service is here to support.

Privacy means both parties know who they are doing business with, and traditional tracing and police work can still find bad actors. Anonymity — not knowing who you are dealing with, and losing that tracing ability — is a different thing, and not what this service is for.

While everyday use of BSV as cash is still growing, this service provides a trickle of payment-like noise so real commerce is indistinguishable, to a third party, from the background traffic. It is a temporary measure until organic adoption provides that haystack on its own.

Payments go only to recipient keys this process generated, or to identity keys you explicitly mark as controlled. Recycling satoshis between keys you control is a self-controlled transfer; it is not evidence of commerce.

## What you run

One Go daemon, one SQLite file for the generator (`loadgen.sqlite`), and, only when import or broadcast is on, a second SQLite file for `go-wallet-toolbox` (`wallet.sqlite`).

| Mode | Message Box | Chain broadcast |
| --- | --- | --- |
| `dry-run` (default) | not contacted | no |
| `import-only` (`IMPORT_FUNDING=true`, `BROADCAST=false`) | lists `payment_inbox`, internalizes, acknowledges | the wallet may rebroadcast an **unproven** funding tx; this process does not create spends |
| `broadcast` | optional, if import is also on | `CreateAction` submits BSV transactions |

`NETWORK` is `test` or `main` (BSV). Mainnet broadcast also requires `ALLOW_MAINNET_BROADCAST=true`. The binary will refuse to start a mainnet broadcaster without that flag.

## Requirements

- A VPS running **Ubuntu LTS** (the installer targets the current LTS releases).
- Roughly 1 vCPU and 2 GiB RAM is enough for the default 0.5 TPS; give the box a few gigabytes of disk for the wallet database and logs.
- Root or passwordless `sudo`.
- Outbound HTTPS (Docker Hub / Debian, Message Box, Arcade, WhatsOnChain).
- Optional but recommended: a DNS name pointing at the VPS, so Caddy can issue a TLS certificate. Without a domain the console is only on `127.0.0.1:8741` (reach it with an SSH tunnel).
- A BRC-29 / PeerPay capable wallet so you can **fund the service identity**. A plain P2PKH address is not a Message Box.

To build from source without Docker you need Go 1.27.3 and a C compiler (`mattn/go-sqlite3`).

## One-command install (Ubuntu LTS)

This repository is often private. Clone it with credentials, then run the installer **on the VPS**. The installer installs Docker and Caddy, generates the server key **on the VPS** (mode `600`, never printed), writes a paused-until-funded config, builds the image, starts the service, and prints the console URL plus the **identity public key** to fund.

```bash
sudo ./install.sh --domain noise.example.com
```

Defaults: `NETWORK=test`, `BROADCAST=false`, `IMPORT_FUNDING=false`, `ALLOW_MAINNET_BROADCAST=false`.

Useful flags:

```text
--network main                 # still will not broadcast without the flags below
--import-funding               # start internalizing Message Box payments
--enable-broadcast             # start creating on-chain payments
--allow-mainnet-broadcast      # required together with --network main --enable-broadcast
--dir /opt/bsv-noise
--skip-caddy
```

After install, point an A (and optional AAAA) record at the VPS if you have not already, then `sudo systemctl reload caddy`.

Manual Docker path (same defaults):

```bash
mkdir -p secrets data
umask 077
openssl rand -hex 32 > secrets/server_private_key
chmod 600 secrets/server_private_key
# never cat the key
sudo chown 65532:65532 data
docker compose up --build -d
curl -s http://127.0.0.1:8741/healthz
```

The image runs as uid 65532. Compose publishes `127.0.0.1:8741` and sets `LISTEN=0.0.0.0:8741` inside the container. Do not publish 8741 on `0.0.0.0`.

A Caddy example lives at `deploy/Caddyfile.example`.

## Funding via Message Box / PeerPay

A plain Bitcoin address is the wrong thing to hand a funder. Funders use BRC-29 / PeerPay:

1. Start the process and read `identity_key` from `/healthz` (or the installer's summary). That is the compressed public key of `SERVER_PRIVATE_KEY`.
2. From a BRC-29 wallet, pay that **identity** on the configured Message Box host (`MESSAGE_BOX_URL`, default `https://messagebox.bsvblockchain.tech`). The box name is `payment_inbox`. The console top-up button does this with the operator's local BRC-100 wallet.
3. The payment body is a PeerPay token: `customInstructions.derivationPrefix` and `derivationSuffix` (canonical base64), `transaction` as a JSON array of bytes `0`–`255` (Atomic BEEF), `amount`, and optional `outputIndex` (default 0). A base64 string in `transaction` is rejected.
4. With `IMPORT_FUNDING=true` the daemon authenticates to the box with BRC-31 (`AuthFetch`, same key), lists the box, and calls `wallet.InternalizeAction`.
5. It acknowledges the message only after the wallet returns `Accepted`, or, if a previous accept already happened, it only retries the acknowledge. A failed import is left on the server. The same `txid:vout` is not credited twice.

`InternalizeAction` in go-wallet-toolbox v0.191.4 verifies the Atomic BEEF. If the transaction is unknown and has no merkle proof, storage may broadcast it. Treat import as a live wallet operation.

A reasonable "paused until funded" sequence on a new box:

1. Install with defaults (or `--network main` **without** broadcast).
2. Turn on `IMPORT_FUNDING=true` and recreate the container.
3. Fund the printed identity.
4. Confirm the balance on the console.
5. Only then set `BROADCAST=true`. On mainnet also set `NETWORK=main` and `ALLOW_MAINNET_BROADCAST=true`. See `deploy/mainnet.env.example`.

`SERVER_PRIVATE_KEY` is the BRC-42 root. Its public key is the Message Box recipient, the user key passed to the wallet, and the identity used for local storage. Keep one key, on the VPS, mode 600, never in git, never pasted into chat.

## Broadcasters: Arcade, GorillaPool Arcade, WhatsOnChain

Built-in wallet services (do not point `ARC_URL` at TAAL unless you intend to override this):

- **Primary Arcade** — toolbox default (`arcade-v2-us-1`). Broadcast, status, and SSE `/events` for merkle proofs.
- **GorillaPool Arcade** — `https://arcade.gorillapool.io`, used as a status / rebroadcast fallback. go-wallet-toolbox v0.191.4 has a single Arcade primary and no public hook to add a second Arcade-type host to the broadcast router, so this is not a second fully wired toolbox failover.
- **WhatsOnChain** — remains enabled as a general failover (headers, status).
- **TAAL ARC** and **GorillaPool classic ARC** are disabled. GorillaPool's host now runs Arcade; the ARC client is not Arcade-compatible.

`ARC_URL` / `ARC_TOKEN` override the primary ARC/Arcade endpoint only if you need that. The token is never logged.

A payment moves to **seen** when Arcade reports `SEEN_ON_NETWORK`, `SEEN_MULTIPLE_NODES`, `MINED`, or `IMMUTABLE`. That frees a `MaxInFlight` slot. **Confirmed** means mined with a merkle path. `REJECTED` is a failure.

## Cost and fees

Defaults (sats, BSV):

| Setting | Default | Meaning |
| --- | --- | --- |
| `target_tps` | 0.5 | Average offered rate. Realistic timing still averages around this. |
| `min_amount` / `max_amount` | 100 / 2000 | Payment size before fees. |
| `sat_per_kb` | 100 | Standard fee rate. |
| `min_fee_sat` / `max_fee_sat` | 1 / 2000 | Fee clamp. |
| `daily_spend_cap` | 100_000_000 | 1 BSV per UTC day, including payments. |
| `daily_fee_cap` | 1_000_000 | 0.01 BSV per UTC day in fees. |
| `reserve_sat` | 50_000 | Leave this much unspent. |
| `max_in_flight` | 4 | Concurrent un-seen payments. |
| `dust_sat` | 1000 | Below this, change is treated as dust. |

At 0.5 TPS the box would *offer* on the order of 40,000 payments per day if it ran flat out and stayed funded. BSV fees are small per transaction, but volume adds up — the daily caps are there so a leftover process cannot empty the wallet overnight. Size the fund to the cap you actually want, and start below 0.5 TPS if you are unsure.

There is no protocol fee to this software. You pay only miner / broadcaster policy fees and whatever you spend to controlled recipients (which, unless you move those coins onward, stay in keys this process made).

## Optional handle registration

You can attach a paymail-style handle (for example `alice@example.com`) to the service identity on the Message Box handle registry. This is optional. Funding works with the raw identity public key.

1. Publish a DNS SRV record: `_bsvalias._tcp.example.com` → the Message Box host (for the public host, `messagebox.bsvblockchain.tech`). The registry operator must also allow your domain.
2. Dry-run first (never prints the key):

```bash
cd scripts/register-handle
npm ci
node register-handle.mjs --handle alice --domain example.com --key-file /opt/bsv-noise/secrets/server_private_key
```

3. Only after the dry-run is clean, and with the operator's explicit approval, add `--execute`.

## Safety caps

- Dry-run / paused defaults; mainnet broadcast is a three-flag opt-in.
- `controlled_recipients=true` is required before broadcast. The process will not pay keys it does not control.
- Daily spend and fee caps, a reserve, and `MaxInFlight`.
- Console bound to `127.0.0.1`; Caddy terminates TLS. Optional `CONSOLE_TOKEN` / `METRICS_TOKEN`.
- The server key never lives in YAML. It comes from the environment or a secret file and is not retained in the YAML struct.
- In-flight dry-run reservations that never committed are released on the next open. A live attempt that was in `sending` when the process died becomes `in_doubt` and **blocks new sends** so the same inputs are not spent again.

## Upgrade, backup, rollback

Backup (does not print secrets):

```bash
sudo /opt/bsv-noise/scripts/backup.sh
# default destination: /var/backups/bsv-noise-<timestamp>
```

For a perfectly consistent SQLite snapshot, stop the container first (`docker compose stop`), copy, then start.

Upgrade:

```bash
cd /opt/bsv-noise
sudo /opt/bsv-noise/scripts/backup.sh
docker tag bsv-noise:local bsv-noise:previous
git pull
docker buildx build --network=host --load -t bsv-noise:local .
docker compose --env-file .env up -d --force-recreate
# volumes and secrets are unchanged
```

Rollback:

```bash
docker tag bsv-noise:previous bsv-noise:local
docker compose --env-file /opt/bsv-noise/.env up -d --force-recreate
```

To restore data, stop the container, copy `data/` and `secrets/` back from the backup directory (keep mode 600 on the key), and start again. Never overwrite a live `wallet.sqlite` while the container is writing to it.

## Known limitations

- **Single-wallet linkability.** Every payment is signed by one identity. Controlled recipients do not spend onward, so an observer who clusters the graph can often tell the coins are circling one operator. Output shuffling, a realistic amount/timing distribution, a higher dust threshold, and a target of several change outputs make the *shape* of each transaction closer to ordinary commerce, but they do not make a single wallet look like many independent businesses.
- **Benford change allocation is implemented in this repo but is not yet on-chain.** The TypeScript SDK has a working Benford change distribution; `go-sdk`'s `ChangeDistributionRandom` is not implemented, so change amounts do not yet follow that law in the live Go wallet path.
- **Dust sweeping is coarser than intended.** Sweeps cannot pass pre-selected inputs into `CreateAction` (those inputs would lack a BRC-29 unlocking template and would fail to sign). The funder currently picks a mid-size coin, so a sweep tends to be one input plus an `OP_FALSE OP_RETURN` burn and change, not a batch of eight dust coins. The `dust_swept` counter can over-count relative to how many tiny coins actually disappeared.
- **Confirmed can lag seen.** "Seen" frees in-flight once Arcade has the transaction on the network. "Confirmed" waits for a merkle path (Arcade SSE / `CheckForProofs`, with a poll fallback). Older unproven rows can sit without a path for a long time even while new payments confirm.
- **GorillaPool Arcade is only a partial failover**, for the toolbox reason above.
- **One process, one box, one SQLite wallet.** There is no built-in multi-node HA. The wallet DSN uses a single open connection and a busy timeout; do not point a second writer at the same files.
- The health document still carries an internal build marker so the container health check can identify this binary. That marker is not a product name.

## Repository layout

```text
install.sh                 Ubuntu LTS installer
Dockerfile                 multi-stage console + Go binary
docker-compose.yml         testnet / paused defaults
deploy/Caddyfile.example   HTTPS on a custom domain
deploy/mainnet.env.example explicit mainnet flags
scripts/backup.sh
scripts/register-handle/   optional handle registration
grokbot/SKILL.md           instructions for another Grok Bot to deploy this
cmd/brc29-loadgen          daemon
cmd/list-inbox             read-only Message Box inspector
```

## License

[Open BSV License Version 5](./LICENSE), the same license used by the BSV Association SDKs and by other BSV work under this account. The software, and any software derived from it, must only be used on the BSV blockchains (main and test), as defined in that license.

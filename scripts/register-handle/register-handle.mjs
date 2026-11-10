#!/usr/bin/env node
/**
 * Register (or dry-run) a paymail handle on a go-message-box-server handle registry.
 *
 * Mirrors @bsv/expo-wallet-toolbox handle-registry client, registration,
 * profile certificate, and rules.
 *
 * Live write path (NOT used unless --execute):
 *   1. ProtoWallet(PrivateKey) as ProfileSigner
 *   2. buildProfileCertificate({ paymail, issuedAt, displayName? })
 *   3. PUT {REGISTRY}/api/handle  body=ProfileCertJson  (no AuthFetch; cert is the proof)
 *
 * Read-only checks always run; --dry-run (default) never PUTs.
 *
 * Usage:
 *   node register-handle.mjs --handle alice --domain example.com --dry-run
 *   node register-handle.mjs --handle alice --domain example.com --execute
 *
 * --execute performs a real registration and requires the operator's explicit approval.
 *
 * Env / flags:
 *   --handle, --domain            required
 *   --registry                    default https://messagebox.bsvblockchain.tech
 *   --key-file or SERVER_PRIVATE_KEY_FILE
 *                                 default ../../secrets/server_private_key (from this script)
 *   --expect-identity             optional; defaults to the public key derived from the key file
 *   --display-name                optional
 *
 * The private key is never printed.
 */
import { readFileSync, existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { Certificate, PrivateKey, ProtoWallet, Random, Utils } from '@bsv/sdk'

const PROFILE_CERT_TYPE = 'SbatVXXssDW3AO0J9bxIljkHGbPBGCVAXg94gXFf0cE='
const ZERO_OUTPOINT = `${'0'.repeat(64)}.0`
const HANDLE_FORMAT = /^[a-z0-9](?:[a-z0-9._-]{0,30}[a-z0-9])?$/
const MIN_HANDLE_LEN = 3
const HERE = dirname(fileURLToPath(import.meta.url))
const DEFAULT_KEY = resolve(HERE, '../../secrets/server_private_key')

function arg(name, fallback) {
  const i = process.argv.indexOf(name)
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback
}
function has(flag) {
  return process.argv.includes(flag)
}

const EXECUTE = has('--execute')
const DRY = !EXECUTE
const HANDLE = (arg('--handle', '') || '').trim().toLowerCase()
const DOMAIN = (arg('--domain', '') || '').trim().toLowerCase()
const REGISTRY = arg('--registry', 'https://messagebox.bsvblockchain.tech').replace(/\/+$/, '')
const DISPLAY = arg('--display-name', '')
const KEY_FILE = arg('--key-file', process.env.SERVER_PRIVATE_KEY_FILE || DEFAULT_KEY)

if (!HANDLE || !DOMAIN) {
  console.error('usage: node register-handle.mjs --handle <name> --domain <example.com> [--dry-run|--execute]')
  process.exit(2)
}

const paymail = `${HANDLE}@${DOMAIN}`
const EXPECT_FLAG = (arg('--expect-identity', '') || '').trim().toLowerCase()

function ok(label, detail) {
  console.log(`OK  ${label}${detail !== undefined ? ': ' + detail : ''}`)
}
function warn(label, detail) {
  console.log(`WARN ${label}${detail !== undefined ? ': ' + detail : ''}`)
}
function fail(label, detail) {
  console.log(`FAIL ${label}${detail !== undefined ? ': ' + detail : ''}`)
}

async function httpJson(url, opts = {}) {
  const res = await fetch(url, {
    ...opts,
    headers: { accept: 'application/json', ...(opts.headers || {}) },
    signal: AbortSignal.timeout(20_000)
  })
  const text = await res.text()
  let body
  try {
    body = text ? JSON.parse(text) : null
  } catch {
    body = text
  }
  return { status: res.status, headers: res.headers, body, date: res.headers.get('date') }
}

async function checkSrv(domain) {
  const name = `_bsvalias._tcp.${domain}`
  const url = `https://cloudflare-dns.com/dns-query?name=${encodeURIComponent(name)}&type=SRV&do=1`
  const res = await fetch(url, { headers: { accept: 'application/dns-json' }, signal: AbortSignal.timeout(15_000) })
  const j = await res.json()
  const answers = (j.Answer || []).filter((a) => a.type === 33)
  return { status: j.Status, ad: j.AD, answers, raw: j }
}

function loadKey(path) {
  if (!existsSync(path)) throw new Error(`key file missing: ${path}`)
  const raw = readFileSync(path, 'utf8').trim()
  if (/^[0-9a-fA-F]{64}$/.test(raw)) return PrivateKey.fromString(raw, 'hex')
  return PrivateKey.fromWif(raw)
}

async function buildCert(signer, paymailAddr, issuedAt, displayName) {
  const fields = {
    paymail: paymailAddr.toLowerCase(),
    issuedAt: issuedAt.toISOString()
  }
  if (displayName && displayName.trim()) fields.displayName = displayName.trim()
  const { publicKey } = await signer.getPublicKey({ identityKey: true })
  const certificate = new Certificate(
    PROFILE_CERT_TYPE,
    Utils.toBase64(Random(32)),
    publicKey,
    publicKey,
    ZERO_OUTPOINT,
    fields
  )
  await certificate.sign(signer)
  return {
    type: certificate.type,
    serialNumber: certificate.serialNumber,
    subject: certificate.subject,
    certifier: certificate.certifier,
    revocationOutpoint: certificate.revocationOutpoint,
    fields: certificate.fields,
    signature: certificate.signature ?? ''
  }
}

async function main() {
  console.log('=== register-handle ===')
  console.log(`mode: ${DRY ? 'DRY-RUN (no PUT)' : 'EXECUTE (WILL PUT /api/handle)'}`)
  console.log(`paymail: ${paymail}`)
  console.log(`registry: ${REGISTRY}`)
  console.log(`key file: ${KEY_FILE} (contents not printed)`)
  console.log('')

  const blockers = []

  if (!(HANDLE.length >= MIN_HANDLE_LEN && HANDLE_FORMAT.test(HANDLE))) {
    fail('handle format', HANDLE)
    blockers.push('invalid handle format')
  } else ok('handle format', HANDLE)

  try {
    const srv = await checkSrv(DOMAIN)
    const targets = srv.answers.map((a) => a.data)
    console.log('SRV answers:', targets.join(' | ') || '(none)', `AD=${srv.ad}`)
    const hit = targets.some((d) => String(d).toLowerCase().includes('messagebox.bsvblockchain.tech'))
    if (hit) ok('DNS SRV _bsvalias', 'points at messagebox.bsvblockchain.tech')
    else {
      fail('DNS SRV _bsvalias', 'does not point at messagebox.bsvblockchain.tech')
      blockers.push('SRV mismatch — add _bsvalias._tcp SRV to the Message Box host before registering')
    }
  } catch (e) {
    fail('DNS SRV lookup', e.message)
    blockers.push('SRV lookup failed')
  }

  try {
    const av = await httpJson(`${REGISTRY}/api/handle/available/${encodeURIComponent(HANDLE)}`)
    console.log('GET /api/handle/available/' + HANDLE, av.status, JSON.stringify(av.body))
    if (av.status === 200 && av.body && av.body.available === true) ok('availability', 'available')
    else if (av.status === 200 && av.body && av.body.available === false) {
      warn('availability', `unavailable reason=${av.body.reason}`)
      blockers.push(`handle unavailable: ${av.body.reason}`)
    } else {
      fail('availability', `unexpected ${av.status}`)
      blockers.push('availability check failed')
    }
  } catch (e) {
    fail('availability', e.message)
    blockers.push('availability error')
  }

  try {
    const s = await httpJson(`${REGISTRY}/api/handle/${encodeURIComponent(HANDLE)}`)
    console.log(
      'GET /api/handle/' + HANDLE,
      s.status,
      Array.isArray(s.body) ? `rows=${s.body.length}` : JSON.stringify(s.body).slice(0, 200)
    )
  } catch (e) {
    warn('search', e.message)
  }

  let signer
  let identity
  try {
    const key = loadKey(KEY_FILE)
    identity = key.toPublicKey().toString()
    ok('derived identity', identity)
    const expect = EXPECT_FLAG || identity.toLowerCase()
    if (identity.toLowerCase() !== expect) {
      fail('derived identity mismatch', `got ${identity}`)
      blockers.push('key does not match --expect-identity')
    } else if (EXPECT_FLAG) {
      ok('derived identity matches --expect-identity', identity)
    }
    signer = new ProtoWallet(key)
  } catch (e) {
    fail('load key', e.message)
    blockers.push('cannot load SERVER_PRIVATE_KEY_FILE')
  }

  if (identity) {
    try {
      const rev = await httpJson(`${REGISTRY}/api/identityKey/${encodeURIComponent(identity)}`)
      console.log(
        'GET /api/identityKey/{identity}',
        rev.status,
        typeof rev.body === 'object' ? JSON.stringify(rev.body).slice(0, 300) : rev.body
      )
      if (rev.status === 404) ok('identity has no handle yet', 'ERR_HANDLE_NOT_FOUND expected before claim')
      else if (rev.status === 200) {
        warn('identity already has profile', JSON.stringify(rev.body).slice(0, 200))
      } else fail('identity lookup', `HTTP ${rev.status}`)
    } catch (e) {
      fail('identity lookup', e.message)
    }
  }

  try {
    const h = await fetch(REGISTRY + '/', { method: 'GET', signal: AbortSignal.timeout(15_000) })
    ok('registry host reachable', `HTTP ${h.status}`)
  } catch (e) {
    fail('registry host', e.message)
    blockers.push('registry unreachable')
  }

  if (!signer) {
    console.log('\nSkipping cert mint — no signer')
  } else {
    const issuedAt = new Date()
    const cert = await buildCert(signer, paymail, issuedAt, DISPLAY)
    if (identity && cert.subject.toLowerCase() !== identity.toLowerCase()) {
      fail('cert subject', cert.subject)
      blockers.push('cert subject mismatch')
    } else ok('cert subject/certifier', cert.subject)
    ok('cert fields', JSON.stringify(cert.fields))
    ok('cert serialNumber present', cert.serialNumber ? 'yes' : 'no')
    ok('cert signature present', cert.signature ? `len=${cert.signature.length}` : 'MISSING')
    const preview = {
      type: cert.type,
      subject: cert.subject,
      certifier: cert.certifier,
      fields: cert.fields,
      serialNumber: cert.serialNumber,
      signature: cert.signature.slice(0, 16) + '…'
    }
    console.log('cert preview (truncated sig):', JSON.stringify(preview, null, 2))

    if (DRY) {
      console.log('\nDRY-RUN: would PUT', `${REGISTRY}/api/handle`)
      console.log('DRY-RUN: not sending. Pass --execute after approval to register.')
    } else {
      if (blockers.length) {
        console.log('\nEXECUTE aborted due to blockers:', blockers)
        process.exit(2)
      }
      console.log('\nEXECUTE: PUT', `${REGISTRY}/api/handle`)
      const put = await httpJson(`${REGISTRY}/api/handle`, {
        method: 'PUT',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(cert)
      })
      console.log('PUT status', put.status, JSON.stringify(put.body).slice(0, 500))
      if (put.status === 200 || put.status === 201) ok('registered', paymail)
      else {
        fail('register', `HTTP ${put.status}`)
        process.exit(1)
      }
      const rev2 = await httpJson(`${REGISTRY}/api/identityKey/${encodeURIComponent(identity)}`)
      console.log('post-register reverse', rev2.status, JSON.stringify(rev2.body).slice(0, 400))
    }
  }

  console.log('\n=== blockers ===')
  if (blockers.length === 0) console.log('(none detected by dry-run checks)')
  else blockers.forEach((b) => console.log('-', b))

  console.log('\n=== notes ===')
  console.log('- Handle PUT does NOT use AuthFetch; the self-signed BRC-52 profile cert is the auth.')
  console.log('- Domain proof for clients: DNS SRV _bsvalias._tcp.' + DOMAIN + ' → the Message Box host.')
  console.log('- The Message Box operator must allow ' + DOMAIN + ' as a registrable handle domain.')
  console.log('- No on-chain fee for the handle claim itself; registry policy may rate-limit.')

  if (DRY) process.exit(blockers.length ? 3 : 0)
}

main().catch((e) => {
  console.error('FATAL', e)
  process.exit(1)
})

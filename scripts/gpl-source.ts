// Checks that the source archive published with a release accounts for the
// third-party programs inside its enclave image (GPL-2.0 sections 1 and 3).
//
//   bun scripts/gpl-source.ts <permaily.eif> <permaily-gpl-source.tar.gz> [--require-complete]
//
// It fails when the archive is not well formed (a file missing or changed
// against its SHA256SUMS), when a program in the image is not the binary the
// archive describes (the archive was built from other AWS blobs than the
// image), or when the image's boot ramdisk holds a file the archive does not
// account for. With --require-complete (the release), it also fails while a
// copyleft program's source is "unavailable" in the archive's manifest.

import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, relative } from 'node:path'
import { gunzipSync } from 'node:zlib'

export type Component = {
  name: string
  location: string
  sha256: string
  license: string
  copyleft: boolean
  source: 'exact' | 'aws-stated' | 'unavailable'
  files: string[]
}

export type Manifest = { components: Component[] }

const sha256 = (b: Uint8Array) => createHash('sha256').update(b).digest('hex')

// EIF layout: aws/aws-nitro-enclaves-image-format, src/defs/mod.rs. Header:
// magic, version u16, flags u16, memory u64, cpus u64, reserved u16, count u16,
// 32 section offsets u64, 32 section sizes u64, unused u32, crc u32 (big endian).
// Each section starts with type u16, flags u16, size u64.
const SECTION = { 1: 'kernel', 2: 'cmdline', 3: 'ramdisk', 4: 'signature', 5: 'metadata' } as const

export function eifSections(eif: Uint8Array): { type: string; data: Uint8Array }[] {
  const v = new DataView(eif.buffer, eif.byteOffset, eif.byteLength)
  if (new TextDecoder().decode(eif.subarray(0, 4)) !== '.eif') throw new Error('not an EIF')
  const count = v.getUint16(4 + 2 + 2 + 8 + 8 + 2)
  const offsets = 4 + 2 + 2 + 8 + 8 + 2 + 2
  const sections = []
  for (let i = 0; i < count; i++) {
    const at = Number(v.getBigUint64(offsets + i * 8))
    const type = v.getUint16(at)
    const size = Number(v.getBigUint64(at + 4))
    const name = SECTION[type as keyof typeof SECTION] ?? `type-${type}`
    sections.push({ type: name, data: eif.subarray(at + 12, at + 12 + size) })
  }
  return sections
}

// Regular files of a newc cpio archive, by path.
export function cpioFiles(cpio: Uint8Array): Map<string, Uint8Array> {
  const files = new Map<string, Uint8Array>()
  const text = new TextDecoder()
  const pad = (n: number) => (n + 3) & ~3
  let at = 0
  while (at + 110 <= cpio.length) {
    const header = text.decode(cpio.subarray(at, at + 110))
    if (!header.startsWith('070701') && !header.startsWith('070702'))
      throw new Error(`bad cpio header at ${at}`)
    const field = (i: number) => Number.parseInt(header.slice(6 + i * 8, 14 + i * 8), 16)
    const mode = field(1)
    const fileSize = field(6)
    const nameSize = field(11)
    const name = text.decode(cpio.subarray(at + 110, at + 110 + nameSize - 1))
    const dataAt = pad(at + 110 + nameSize)
    if (name === 'TRAILER!!!') break
    if ((mode & 0o170000) === 0o100000)
      files.set(name.replace(/^\.\//, ''), cpio.subarray(dataAt, dataAt + fileSize))
    at = pad(dataAt + fileSize)
  }
  return files
}

// Files of the image's user ramdisk that are Permaily's own (monzo/aws-nitro-util
// mkUserRamdisk: the entry point, its environment and the root filesystem).
const OWN = /^(cmd|env|rootfs\/.*)$/

export function checkImage(eif: Uint8Array, manifest: Manifest): string[] {
  const problems: string[] = []
  const sections = eifSections(eif)
  const blobs = new Map<string, Uint8Array>()
  for (const s of sections) if (s.type === 'kernel') blobs.set('kernel', s.data)
  for (const s of sections.filter((s) => s.type === 'ramdisk')) {
    for (const [path, data] of cpioFiles(gunzipSync(s.data))) {
      if (blobs.has(`ramdisk:${path}`)) problems.push(`ramdisk:${path} appears twice`)
      blobs.set(`ramdisk:${path}`, data)
    }
  }
  const declared = new Set(manifest.components.map((c) => c.location))
  for (const c of manifest.components) {
    const data = blobs.get(c.location)
    if (!data) problems.push(`${c.name}: no ${c.location} in the image`)
    else if (sha256(data) !== c.sha256)
      problems.push(
        `${c.name}: ${c.location} in the image is ${sha256(data)}, the archive describes ${c.sha256}`,
      )
  }
  for (const location of blobs.keys()) {
    const path = location.replace(/^ramdisk:/, '')
    if (!declared.has(location) && !(location.startsWith('ramdisk:') && OWN.test(path)))
      problems.push(`${location} is in the image but not in the archive's manifest`)
  }
  return problems
}

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}

export function checkArchive(root: string, manifest: Manifest): string[] {
  const problems: string[] = []
  const sums = new Map<string, string>()
  for (const line of readFileSync(join(root, 'SHA256SUMS'), 'utf8').split('\n').filter(Boolean)) {
    const [hash, file] = line.split(/\s+\*?/, 2)
    sums.set(file.replace(/^\.\//, ''), hash)
  }
  for (const [file, hash] of sums) {
    const p = join(root, file)
    if (!existsSync(p)) problems.push(`${file} is in SHA256SUMS but not in the archive`)
    else if (sha256(readFileSync(p)) !== hash) problems.push(`${file} does not match SHA256SUMS`)
  }
  for (const p of walk(root)) {
    const file = relative(root, p)
    if (file !== 'SHA256SUMS' && !sums.has(file)) problems.push(`${file} is not in SHA256SUMS`)
  }
  for (const f of ['COPYING', 'README.md', 'manifest.json'])
    if (!sums.has(f)) problems.push(`the archive has no ${f}`)
  for (const c of manifest.components)
    for (const f of c.files)
      if (!sums.has(f)) problems.push(`${c.name}: ${f} is not in the archive`)
  return problems
}

export function incomplete(manifest: Manifest): Component[] {
  return manifest.components.filter((c) => c.copyleft && c.source === 'unavailable')
}

if (import.meta.main) {
  const args = process.argv.slice(2).filter((a) => !a.startsWith('--'))
  const requireComplete = process.argv.includes('--require-complete')
  if (args.length !== 2) {
    console.error(
      'usage: bun scripts/gpl-source.ts <permaily.eif> <permaily-gpl-source.tar.gz> [--require-complete]',
    )
    process.exit(2)
  }
  const [eifPath, archivePath] = args
  const tmp = mkdtempSync(join(tmpdir(), 'gpl-source-'))
  let problems: string[]
  let manifest: Manifest
  try {
    execFileSync('tar', ['-xzf', archivePath, '-C', tmp])
    const root = join(tmp, 'permaily-gpl-source')
    manifest = JSON.parse(readFileSync(join(root, 'manifest.json'), 'utf8'))
    problems = [...checkArchive(root, manifest), ...checkImage(readFileSync(eifPath), manifest)]
  } finally {
    rmSync(tmp, { recursive: true, force: true })
  }
  for (const c of manifest.components)
    console.log(`${c.source.padEnd(11)} ${c.license.padEnd(26)} ${c.name}`)
  for (const c of incomplete(manifest)) {
    const message = `${c.name} (${c.license}) is in the image, but its corresponding source is not in the archive`
    if (requireComplete) problems.push(message)
    else console.log(`::warning::${message}`)
  }
  for (const p of problems) console.log(`::error::${p}`)
  if (problems.length) process.exit(1)
  console.log(`${archivePath} accounts for every third-party program in ${eifPath}`)
}

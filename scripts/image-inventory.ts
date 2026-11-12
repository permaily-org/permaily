// Checks that the third-party programs inside an enclave image are exactly the
// ones nix/aws-blobs.json describes: the kernel and every file of the boot
// ramdisk have the SHA-256 it gives, and nothing in the image is unaccounted
// for besides Permaily's own entry point, environment and root filesystem.
//
//   bun scripts/image-inventory.ts <permaily.eif> [<permaily.eif> ...]
//
// CI runs it on every image it builds, so the image cannot change its AWS blobs
// without the inventory saying what they are and where their source is.

import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { gunzipSync } from 'node:zlib'

export type Component = {
  name: string
  location: string
  sha256: string
  license: string
  source: 'exact' | 'aws-stated' | 'unavailable'
}

export type Inventory = { components: Component[] }

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

export function checkImage(eif: Uint8Array, inventory: Inventory): string[] {
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
  const declared = new Set(inventory.components.map((c) => c.location))
  for (const c of inventory.components) {
    const data = blobs.get(c.location)
    if (!data) problems.push(`${c.name}: no ${c.location} in the image`)
    else if (sha256(data) !== c.sha256)
      problems.push(
        `${c.name}: ${c.location} in the image is ${sha256(data)}, the inventory says ${c.sha256}`,
      )
  }
  for (const location of blobs.keys()) {
    const path = location.replace(/^ramdisk:/, '')
    if (!declared.has(location) && !(location.startsWith('ramdisk:') && OWN.test(path)))
      problems.push(`${location} is in the image but not in the inventory`)
  }
  return problems
}

if (import.meta.main) {
  const images = process.argv.slice(2)
  if (images.length === 0) {
    console.error('usage: bun scripts/image-inventory.ts <permaily.eif> [<permaily.eif> ...]')
    process.exit(2)
  }
  const inventory: Inventory = JSON.parse(
    readFileSync(join(import.meta.dir, '../nix/aws-blobs.json'), 'utf8'),
  )
  let failed = false
  for (const image of images) {
    const problems = checkImage(readFileSync(image), inventory)
    for (const p of problems) console.log(`::error::${image}: ${p}`)
    if (problems.length) failed = true
    else console.log(`${image}: every third-party program is the one nix/aws-blobs.json describes`)
  }
  for (const c of inventory.components)
    console.log(`${c.source.padEnd(11)} ${c.license.padEnd(18)} ${c.name}`)
  if (failed) process.exit(1)
}

import { describe, expect, it } from 'vitest'
import { compressibleKind, fitSize, isAnimatedPng, needsCompression, prepareImage, withExtension, worthKeeping, type DecodedImage, type ImageCodec } from './imageCompress'
import { formatBytes } from './bytes'

const KB = 1024
const file = (name: string, type: string, size: number) => Object.assign(new Blob([new Uint8Array(size)], { type }), { name })

interface Fake {
  codec: ImageCodec
  calls: string[]
}
function fake(opts: { width?: number; height?: number; out?: { size: number; type: string } | null; decodeNull?: boolean; animated?: boolean; throws?: boolean } = {}): Fake {
  const calls: string[] = []
  const image: DecodedImage = { width: opts.width ?? 1000, height: opts.height ?? 800, close: () => void calls.push('close') }
  return {
    calls,
    codec: {
      isAnimatedPng: async () => {
        calls.push('animated?')
        return !!opts.animated
      },
      decode: async () => {
        calls.push('decode')
        if (opts.throws) throw new Error('boom')
        return opts.decodeNull ? null : image
      },
      encode: async (_i, w, h) => {
        calls.push(`encode ${w}x${h}`)
        return opts.out === null ? null : new Blob([new Uint8Array(opts.out?.size ?? 100 * KB)], { type: opts.out?.type ?? 'image/webp' })
      },
    },
  }
}

describe('the decision', () => {
  it('only PNG and JPEG, by type or else by extension', () => {
    expect(compressibleKind({ type: 'image/png', name: 'a' })).toBe('png')
    expect(compressibleKind({ type: 'image/jpeg', name: 'a' })).toBe('jpeg')
    expect(compressibleKind({ type: '', name: 'A.JPG' })).toBe('jpeg')
    expect(compressibleKind({ type: '', name: 'a.png' })).toBe('png')
    for (const [type, name] of [['image/gif', 'a.gif'], ['image/svg+xml', 'a.svg'], ['image/webp', 'a.webp'], ['image/avif', 'a.avif'], ['application/pdf', 'a.pdf'], ['text/plain', 'a.png'], ['', 'a.gif'], ['', 'png']]) {
      expect(compressibleKind({ type, name }), `${type} ${name}`).toBeNull()
    }
  })
  it('over 300 KB, or over 2560 px on either side', () => {
    expect(needsCompression(300 * KB, 1000, 1000)).toBe(false)
    expect(needsCompression(300 * KB + 1, 1000, 1000)).toBe(true)
    expect(needsCompression(10, 2560, 100)).toBe(false)
    expect(needsCompression(10, 2561, 100)).toBe(true)
    expect(needsCompression(10, 100, 2561)).toBe(true)
  })
  it('scales to fit 2560 on the longest side, never up', () => {
    expect(fitSize(4000, 3000)).toEqual({ width: 2560, height: 1920 })
    expect(fitSize(3000, 6000)).toEqual({ width: 1280, height: 2560 })
    expect(fitSize(1000, 800)).toEqual({ width: 1000, height: 800 })
    expect(fitSize(10000, 1)).toEqual({ width: 2560, height: 1 })
  })
  it('kept only when at least 20 % smaller', () => {
    expect(worthKeeping(1000, 800)).toBe(true)
    expect(worthKeeping(1000, 801)).toBe(false)
    expect(worthKeeping(1000, 1200)).toBe(false)
  })
  it('the new name has the .webp extension', () => {
    expect(withExtension('photo.jpg', 'webp')).toBe('photo.webp')
    expect(withExtension('a.b.PNG', 'webp')).toBe('a.b.webp')
    expect(withExtension('noext', 'webp')).toBe('noext.webp')
  })
})

describe('prepareImage', () => {
  it('redraws a big JPEG scaled to fit and uses the WebP when it is much smaller', async () => {
    const f = fake({ width: 4000, height: 3000, out: { size: 400 * KB, type: 'image/webp' } })
    const original = file('photo.jpg', 'image/jpeg', 2000 * KB)
    const r = await prepareImage(original, f.codec)
    expect(r).toMatchObject({ name: 'photo.webp', compressed: true })
    expect(r.blob.size).toBe(400 * KB)
    expect(f.calls).toEqual(['decode', 'encode 2560x1920', 'close'])
  })
  it('compresses a PNG over 300 KB that is small in pixels', async () => {
    const f = fake({ width: 800, height: 600, out: { size: 100 * KB, type: 'image/webp' } })
    expect(await prepareImage(file('shot.png', 'image/png', 500 * KB), f.codec)).toMatchObject({ name: 'shot.webp', compressed: true })
    expect(f.calls).toContain('encode 800x600')
  })
  it('compresses a small file that is over 2560 px', async () => {
    const f = fake({ width: 3000, height: 100, out: { size: 10 * KB, type: 'image/webp' } })
    expect((await prepareImage(file('wide.png', 'image/png', 200 * KB), f.codec)).compressed).toBe(true)
  })
  it('leaves a small, small-in-pixels file alone without encoding', async () => {
    const f = fake({ width: 800, height: 600 })
    const original = file('small.png', 'image/png', 100 * KB)
    const r = await prepareImage(original, f.codec)
    expect(r.blob).toBe(original)
    expect(r).toMatchObject({ name: 'small.png', compressed: false })
    expect(f.calls.some((c) => c.startsWith('encode'))).toBe(false)
  })
  it('keeps the original when the result is not 20 % smaller', async () => {
    const f = fake({ width: 4000, height: 3000, out: { size: 1700 * KB, type: 'image/webp' } })
    const original = file('photo.jpg', 'image/jpeg', 2000 * KB)
    const r = await prepareImage(original, f.codec)
    expect(r.blob).toBe(original)
    expect(r.compressed).toBe(false)
  })
  it('keeps the original when the browser cannot write WebP', async () => {
    for (const out of [null, { size: 10, type: 'image/png' }]) {
      const original = file('photo.jpg', 'image/jpeg', 2000 * KB)
      expect((await prepareImage(original, fake({ width: 4000, height: 3000, out }).codec)).blob).toBe(original)
    }
  })
  it('keeps the original when the picture cannot be read or the codec fails', async () => {
    const original = file('photo.jpg', 'image/jpeg', 2000 * KB)
    expect((await prepareImage(original, fake({ decodeNull: true }).codec)).blob).toBe(original)
    expect((await prepareImage(original, fake({ throws: true }).codec)).blob).toBe(original)
  })
  it('never touches GIF, SVG, WebP, AVIF or other files: the codec is not even asked', async () => {
    for (const [name, type] of [['a.gif', 'image/gif'], ['a.svg', 'image/svg+xml'], ['a.webp', 'image/webp'], ['a.avif', 'image/avif'], ['a.pdf', 'application/pdf'], ['a.zip', 'application/zip']]) {
      const f = fake({ width: 9000, height: 9000 })
      const original = file(name, type, 5000 * KB)
      const r = await prepareImage(original, f.codec)
      expect([name, r.blob === original, r.compressed, f.calls]).toEqual([name, true, false, []])
    }
  })
  it('leaves an animated PNG alone', async () => {
    const f = fake({ width: 9000, height: 9000, animated: true })
    const original = file('anim.png', 'image/png', 5000 * KB)
    expect((await prepareImage(original, f.codec)).blob).toBe(original)
    expect(f.calls).toEqual(['animated?'])
  })
})

describe('isAnimatedPng', () => {
  const chunk = (type: string, length: number) => {
    const out = new Uint8Array(12 + length)
    new DataView(out.buffer).setUint32(0, length)
    out.set(new TextEncoder().encode(type), 4)
    return out
  }
  const png = (...types: [string, number][]) => {
    const parts = [new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10]), ...types.map(([t, n]) => chunk(t, n))]
    const out = new Uint8Array(parts.reduce((a, p) => a + p.length, 0))
    let o = 0
    for (const p of parts) {
      out.set(p, o)
      o += p.length
    }
    return out
  }
  it('finds acTL before the first IDAT', () => expect(isAnimatedPng(png(['IHDR', 13], ['acTL', 8], ['IDAT', 50], ['IEND', 0]))).toBe(true))
  it('a still PNG', () => expect(isAnimatedPng(png(['IHDR', 13], ['sRGB', 1], ['IDAT', 50], ['IEND', 0]))).toBe(false))
  it('does not run past the data on a truncated file', () => expect(isAnimatedPng(png(['IHDR', 13]))).toBe(false))
})

describe('formatBytes', () => {
  it('says sizes in binary units', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(10 * 1024 * 1024)).toBe('10 MB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(300 * 1024)).toBe('300 KB')
    expect(formatBytes(5 * 1024 ** 3)).toBe('5 GB')
  })
})

// Making a big picture smaller before it is uploaded.
//
// A PNG or JPEG that is larger than 300 KB, or wider or taller than 2560 px, is
// redrawn on a canvas scaled to fit 2560 px and encoded as WebP (quality 0.85).
// The result is used only if it is at least 20 % smaller; otherwise the original
// goes up unchanged. Nothing else is ever touched: GIF (it may be animated), SVG
// (not pixels), WebP and AVIF (already compressed) and files that are not images.
// So are animated PNGs: a canvas keeps only the first frame.
//
// The decision logic is plain functions; the browser's decoder and encoder come
// in through `ImageCodec`, so tests do not need a canvas. Compression is a
// courtesy: any failure along the way means "upload the original".

export const COMPRESS_MIN_BYTES = 300 * 1024
export const COMPRESS_MAX_SIDE = 2560
export const COMPRESS_QUALITY = 0.85
export const COMPRESS_MIN_SAVING = 0.2

export interface DecodedImage {
  width: number
  height: number
  close?(): void
}

export interface ImageCodec {
  // Null when the browser cannot read the file.
  decode(blob: Blob): Promise<DecodedImage | null>
  // WebP at COMPRESS_QUALITY, drawn at this size; null when the browser cannot encode WebP.
  encode(image: DecodedImage, width: number, height: number): Promise<Blob | null>
  // Whether the file is an animated PNG.
  isAnimatedPng(blob: Blob): Promise<boolean>
}

export interface Prepared {
  blob: Blob
  name: string
  compressed: boolean
}

type FileLike = Blob & { name: string }

// The kind by MIME type, falling back to the extension when the system gave none.
export function compressibleKind(file: { type: string; name: string }): 'png' | 'jpeg' | null {
  const type = file.type.toLowerCase()
  if (type === 'image/png') return 'png'
  if (type === 'image/jpeg' || type === 'image/jpg') return 'jpeg'
  if (type !== '') return null
  const ext = /\.([^.]+)$/.exec(file.name)?.[1]?.toLowerCase()
  if (ext === 'png') return 'png'
  if (ext === 'jpg' || ext === 'jpeg') return 'jpeg'
  return null
}

export const needsCompression = (size: number, width: number, height: number) => size > COMPRESS_MIN_BYTES || width > COMPRESS_MAX_SIDE || height > COMPRESS_MAX_SIDE

// Scaled down to fit the longest side, never up.
export function fitSize(width: number, height: number, max = COMPRESS_MAX_SIDE): { width: number; height: number } {
  const scale = Math.min(1, max / Math.max(width, height))
  return { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)) }
}

export const worthKeeping = (originalSize: number, newSize: number) => newSize <= originalSize * (1 - COMPRESS_MIN_SAVING)

export const withExtension = (name: string, ext: string) => `${name.replace(/\.[^./\\]+$/, '')}.${ext}`

// An APNG has an `acTL` chunk before its first `IDAT`. `head` is the start of the file.
export function isAnimatedPng(head: Uint8Array): boolean {
  const view = new DataView(head.buffer, head.byteOffset, head.byteLength)
  let offset = 8
  while (offset + 8 <= head.length) {
    const length = view.getUint32(offset)
    const type = String.fromCharCode(head[offset + 4], head[offset + 5], head[offset + 6], head[offset + 7])
    if (type === 'acTL') return true
    if (type === 'IDAT' || type === 'IEND') return false
    offset += 12 + length
  }
  return false
}

export async function prepareImage(file: FileLike, codec: ImageCodec): Promise<Prepared> {
  const original: Prepared = { blob: file, name: file.name, compressed: false }
  const kind = compressibleKind(file)
  if (!kind) return original
  let decoded: DecodedImage | null = null
  try {
    if (kind === 'png' && (await codec.isAnimatedPng(file))) return original
    decoded = await codec.decode(file)
    if (!decoded || !needsCompression(file.size, decoded.width, decoded.height)) return original
    const { width, height } = fitSize(decoded.width, decoded.height)
    const out = await codec.encode(decoded, width, height)
    // A browser that cannot write WebP hands back a PNG: that is no gain.
    if (!out || out.type !== 'image/webp' || !worthKeeping(file.size, out.size)) return original
    return { blob: out, name: withExtension(file.name, 'webp'), compressed: true }
  } catch {
    return original
  } finally {
    decoded?.close?.()
  }
}

export const browserCodec: ImageCodec = {
  async decode(blob) {
    try {
      return await createImageBitmap(blob)
    } catch {
      return null
    }
  },
  async encode(image, width, height) {
    const canvas = document.createElement('canvas')
    canvas.width = width
    canvas.height = height
    const ctx = canvas.getContext('2d')
    if (!ctx) return null
    ctx.imageSmoothingQuality = 'high'
    ctx.drawImage(image as ImageBitmap, 0, 0, width, height)
    return new Promise((resolve) => canvas.toBlob(resolve, 'image/webp', COMPRESS_QUALITY))
  },
  async isAnimatedPng(blob) {
    // The animation chunk comes before the pixels, near the start of the file.
    return isAnimatedPng(new Uint8Array(await blob.slice(0, 256 * 1024).arrayBuffer()))
  },
}

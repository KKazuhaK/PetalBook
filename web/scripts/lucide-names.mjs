// Writes the names of the Lucide icons the web app ships, one per line, as the
// server checks an icon against them (internal/icons/lucide.txt). The names are
// those of src/lib/icons/lucideAll.ts. Run it again after upgrading lucide-react:
// `node scripts/lucide-names.mjs > ../internal/icons/lucide.txt`.
import { icons } from 'lucide-react'

const kebab = (s) =>
  s
    .replace(/([a-z0-9])([A-Z])/g, '$1-$2')
    .replace(/([A-Za-z])(\d)/g, '$1-$2')
    .toLowerCase()

const names = [...new Set(Object.keys(icons).map(kebab))].sort()
process.stdout.write(names.join('\n') + '\n')

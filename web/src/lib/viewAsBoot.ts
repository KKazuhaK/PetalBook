// Imported first by main.tsx, before the app and its router read the address: a tab opened at `/as/<id>` keeps the
// view and shows `/` (lib/viewAs).
import { startViewAs } from './viewAs'

startViewAs()

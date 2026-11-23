// The shape of the words of templates (see templates/): the Chinese ones with their literals widened, so the English
// file must have exactly the same keys.

import type { templatesZhCN } from './templates/zh-CN.ts'
import type { Widen } from './types.ts'

export type TemplatesMessages = Widen<typeof templatesZhCN>

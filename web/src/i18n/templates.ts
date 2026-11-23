// The words of templates: the "Templates" section of a space's settings, the choice made when a page is created and
// the default template of a folder, in the language of the page. Imported only by those, which load on demand: the
// main bundle does not carry them.

import { templatesEn } from './templates/en.ts'
import { templatesZhCN } from './templates/zh-CN.ts'
import type { TemplatesMessages } from './templatesTypes.ts'
import { lang } from './index.ts'

export type { TemplatesMessages }
export const tt: TemplatesMessages = lang === 'en' ? templatesEn : templatesZhCN

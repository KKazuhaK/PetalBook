// The administration console's words (the sidebar, the tables, a person's page), in the language of the page. Imported
// only by the administration, which is loaded on demand.

import { consoleEn } from './console/en.ts'
import { consoleZhCN } from './console/zh-CN.ts'
import type { ConsoleMessages } from './consoleTypes.ts'
import { lang } from './index.ts'

export type { ConsoleMessages }
export const tc: ConsoleMessages = lang === 'en' ? consoleEn : consoleZhCN

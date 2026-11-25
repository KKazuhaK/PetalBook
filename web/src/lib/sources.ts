import { lang } from '@/i18n'

// The sources a space offers when it has chosen none (space.yml `sources:`), in the language of the page.
export const DEFAULT_SOURCES_ZH = ['原创', '互联网', '书', '电影', '音乐', '对话'] as const
export const DEFAULT_SOURCES_EN = ['Original', 'Internet', 'Book', 'Film', 'Music', 'Conversation'] as const
export const DEFAULT_SOURCES: string[] = [...(lang === 'zh-CN' ? DEFAULT_SOURCES_ZH : DEFAULT_SOURCES_EN)]

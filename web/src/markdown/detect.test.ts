import { describe, expect, it } from 'vitest'
import { detectMarkdown, looksLikeMarkdown } from './detect'

const yes = (text: string) => expect(detectMarkdown(text), JSON.stringify(text)).toMatchObject({ markdown: true })
const no = (text: string) => expect(detectMarkdown(text), JSON.stringify(text)).toMatchObject({ markdown: false })

describe('is Markdown', () => {
  it('a heading', () => {
    yes('# 标题')
    yes('正文\n\n### 小节\n\n正文')
    yes('###### six')
  })
  it('a closed fence, with or without a language', () => {
    yes('```\ncode\n```')
    yes('说明\n\n```ts\nconst a = 1;\nconst b = 2;\n```\n')
    yes('~~~\nx\n~~~')
  })
  it('a table', () => {
    yes('| a | b |\n| --- | --- |\n| 1 | 2 |')
    yes('a | b\n--- | ---\n1 | 2')
    yes('| 花 | 季节 |\n|:--|--:|\n| 栀子 | 夏 |')
  })
  it('lists of two or more items', () => {
    yes('- 一\n- 二')
    yes('1. 一\n2. 二')
    yes('* 一\n  * 嵌套')
    yes('- [ ] 待办')
  })
  it('two or more quote lines', () => yes('> 一\n> 二'))
  it('a link or an image', () => {
    yes('见[另一篇](https://example.com/x)。')
    yes('![图](/assets/a/b.png)')
    yes('[a](b "title")')
  })
  it('bold and strike', () => {
    yes('这是**粗体**')
    yes('~~删除~~')
  })
  it('two different weak signals', () => {
    yes('*斜体* 和 `代码`')
    yes('> 引用一行\n\n- 一条')
    yes('- 一条\n\n---')
  })
  it('a fence that is never closed, with another sign', () => yes('# 标题\n\n```js\nlet x'))
  it('a closed fence even in a script-looking text', () => yes('```sh\n$ npm run build;\n$ npm test;\n```'))
})

describe('is not Markdown', () => {
  it('ordinary text', () => {
    no('')
    no('一句话')
    no('第一行\n第二行\n\n第二段')
    no('诗行一\n诗行二\n诗行三')
  })
  it('a lone list item or quote line (chat text)', () => {
    no('- 一条')
    no('1. 引言')
    no('> 一行引用')
    no('2024. 一年')
  })
  it('a bare URL, a hash tag, an at-name', () => {
    no('https://example.com/a?b=c')
    no('#标签 和 #另一个')
    no('@someone 你好')
  })
  it('one weak signal', () => {
    no('用 `foo` 来调用')
    no('一个 *强调*')
    no('---')
    no('__init__.py')
  })
  it('spaces just inside the markers, and unbalanced ones', () => {
    no('2 * 3 * 4')
    no('a ** b ** c')
    no('**没有结尾')
    no('~单个波浪~ 约~10')
    no('snake_case_name 和 other_var_name')
  })
  it('table lookalikes', () => {
    no('a | b')
    no('a | b\n\nc | d')
    no('名称|说明\n- - -')
  })
  it('program code', () => {
    no('npm install;\n# run\nnpm start;')
    no('import os\nimport sys\n# comment\nprint(1)')
    no('const a = 1;\nconst b = 2;\n# not a heading')
    no('$ ls\n# a comment\n$ cd x')
    no('function f() {\n  return 1\n}')
  })
  it('an unclosed fence on its own', () => no('```js\nlet x'))
  it('text inside a fence is not inspected', () => {
    // Only the fence is Markdown here; the list-looking lines inside add nothing and the text still counts once.
    expect(detectMarkdown('```\n- a\n- b\n# c\n```').signals).toEqual(['fence'])
  })
  it('HTML', () => no('<p>text</p>'))
})

describe('details', () => {
  it('line endings do not matter', () => expect(looksLikeMarkdown('# a\r\n\r\ntext\r\n')).toBe(true))
  it('reports what it found', () => expect(detectMarkdown('# a\n\n- x\n- y').signals).toEqual(['heading', 'list']))
})

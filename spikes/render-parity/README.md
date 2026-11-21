# Go 端渲染与前端是否一致

第 0 期的验证。前端用 `remark-cjk-friendly` 让 `**“引文”**。` 这类写法算粗体，
这不是标准 CommonMark。服务端（Go）以后要渲染 Markdown（公开发布、导出），
必须得出同样的结果，否则同一个文件在编辑器里是粗体、发布出去却露出星号。

结论：**可以一致。Go 端用 goldmark 加 `goldmark-cjk-friendly` 扩展。**

## 结果（2026-11-21）

拿编辑器实际会写出的 116 份 Markdown，分别用前端的 remark 流水线和 Go 的
goldmark 渲染成 HTML，去掉纯属标记风格的差异后比较。

| Go 端配置 | 一致 |
|---|---|
| goldmark 1.8.7，不加扩展（`go run . -plain`） | 106/116 |
| 加 `goldmark-cjk-friendly` 2.0.3 | 114/116 |

不加扩展时不一致的 10 个：9 个中文标点旁的强调用例里的 7 个，包含这类
写法的「随笔」综合用例，以及下面两个。

剩下 2 个：

| 用例 | 现象 | 处理 |
|---|---|---|
| 粗体和斜体部分交叠（`**甲*乙****丙*`） | 前端解析正确，goldmark 露出星号 | 连续四个星号有歧义。让转换层在这种位置改用 `_` 表示斜体，避免写出歧义序列 |
| 正文里的裸网址 | 前端当成链接，goldmark 不当 | 转换层把裸网址统一写成 `<网址>`，两边就一致 |

两个都要在转换层里改，改完应当全部一致。还没改。`node parity.mjs` 在出现
这两个之外的差异时会报错退出。

## 要知道的事

- `goldmark-cjk-friendly` 和前端的 `remark-cjk-friendly` 是同一位作者维护的
  移植版，规则来源相同。它是个小项目（3 个 star，MIT），要锁定版本，并把
  这份对比测试放进 CI：任何一边升级后结果不一致就会发现。
- 它目前依赖 goldmark 的 1.x 系列。goldmark 已经有 `/v2` 模块路径，这个扩展
  是否支持还没查。
- goldmark 自带一个更宽松的强调算法（`parser.ParseDelimiterSimple`），没有
  采用，因为它和前端的规则不是同一套。
- 脚注、原始 HTML 块的渲染没有比较，两边的 HTML 结构本来就不同。

## 运行

先由前端测试导出用例，再用 Go 渲染，最后比较：

```bash
cd ../markdown-roundtrip && node docfirst.mjs tiptap-remark --dump=../render-parity/cases.json
```

```bash
GOWORK=off go run .
```

```bash
cd ../markdown-roundtrip && node parity.mjs --show
```

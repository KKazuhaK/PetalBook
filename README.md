# PetalBook

自托管的个人写作与笔记应用：内容是 Git 仓库里的 Markdown 文件，通过网页
所见即所得地编辑，不需要懂 Markdown。

A self-hosted writing and notes app. Pages are Markdown files in a Git
repository, edited in the browser without needing to know Markdown.

**状态：设计阶段，还没有可用的程序。** 目前仓库里是设计文档和动工前的
验证代码。

- [设计与分期计划](docs/design.md)
- 验证
  - [Markdown 往返与浏览器编辑](spikes/markdown-roundtrip/README.md)
  - [搜索与列表的规模](spikes/search-scale/README.md)
  - [Go 端渲染与前端的一致性](spikes/render-parity/README.md)

PetalBook 是 [Rackbook](https://github.com/KazuhaHub/rackbook) 的姊妹项目，
登录协议层共用 [authcore](https://github.com/KazuhaHub/authcore)。

## 许可证

[AGPL-3.0](LICENSE)

// Invented sample pages for development and tests. Times are offsets back from
// "now" so the relative timestamps in the list always look alive.

export interface SeedPage {
  folder: string
  title?: string
  subtitle?: string
  tags?: string[]
  created: string
  // How long ago it was last changed, in minutes.
  ago: number
  extra?: Record<string, unknown>
  body: string
}

export const SPACE_NAME = '我的笔记'

// Folders that exist but hold no page yet.
export const SEED_EMPTY_FOLDERS = ['随笔/2027', '归档']

export const seedPages: SeedPage[] = [
  // --- root: loose notes, most of them untitled ---
  { folder: '', created: '2026-11-20', ago: 12, tags: ['随记'], body: '晚饭后绕着小区走了一圈，桂花已经落光了，路灯下面还剩一点香。\n' },
  { folder: '', created: '2026-11-18', ago: 60 * 30, body: '想起外婆晒的梅干菜，坛子口总是压着一块青石。\n' },
  { folder: '', created: '2026-11-02', ago: 60 * 24 * 12, tags: ['随记', '天气'], body: '第一次开了暖气。窗上起雾，字写在雾上，一会儿就干了。\n' },
  { folder: '', title: '本周要做的事', created: '2026-11-16', ago: 60 * 5, tags: ['待办'], body: '- [x] 给阳台的绿萝换盆\n- [ ] 回复房东的消息\n- [ ] 把旧书整理出两箱\n- [ ] 预约牙医\n' },

  // --- 随笔 ---
  {
    folder: '随笔',
    title: '关于慢',
    subtitle: '走路、煮饭和等一壶水',
    created: '2026-09-04',
    ago: 60 * 26,
    tags: ['随笔', '日常'],
    body: '我一直觉得“慢”不是速度，是**注意力停留的时间**。\n\n煮饭的时候不看手机，米香就会从锅盖的缝里钻出来，这是快的人闻不到的。\n\n## 三件小事\n\n1. 走路时只看路\n2. 喝茶时只喝茶\n3. 睡前十分钟什么也不做\n',
  },
  {
    folder: '随笔',
    title: '一把旧伞',
    subtitle: '它比我更懂下雨',
    created: '2026-06-12',
    ago: 60 * 24 * 3,
    tags: ['随笔', '物件'],
    body: '伞骨断了一根，撑开时像一只受伤的鸟。\n\n> 修伞的老师傅说，伞是修得好的，只是没人再愿意等。\n\n我等了三天，取回来的时候它多了一根不一样颜色的骨头。\n',
  },
  { folder: '随笔', title: '菜市场的早晨', created: '2026-05-21', ago: 60 * 24 * 40, tags: ['随笔', '日常'], body: '六点半的菜市场，鱼摊的冰还没化，卖豆腐的阿姨已经把秤砣擦得发亮。\n' },
  {
    folder: '随笔',
    title: '夜行',
    subtitle: '凌晨的路灯是一排省略号',
    created: '2025-12-30',
    ago: 60 * 24 * 88,
    tags: ['随笔', '夜晚'],
    extra: { series: '冬季', status: '定稿' },
    body: '凌晨两点的街很安静，只有红绿灯还在认真地交替。\n\n我不赶时间，所以每个红灯都等满。\n',
  },
  { folder: '随笔', created: '2026-10-09', ago: 60 * 24 * 40, tags: ['随记'], body: '地铁上看到一个人在给空气比划着说话，耳机线藏在围巾里。我也想有一件这样的围巾。\n' },
  {
    folder: '随笔/2026',
    title: '十月的账',
    subtitle: '记一笔不太算数的账',
    created: '2026-10-31',
    ago: 60 * 24 * 21,
    tags: ['随笔', '月记'],
    body: '这个月花得最值的是一把新钥匙扣。\n\n| 项目 | 金额 | 值不值 |\n| --- | --- | --- |\n| 钥匙扣 | 18 | 值 |\n| 外卖 | 640 | 不太值 |\n| 书 | 215 | 值 |\n',
  },
  { folder: '随笔/2026', title: '搬家笔记', created: '2026-03-15', ago: 60 * 24 * 70, tags: ['随笔', '日常'], body: '搬家最难打包的是不知道该不该留的东西。纸箱上我只写了两个字：以后。\n' },
  { folder: '随笔/2026', created: '2026-08-08', ago: 60 * 24 * 104, tags: ['随记', '天气'], body: '立秋。热得像是忘了这回事。\n' },
  {
    folder: '随笔/2026',
    title: '长一点的文章',
    subtitle: '用来试试滚动和排版',
    created: '2026-07-02',
    ago: 60 * 24 * 130,
    tags: ['随笔'],
    body:
      '这是一篇用来检查长文排版的样稿。\n\n' +
      Array.from({ length: 8 }, (_, i) => `第${i + 1}段：每天的日子像抽屉里的纽扣，大小不一，颜色各异，找的时候一颗也不见，不找的时候哗啦啦全滚了出来。把它们摆在桌上，竟然能拼出一个季节。\n`).join('\n'),
  },

  // --- 读书 ---
  {
    folder: '读书',
    title: '《小径分岔的花园》读后',
    subtitle: '迷宫就是时间本身',
    created: '2026-02-10',
    ago: 60 * 24 * 6,
    tags: ['读书', '小说'],
    body: '读到一半我才发现，这个故事要求读者自己先成为迷宫的一部分。\n\n## 摘记\n\n- 时间不是一条线\n- 每一次选择都开出另一条路\n- 书信和花园是同一种东西\n',
  },
  { folder: '读书', title: '读书清单', subtitle: '想读的和读了一半的', created: '2026-01-05', ago: 60 * 24 * 2, tags: ['读书', '待办'], body: '- [x] 《山居笔记》\n- [ ] 《看不见的城市》\n- [ ] 《植物的欲望》\n- [ ] 《夜航西飞》\n' },
  { folder: '读书', title: '关于注释', created: '2026-04-22', ago: 60 * 24 * 55, tags: ['读书'], body: '好的注释像路边的长椅，不拦着你往前走，但累了可以坐下。\n' },
  { folder: '读书', created: '2026-09-19', ago: 60 * 24 * 9, tags: ['读书', '随记'], body: '“书页翻得多了，会有手的形状。”这句话我是在二手书的空白处读到的，铅笔写的。\n' },
  {
    folder: '读书',
    title: '摘抄：写给自己的备忘',
    created: '2026-03-01',
    ago: 60 * 24 * 90,
    tags: ['读书', '摘抄'],
    body: '一段代码也能当作读书笔记：\n\n```js\nconst pages = books.flatMap((b) => b.pages)\nconsole.log(`共读了 ${pages.length} 页`)\n```\n\n---\n\n不过大多数时候，铅笔更好用。\n',
  },

  // --- 菜谱 ---
  {
    folder: '菜谱',
    title: '番茄鸡蛋面',
    subtitle: '十五分钟，一口锅',
    created: '2026-04-08',
    ago: 60 * 24 * 14,
    tags: ['菜谱', '面食'],
    body: '## 材料\n\n- 番茄 2 个\n- 鸡蛋 2 个\n- 细面一把\n- 葱花、盐、一小勺糖\n\n## 做法\n\n1. 番茄切块炒出汁，加半碗水\n2. 水开下面，快好时淋入蛋液\n3. 出锅撒葱花\n\n**糖是关键**，一点点就够了。\n',
  },
  { folder: '菜谱', title: '桂花酒酿圆子', created: '2025-11-02', ago: 60 * 24 * 300, tags: ['菜谱', '甜品'], body: '酒酿不要煮太久，最后一分钟放进去，香气才在。\n' },
  { folder: '菜谱', title: '葱油拌面', subtitle: '把葱炸到焦黄再停火', created: '2026-01-19', ago: 60 * 24 * 160, tags: ['菜谱', '面食'], body: '小葱一斤，油要多，火要小。酱油、老抽和一点冰糖在锅里化开，浇在面上。\n' },
  { folder: '菜谱', created: '2026-05-30', ago: 60 * 24 * 18, tags: ['菜谱', '随记'], body: '粥里放一小撮盐，米的甜味会出来。试过了，真的。\n' },

  // --- 收件箱: quick fragments ---
  { folder: '收件箱', created: '2026-11-21', ago: 3, body: '记得问一下邻居那盆茉莉是怎么养的。\n' },
  { folder: '收件箱', created: '2026-11-21', ago: 45, tags: ['待办'], body: '周六去花市，买一把剪刀，两个小花盆。\n' },
  { folder: '收件箱', created: '2026-11-19', ago: 60 * 40, body: '一句歌词：“你把月亮折成纸船，放进我的水杯里。”下次想想后半句。\n' },
  { folder: '收件箱', created: '2026-11-17', ago: 60 * 24 * 4, tags: ['随记'], body: '路过一家没有招牌的面馆，门口放着一盆水仙，开了。\n' },

  // --- 工作 ---
  {
    folder: '工作',
    title: '周会记录',
    subtitle: '十一月第三周',
    created: '2026-11-17',
    ago: 60 * 24 * 4,
    tags: ['工作', '会议'],
    body: '## 议题\n\n1. 发布节奏\n2. 文档整理\n3. 下周排班\n\n## 结论\n\n- 周四之前把草稿交出来\n- 文档统一放进共享文件夹\n\n行动项：\n\n- [ ] 整理旧文档目录\n- [ ] 确认周四的评审时间\n',
  },
  { folder: '工作', title: '术语表', subtitle: '大家说的是同一件事吗', created: '2026-08-21', ago: 60 * 24 * 33, tags: ['工作'], body: '| 术语 | 含义 |\n| --- | --- |\n| 草稿 | 还没有被别人看过的版本 |\n| 定稿 | 写的人觉得可以了的版本 |\n| 最终版 | 以后再改的版本 |\n' },
  { folder: '工作', created: '2026-10-28', ago: 60 * 24 * 24, tags: ['工作', '随记'], body: '开会时有人说“我们对齐一下颗粒度”，我在本子上画了一颗米。\n' },

  // --- 旅行 (second level) ---
  { folder: '旅行/杭州', title: '西湖边的一个下午', subtitle: '没有景点，只有长椅', created: '2026-10-03', ago: 60 * 24 * 49, tags: ['旅行', '随笔'], body: '没有去断桥，在苏堤边的长椅上坐了一个下午。柳树的影子像一把扫帚，在水面上扫了很久。\n' },
  { folder: '旅行/杭州', created: '2026-10-04', ago: 60 * 24 * 48, tags: ['旅行', '随记'], body: '龙井村的茶农说，明前茶不是名字，是时间。\n' },
  { folder: '', created: '2026-11-10', ago: 60 * 24 * 11, tags: ['随记'], body: '收到一张明信片，邮戳是三周前的，背面只有一句：[这家店](https://example.com/shop)的桂花糕比我说得还好。\n' },
  { folder: '旅行/杭州', title: '行程草稿', subtitle: '三天，不赶', created: '2026-09-28', ago: 60 * 24 * 55, tags: ['旅行', '待办'], body: '- [x] 订车票\n- [x] 订民宿\n- [ ] 查一下茶馆的营业时间\n- [ ] 带一把伞\n' },
  { folder: '工作', title: '读者来信的回复模板', created: '2026-09-02', ago: 60 * 24 * 60, tags: ['工作'], body: '你好，\n\n谢谢你的来信。关于你提到的问题，我们的看法是：先写，再改，最后再决定要不要发。\n\n祝好\n' },
]

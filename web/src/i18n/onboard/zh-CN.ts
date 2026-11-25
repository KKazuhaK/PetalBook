// The words of the first-sign-in tour, the connect-your-repository guide and the help menu.
export const onboardZhCN = {
  help: {
    label: '帮助',
    tour: '新手引导',
    repo: '连接自己的仓库：一步一步',
    admin: '管理员须知',
  },
  tour: {
    title: '新手引导',
    step: (i: number, n: number) => `第 ${i} 步，共 ${n} 步`,
    next: '下一步',
    back: '上一步',
    skip: '跳过',
    finish: '开始使用',
    skipHelp: '随时可以跳过，之后在左下角的「帮助」里重新打开。',
    steps: {
      welcome: {
        title: '欢迎来到 PetalBook',
        body: '花一分钟，看看怎么写第一页、怎么找回它。不想看就点「跳过」，之后在左下角的「帮助」里还能打开。',
      },
      page: {
        title: '新建一页',
        body: '点侧边栏的「快速记录」随手记一笔，或者在文件夹里点「新建」。每一页都是一个普通的 Markdown 文件，保存在空间的仓库里，不会被锁在这个应用里。',
        action: '打开快速记录',
      },
      editor: {
        title: '写作',
        body: '边写边自动保存。选中文字会出现格式工具栏；右上角可以切到源码模式；标签、来源这些信息在页面的属性面板里改，只能改值，不会把头部写坏。',
      },
      favorites: {
        title: '收藏',
        body: '把鼠标停在文件夹列表的一行上，右边会淡入一颗星，点一下就收藏了，收藏会出现在侧边栏上方。手机上星一直在，只是淡一些。',
      },
      search: {
        title: '搜索',
        body: '按 Ctrl+K（Mac 上是 ⌘K），或者点侧边栏顶部的搜索框，可以按标题、正文、标签和来源找，也能按日期筛选。',
        action: '打开搜索',
      },
      repo: {
        title: '连接你自己的仓库',
        bodyAsked:
          '这个站点希望你的空间连着你自己的 Git 仓库，好让你的文字在你自己手里也有一份。一共两步要你来做，下面一步一步讲。',
        bodyForced:
          '这个站点要求你的空间连着你自己的 Git 仓库，在连上之前空间是只读的。一共两步要你来做，下面一步一步讲。',
        action: '一步一步来',
      },
      admin: {
        title: '管理员：先看这几件事',
        body: '每日文案默认是关闭的：到「管理后台 → 每日文案」打开一个内置接口，或者加几句自己的话，首页才会出现每天的一句。用户、存储和登录方式也都在管理后台里。',
        action: '打开每日文案',
      },
      done: {
        title: '就这些',
        body: '想再看一遍，点侧边栏底部的「帮助」。祝你写得开心。',
      },
    },
  },
  repo: {
    title: '连接你自己的仓库',
    whyTitle: '为什么要连',
    whyAsked: (when: string) =>
      `站点建议你的空间连着你自己的 Git 仓库，让内容在你自己手里留一份。${when} 之前连上即可，现在不影响写作。`,
    whyForced: '站点要求你的空间连着你自己的 Git 仓库。连上之前，这个空间是只读的，连上后马上就能继续写。',
    byProvider: (name: string) => `要求用的是 ${name}。`,
    anyProvider: '可以用任何一个站点支持的平台。',
    stepsTitle: '要做的事',
    stepOf: (i: number, n: number) => `第 ${i} 步，共 ${n} 步`,
    done: '已完成',
    todo: '还没做',
    now: '现在做这一步',
    auto: '自动完成',
    connect: {
      title: (name: string) => `连接你的 ${name} 账号`,
      what: (name: string) => `点「去连接」，在设置的「连接」里选 ${name}，会跳到 ${name} 的授权页面。`,
      authorise: (name: string) => `授权 PetalBook 读取你在 ${name} 上的账号信息。这一步不会给它任何仓库的权限。`,
      action: '去连接',
    },
    repo: {
      title: '选一个仓库，或者让 PetalBook 新建一个',
      what: '点「打开同步设置」，再点「连接仓库」：选一个已有的空仓库，或者让 PetalBook 在你的账号下新建一个。',
      authorise: '在平台的授权页面上，只给 PetalBook 这一个仓库的读写权限（内容）就够了，不用给所有仓库。',
      action: '打开同步设置',
    },
    sync: {
      title: '等第一次同步',
      what: '连上之后空间会先同步一次，状态变成「已同步」就好了，不用再做什么。',
    },
    left: (n: number) => (n === 1 ? '还差最后一步' : `还差 ${n} 步`),
    helpTitle: '卡住了？',
    help: '先看设置里「同步」页面上的提示，它会说是哪里不对。还是不行，就把页面上的错误文字发给你的管理员。',
    nothing:
      '现在没有需要连接仓库的空间：站点没有这么要求，或者你的空间都已经连好了。想自己连，随时可以在设置的「同步」里做。',
    close: '关闭',
    bannerLeft: (step: string) => `还差：${step}`,
    bannerAction: '一步一步来',
  },
}

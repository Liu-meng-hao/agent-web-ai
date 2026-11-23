---
name: web-report
description: 把数据或调研结果做成一页可在浏览器打开的精美 HTML 报告
when_to_use: 用户要求生成数据报告、周报、数据看板、单页分析或可视化页面时
version: 0.1.0
allowed-tools: [fs_read, fs_list, shell, artifact_write, skill_read_file]
---

# Web Report

把用户给的数据变成**一页自包含的 HTML 报告**：能直接在浏览器打开、手机可读、可打印，不依赖任何外网资源。

## 流程

1. **读数据**：用 `fs_read` / `fs_list` 定位数据文件；大文件先用 `shell` 的 `head` / `wc` 看结构。不要凭空编造数字，缺数据就向用户要。
2. **定结构**：先想清楚这份报告给谁看、最重要的三个结论是什么。典型结构：
   - 标题 + 一句话结论 + 生成时间
   - 3–4 个 KPI 卡片（数值 + 同比/环比 + 简短解释）
   - 1–2 张图表（趋势用折线，对比用柱状，占比用环形）
   - 明细表格（可横向滚动）
   - 结论/建议 + 数据口径说明
3. **读设计系统**：用 `fs_read` 读取系统提示给出的设计系统目录（默认 `data/artifacts/_shared/`）下的 `tokens.css`、`base.css`、`gallery.html`，按页面类型参考对应示例；只有确实需要图表时才读 `charts.js`。
   - 非数据类需求（落地页、文章、表单、作品集）不要硬套下面的报告结构，按 `gallery.html` 中对应类型组织。
4. **写页**：调用 `artifact_write`：
   - `slug`：kebab-case，如 `sales-weekly`
   - `title`：中文标题
   - `path`：`index.html`
   - `mode`：`create`
   - `content`：完整 HTML，样式与脚本**全部内联**
   - 超过约 300 行或 12KB 时先 `create` 完整可打开的骨架，再用 `mode=patch` 分块补全；每块尽量大，最多 3 次 patch，避免输出截断和步数耗尽。
5. **修自检问题**：工具返回的问题清单要逐条修，用 `mode=patch` + `old` 唯一片段修改，不要整页重写。
6. **收尾**：把预览地址给用户，说明数据口径与图表含义；不要贴整页 HTML。

## 质量红线

- **无外链**：不引用 `http(s)://`、CDN、Google Fonts、外部图片；字体用系统字体栈，图片用内联 SVG 或 data URI。
- **不要引用 `/a/_shared/`**：设计系统文件只用来抄代码，成品必须自包含，否则脱离预览服务会掉样式。
- 必须有 `<html lang="zh-CN">`、`<meta charset>`、viewport；用 `<header>/<main>/<section>/<table>` 等语义标签。
- 375px 宽度不横向溢出：表格放可滚动容器，图表用 `viewBox` 自适应。
- 正文对比度 ≥ 4.5:1；不只靠颜色传达信息（涨跌同时用文字/符号）。
- 单文件 < 200KB（默认配置）；数据行数多时只放摘要 + 代表性明细。
- 打印可读：避免大面积深色背景，关键内容不依赖 hover。

## 图表

`charts.js` 暴露 `window.Charts`，提供纯内联 SVG：

```js
Charts.bar(el, { labels: [...], values: [...], unit: "%" })
Charts.line(el, { labels: [...], values: [...] })
Charts.donut(el, { labels: [...], values: [...] })
```

把函数源码直接内联进 `<script>`，不要用 `<script src>`。数据为空或全为 0 时给占位文案，不要画空图。

## 完成标准

- `artifact_write` 自检无问题（或已修复并再次通过）。
- 预览地址可打开，桌面与 375px 宽度都不溢出。
- 用户能从页面第一屏读到结论，而不是一堆无解释的图。

更细的排版与验收清单见 `references/page-quality.md`。

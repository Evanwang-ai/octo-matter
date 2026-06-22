# Bug: 计划图连线首次加载不渲染

## 现象

打开任何有计划图的 matter 详情页，节点正常显示但节点之间的 SVG 贝塞尔连线不出现。

**复现步骤**：
1. 打开 `localhost:28080/matter/ui`
2. 点击任何有子任务或 mode_config 的 matter
3. 计划图区域：节点（.pg-node）正常渲染，连线不可见

**关键线索**：点击计划图右上角的 `⛶` 展开浮窗按钮后，浮窗内的连线正常显示。关闭浮窗后，外部视图的连线也出现了。刷新页面后又消失。

这说明 `drawEdgesFor()` 函数本身能正确生成 SVG path，但首次调用时机有问题。

## 涉及文件

`internal/webui/static/index.html`

## 核心函数

- `drawEdgesFor(g)` (~line 6363) — 在 `.plan-graph` 容器内找 `svg.pg-edges`，计算节点位置，生成贝塞尔 path，设置 SVG innerHTML
- `drawPlanEdges()` (~line 6394) — 遍历所有 `.plan-graph` 元素调 `drawEdgesFor`，附加 ResizeObserver
- `paintMatter(my)` (~line 6490) — 渲染 matter 详情页，结尾调 `requestAnimationFrame(function(){ requestAnimationFrame(drawPlanEdges); })` + `setTimeout(drawPlanEdges, 150)`

## SVG 结构

SVG 在 HTML 模板里生成（`planGraphHTML` 和 `roleGraphHTML` 都包含）：
```html
<div class="plan-graph">
  <svg class="pg-edges" aria-hidden="true"></svg>
  <div class="pg-cols">
    <div class="pg-col">[节点...]</div>
    ...
  </div>
</div>
```

CSS:
```css
.plan-graph { position: relative; padding: 6px 2px 10px; overflow-x: auto; cursor: grab; }
.pg-edges { position: absolute; left: 0; top: 0; width: 100%; height: 100%; pointer-events: none; z-index: 0; overflow: visible; }
.pg-cols { position: relative; display: flex; align-items: center; gap: 56px; z-index: 1; width: max-content; min-width: 100%; }
```

## 已尝试但无效的方案

1. **单次 requestAnimationFrame** — 不够，布局未完成
2. **双重 requestAnimationFrame** — 仍不够
3. **setTimeout(drawPlanEdges, 150)** — 仍不够
4. **ResizeObserver** — 附加了但首次仍不触发有效绘制
5. **createElementNS 重建 SVG** — SVG innerHTML 在 NS 元素上的兼容性问题，回退到模板 SVG
6. **CSS width:100% height:100% + overflow:visible** — 加了但没解决

## 推测根因

`drawEdgesFor` 的 `g.getBoundingClientRect()` 在首次调用时返回正确的 width/height（否则 ResizeObserver 不会重新触发），但 **子节点 `.pg-node` 的 `getBoundingClientRect()`** 可能返回 (0,0) 坐标，导致所有 path 的起终点重合在原点——path 存在但长度为零，视觉上不可见。

浮窗能触发正确渲染，可能是因为浮窗的 `.plan-graph` 在 `document.body` 层级上，没有嵌套在 `#centerCol` 的 flex/scroll 容器里，布局更直接。

## 建议排查方向

1. 在 `drawEdgesFor` 中 `console.log(gr, paths)` 看 getBoundingClientRect 值和生成的 path 字符串
2. 检查 `#centerCol` 是否有 CSS transition 或延迟布局（flex item resize animation）
3. 检查 `.plan-graph` 是否在首次渲染时被 `overflow: hidden` 的祖先裁剪
4. 尝试把 SVG 放在 `.pg-cols` **之后**（而非之前），用 `position: absolute; inset: 0` 覆盖
5. 尝试用 `IntersectionObserver` 替代 `ResizeObserver`，在元素进入视口时绘制

# Bug: 收件箱列表右键菜单不弹出

## 现象

在收件箱列表视图中，右键点击事项行（`.mrow`），浏览器默认右键菜单出现，自定义的 `.ctx-menu` 不弹出。

## 涉及文件

`internal/webui/static/index.html`

## 代码位置

### CSS (~line 1678)
```css
.ctx-menu { position: fixed; z-index: 800; min-width: 160px; ... }
```

### JS — 事件绑定 (~line 3634)
在 `bindInbox(my)` 函数内：
```javascript
var mlist = $(".mlist");
if (mlist) mlist.addEventListener("change", function(e){ ... });  // 批量选择
if (mlist) mlist.addEventListener("contextmenu", function(e){     // 右键菜单
  var row = e.target.closest(".mrow");
  if (!row) return;
  e.preventDefault();
  closeCtxMenu();
  var mid = row.getAttribute("data-id");
  ...
  document.body.appendChild(menu);
  ...
});
```

### 菜单操作
- 归档 (data-ctx="archive") → PUT /matters/:id/status {status:"archived"}
- 取消归档 (data-ctx="unarchive") → PUT /matters/:id/status {status:"done"}
- 取消事项 (data-ctx="cancel") → PUT /matters/:id/status {status:"cancelled"}

### 辅助函数 (~line 3668)
```javascript
function closeCtxMenu(){ var m = $("#ctxMenu"); if (m) m.remove(); }
function doCtxStatus(id, status, my){ ... }
```

## 排查方向

1. `bindInbox(my)` 是否在每次 `paintInbox` 后被调用？检查调用链
2. `$(".mlist")` 是否在 `bindInbox` 执行时已经存在于 DOM 中？`paintInbox` 设置 innerHTML 后是否立即调了 `bindInbox`？
3. 是否有其他代码在 `#centerCol` 或 `.mlist` 上拦截了 `contextmenu` 事件（`e.stopPropagation()`）
4. 浏览器 DevTools → Elements → Event Listeners 面板，检查 `.mlist` 元素上是否有 `contextmenu` listener
5. 检查 `.mrow` 是否有 `data-id` 属性（`rowHTML` 生成时是否设置了）

## 已确认

- CSS 样式已写入文件
- JS 代码已写入文件
- `if (mlist)` guard 已加
- `closeCtxMenu` 和 `doCtxStatus` 函数已定义

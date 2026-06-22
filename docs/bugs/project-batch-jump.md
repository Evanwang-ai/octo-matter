# Bug: 项目列表多选操作后页面跳位

## 现象

在项目详情 → 事项列表视图中，勾选多个事项后出现批量操作条。点击「归档」或「取消事项」后，操作成功但页面滚动位置跳到顶部。

## 原因

操作完成后调用 `paintProjectDetail(my)` 重新渲染整个 `#centerCol`。innerHTML 全量替换导致滚动位置重置。

## 涉及文件

`internal/webui/static/index.html`

## 代码位置

### 批量操作绑定 (~line 5617, `paintProjectDetail` 末尾)

```javascript
Promise.all(ids.map(function(id){
  return api("/matters/" + id + "/status", ...).then(function(){ done++; }).catch(function(){});
})).then(function(){
  toast(...);
  ids.forEach(function(id){ var r = st.rows.find(...); if (r) r.status = target; });
  paintProjectDetail(my);  // ← 全量重绘,滚动位置丢失
});
```

### 右键菜单同样的问题 (~line 5650)
```javascript
api("/matters/" + mid + "/status", ...).then(function(){
  toast(...);
  var r = st.rows.find(...); if (r) r.status = target;
  paintProjectDetail(my);  // ← 同样全量重绘
});
```

## 修复方向

**方案 A（推荐）**：在 `paintProjectDetail` 开头保存滚动位置，结尾恢复。已有先例——看板模式的 `savedScrollLeft` 逻辑（~line 5189）：
```javascript
var savedScrollLeft = 0;
var oldVp = $(".board-viewport", $("#centerCol"));
if (oldVp) savedScrollLeft = oldVp.scrollLeft;
// ... 渲染 ...
if (savedScrollLeft) { var newVp = ...; if (newVp) newVp.scrollLeft = savedScrollLeft; }
```

对列表视图也做同样处理：保存 `#centerCol` 或 `.content` 的 `scrollTop`，渲染后恢复。

**方案 B**：不调 `paintProjectDetail` 全量重绘，而是局部更新——只修改受影响行的 DOM（`row.classList.add("archived")` + 淡出动画），然后从 DOM 移除。更流畅但改动更大。

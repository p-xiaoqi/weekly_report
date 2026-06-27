// web/extension/icon.png - 这是一个 128x128 的透明图标占位文件
// 实际使用时请替换为真实的 PNG 图标
// 可以使用任意图片编辑工具创建一个简单的图标
// 或从 https://www.flaticon.com/ 下载免费图标

// 由于 Write 工具不支持二进制文件，这里创建一个简单的 SVG 图标作为占位
// 在 Chrome 扩展中，如果找不到 icon.png，可以暂时使用 SVG 或从网页加载
// 但 manifest.json 要求 PNG 格式，所以请手动创建一个 icon.png 文件

// 临时方案：使用 Canvas 生成图标（放在 popup.js 中）
// 或直接使用在线工具生成：https://www.flaticon.com/

// 建议图标尺寸：16x16, 48x48, 128x128
// 颜色：#409eff（蓝色）


## ⚠️ 使用前必读：登录会话要求

`/api/v1/collect/browser` 接口现在需要登录鉴权。使用插件「生成周报」前，请先在**同一浏览器**中打开
[http://localhost:8080](http://localhost:8080) 并通过**飞书登录**，使该来源（origin）下存在有效的登录会话 Cookie。

插件推送时会以 `credentials: 'include'` 携带该 Cookie，后端从会话中推导用户身份（忽略请求体中的 `user_id`）。
若未登录，推送会返回 **401 未登录**。

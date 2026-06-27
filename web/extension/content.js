// content.js - 内容脚本，注入到所有页面

// 预定义常见系统的选择器规则
const SITE_RULES = {
  'jira': {
    host: ['atlassian.net', 'jira'],
    selectors: ['.issue-row', '[data-testid="issue-row"]', '.issuerow'],
    title: '.summary, .issue-summary, [data-testid="issue-summary"]',
    status: '.status, .issue-status, [data-testid="issue-status"]'
  },
  'github': {
    host: ['github.com'],
    selectors: ['.js-issue-row', '[id^="issue_"]', '.Box-row'],
    title: '.Link--primary, .h4, [aria-label]',
    status: '.issue-labels, .labels'
  },
  'feishu': {
    host: ['feishu.cn', 'larksuite.com'],
    selectors: ['.task-item', '[data-task-id]', '.list-item'],
    title: '.task-title, .title, [data-title]',
    status: '.task-status, .status'
  },
  'zentao': {
    host: ['zentao'],
    selectors: ['.datatable-row', 'tr', '.task-item'],
    title: '.task-name, .title, a',
    status: '.task-status, .status'
  },
  'teambition': {
    host: ['teambition.com'],
    selectors: ['.task-card', '.task-item', '[data-task-id]'],
    title: '.task-title, .title',
    status: '.task-status, .status-label'
  }
};

// 检测当前网站类型
function detectSite() {
  const hostname = window.location.hostname;
  for (const [name, rule] of Object.entries(SITE_RULES)) {
    for (const h of rule.host) {
      if (hostname.includes(h)) {
        return name;
      }
    }
  }
  return 'generic';
}

// 通用提取逻辑
function extractGeneric() {
  const records = [];
  const selectors = [
    'tr', 'li', '.list-item', '.card', '.task-item',
    '.issue-item', '.item', '[data-id]', '.box-row'
  ];

  let elements = [];
  let usedSelector = '';

  for (const sel of selectors) {
    const found = document.querySelectorAll(sel);
    if (found.length > 0 && found.length < 200) {
      elements = found;
      usedSelector = sel;
      break;
    }
  }

  console.log(`[周报插件] 使用选择器: ${usedSelector}, 找到 ${elements.length} 个元素`);

  elements.forEach((el, index) => {
    // 尝试多种方式提取标题
    let title = '';
    const titleCandidates = [
      el.querySelector('h1, h2, h3, h4, .title, .summary, .name, [data-title]'),
      el.querySelector('a'),
      el.querySelector('span')
    ];
    for (const cand of titleCandidates) {
      if (cand && cand.innerText?.trim()) {
        title = cand.innerText.trim().substring(0, 200);
        break;
      }
    }

    if (!title && el.innerText) {
      title = el.innerText.split('\n')[0].trim().substring(0, 200);
    }

    // 过滤无效项
    if (!title || title.length < 3 || title.length > 200) {
      return;
    }

    // 提取状态
    let status = 'completed';
    const statusText = el.innerText?.toLowerCase() || '';
    if (statusText.includes('进行中') || statusText.includes('in progress') || statusText.includes('doing')) {
      status = 'in_progress';
    } else if (statusText.includes('待办') || statusText.includes('todo') || statusText.includes('pending')) {
      status = 'pending';
    }

    records.push({
      title: title,
      description: '',
      status: status,
      occurred_at: new Date().toISOString(),
      source_type: 'browser_plugin',
      source_id: `browser_${Date.now()}_${index}`
    });
  });

  return records;
}

// 针对特定网站的提取
function extractSiteSpecific(site) {
  const rule = SITE_RULES[site];
  if (!rule) return extractGeneric();

  const records = [];
  const elements = document.querySelectorAll(rule.selectors.join(', '));

  elements.forEach((el, index) => {
    const titleEl = el.querySelector(rule.title);
    const statusEl = el.querySelector(rule.status);

    if (!titleEl) return;

    const title = titleEl.innerText?.trim();
    if (!title || title.length < 3) return;

    const statusText = statusEl?.innerText?.toLowerCase() || '';
    let status = 'completed';
    if (statusText.includes('progress') || statusText.includes('进行中')) {
      status = 'in_progress';
    }

    records.push({
      title: title,
      description: statusEl?.innerText?.trim() || '',
      status: status,
      occurred_at: new Date().toISOString(),
      source_type: `browser_${site}`,
      source_id: `${site}_${Date.now()}_${index}`
    });
  });

  return records;
}

// 主提取函数
function extractRecords() {
  const site = detectSite();
  console.log(`[周报插件] 检测到网站类型: ${site}`);

  let records;
  if (site !== 'generic') {
    records = extractSiteSpecific(site);
  } else {
    records = extractGeneric();
  }

  // 去重（基于标题相似度）
  const seen = new Set();
  const unique = records.filter(r => {
    const key = r.title.toLowerCase().substring(0, 50);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });

  return {
    site: site,
    records: unique,
    url: window.location.href,
    title: document.title
  };
}

// 监听 popup 消息
chrome.runtime.onMessage.addListener((request, sender, sendResponse) => {
  if (request.action === 'extract') {
    const result = extractRecords();
    sendResponse(result);
  } else if (request.action === 'sendToBackend') {
    sendToBackend(request.data, request.userID)
      .then(res => sendResponse({ success: true, data: res }))
      .catch(err => sendResponse({ success: false, error: err.message }));
    return true; // 保持消息通道开放
  }
  return true;
});

// 发送到后端
async function sendToBackend(records, userID) {
  const response = await fetch('http://localhost:8080/api/v1/collect/browser', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json'
    },
    body: JSON.stringify({
      user_id: userID || 'browser_user',
      source: 'browser_plugin',
      records: records
    })
  });

  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: ${await response.text()}`);
  }

  return response.json();
}

console.log('[周报插件] 内容脚本已加载');

// SPA 路由监听：页面 URL 变化时重新加载脚本
let lastUrl = location.href;
new MutationObserver(() => {
  const currentUrl = location.href;
  if (currentUrl !== lastUrl) {
    lastUrl = currentUrl;
    console.log('[周报插件] 检测到页面切换:', currentUrl);
    // 重新检测网站类型
    const site = detectSite();
    console.log('[周报插件] 切换后网站类型:', site);
  }
}).observe(document, { subtree: true, childList: true });

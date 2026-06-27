// popup.js - 插件弹窗逻辑

const STORAGE_KEY_USER_ID = 'weekly_report_user_id';

async function getStoredUserID() {
  const result = await chrome.storage.local.get([STORAGE_KEY_USER_ID]);
  return result[STORAGE_KEY_USER_ID] || '';
}

async function setStoredUserID(userID) {
  await chrome.storage.local.set({ [STORAGE_KEY_USER_ID]: userID });
}

document.addEventListener('DOMContentLoaded', async () => {
  const btnScan = document.getElementById('btn-scan');
  const btnSend = document.getElementById('btn-send');
  const pageInfo = document.getElementById('page-info');
  const recordCount = document.getElementById('record-count');
  const resultSection = document.getElementById('result-section');
  const resultList = document.getElementById('result-list');
  const resultCount = document.getElementById('result-count');
  const statusText = document.getElementById('status-text');
  const statusDot = document.getElementById('status-dot');

  let currentRecords = [];
  let currentSite = 'generic';

  // 获取当前标签页信息
  async function getCurrentTab() {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    return tab;
  }

  // 更新状态
  function updateStatus(text, type = 'info') {
    statusText.textContent = text;
    statusDot.className = 'status-dot';
    if (type === 'success') statusDot.classList.add('status-success');
    else if (type === 'error') statusDot.classList.add('status-error');
    else if (type === 'loading') statusDot.classList.add('status-loading');
  }

  // 初始化页面信息
  const tab = await getCurrentTab();
  if (tab) {
    const hostname = new URL(tab.url).hostname;
    pageInfo.textContent = hostname;
  }

  // 检查是否需要提示刷新（首次安装或页面已打开）
  try {
    const tab = await getCurrentTab();
    await chrome.tabs.sendMessage(tab.id, { action: 'ping' });
  } catch (err) {
    updateStatus('当前页面需要刷新后才能采集', 'warning');
  }

  // 扫描按钮
  btnScan.addEventListener('click', async () => {
    btnScan.disabled = true;
    btnScan.querySelector('#scan-text').textContent = '扫描中...';
    updateStatus('正在扫描页面...', 'loading');
    resultSection.style.display = 'none';

    try {
      const tab = await getCurrentTab();

      // 发送消息到 content script
      const response = await chrome.tabs.sendMessage(tab.id, { action: 'extract' });

      if (!response || !response.records) {
        throw new Error('页面无可识别内容');
      }

      currentRecords = response.records;
      currentSite = response.site;

      recordCount.textContent = `${currentRecords.length} 条记录`;
      resultCount.textContent = `${currentRecords.length} 条`;

      // 渲染结果列表
      renderResults(currentRecords);
      resultSection.style.display = 'block';

      btnSend.disabled = currentRecords.length === 0;
      updateStatus(`成功提取 ${currentRecords.length} 条记录`, 'success');

    } catch (err) {
      console.error('扫描失败:', err);
      updateStatus('扫描失败: ' + err.message, 'error');

      // 如果是内容脚本未加载，尝试注入
      if (err.message.includes('Receiving end does not exist')) {
        try {
          const tab = await getCurrentTab();
          await chrome.scripting.executeScript({
            target: { tabId: tab.id },
            files: ['content.js']
          });
          updateStatus('已注入脚本，请重新点击扫描', 'info');
        } catch (injectErr) {
          updateStatus('无法注入脚本，请刷新页面后重试', 'error');
        }
      }
    } finally {
      btnScan.disabled = false;
      btnScan.querySelector('#scan-text').textContent = '重新扫描';
    }
  });

  // 发送按钮
  btnSend.addEventListener('click', async () => {
    if (currentRecords.length === 0) return;

    // 获取或配置用户 ID
    let userID = await getStoredUserID();
    if (!userID) {
      userID = prompt('请输入你的用户 ID（用于关联周报）:', 'user_' + Date.now().toString().slice(-6));
      if (!userID) {
        updateStatus('已取消：未设置用户 ID', 'error');
        return;
      }
      await setStoredUserID(userID);
    }

    btnSend.disabled = true;
    updateStatus('正在生成周报...', 'loading');

    try {
      const tab = await getCurrentTab();
      const response = await chrome.tabs.sendMessage(tab.id, {
        action: 'sendToBackend',
        data: currentRecords,
        userID: userID
      });

      if (response.success) {
        updateStatus('周报生成成功！', 'success');
        // 打开结果页面
        chrome.tabs.create({ url: 'http://localhost:8080' });
      } else {
        throw new Error(response.error || '后端返回错误');
      }
    } catch (err) {
      console.error('发送失败:', err);
      updateStatus('发送失败: ' + err.message, 'error');
      btnSend.disabled = false;
    }
  });

  // 渲染结果列表
  function renderResults(records) {
    resultList.innerHTML = '';

    records.forEach((record, index) => {
      const item = document.createElement('div');
      item.className = 'result-item';

      const statusIcon = record.status === 'completed' ? '✅' :
                        record.status === 'in_progress' ? '🔄' : '⏳';

      item.innerHTML = `
        <div class="result-index">${index + 1}</div>
        <div class="result-content">
          <div class="result-title">${escapeHtml(record.title)}</div>
          <div class="result-meta">${statusIcon} ${record.status} | ${record.source_type}</div>
        </div>
      `;

      resultList.appendChild(item);
    });
  }

  function escapeHtml(text) {
    if (!text) return '';
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }
});

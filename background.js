// Background service worker for Claude, Gemini & Grok AI Assistant

async function getTabsForProvider(provider) {
  const p = (provider || 'claude').toLowerCase();
  if (p === 'gemini') {
    return await chrome.tabs.query({ url: 'https://gemini.google.com/*' });
  }
  if (p === 'grok' || p === 'x') {
    const grokTabs = await chrome.tabs.query({ url: 'https://grok.com/*' });
    if (grokTabs.length > 0) return grokTabs;
    return await chrome.tabs.query({ url: 'https://x.com/i/grok*' });
  }
  return await chrome.tabs.query({ url: 'https://claude.ai/*' });
}

function getDefaultUrlForProvider(provider) {
  const p = (provider || 'claude').toLowerCase();
  if (p === 'gemini') return 'https://gemini.google.com/app';
  if (p === 'grok' || p === 'x') return 'https://grok.com';
  return 'https://claude.ai/new';
}

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  (async () => {
    try {
      switch (message.type) {
        case 'CHECK_TAB':
        case 'CHECK_CLAUDE_TAB': {
          const provider = (message.provider || 'claude').toLowerCase();
          const tabs = await getTabsForProvider(provider);
          sendResponse({ hasTab: tabs.length > 0, tabId: tabs[0]?.id || null, provider });
          break;
        }

        case 'CHECK_ALL_TABS': {
          const claudeTabs = await chrome.tabs.query({ url: 'https://claude.ai/*' });
          const geminiTabs = await chrome.tabs.query({ url: 'https://gemini.google.com/*' });
          const grokDirectTabs = await chrome.tabs.query({ url: 'https://grok.com/*' });
          const grokXTabs = await chrome.tabs.query({ url: 'https://x.com/i/grok*' });
          const allGrokTabs = [...grokDirectTabs, ...grokXTabs];
          sendResponse({
            claude: { hasTab: claudeTabs.length > 0, tabId: claudeTabs[0]?.id || null },
            gemini: { hasTab: geminiTabs.length > 0, tabId: geminiTabs[0]?.id || null },
            grok: { hasTab: allGrokTabs.length > 0, tabId: allGrokTabs[0]?.id || null }
          });
          break;
        }

        case 'OPEN_TAB':
        case 'OPEN_CLAUDE_TAB': {
          const provider = (message.provider || 'claude').toLowerCase();
          const targetUrl = getDefaultUrlForProvider(provider);
          const tab = await chrome.tabs.create({ url: targetUrl, active: true });
          sendResponse({ success: true, tabId: tab.id, provider });
          break;
        }

        case 'SEND_PROMPT': {
          const provider = (message.provider || 'claude').toLowerCase();
          const targetUrl = getDefaultUrlForProvider(provider);

          let tabs = await getTabsForProvider(provider);
          let targetTab = tabs[0];

          if (!targetTab) {
            targetTab = await chrome.tabs.create({ url: targetUrl, active: false });
            await new Promise(resolve => setTimeout(resolve, 3500));
          }

          const response = await chrome.tabs.sendMessage(targetTab.id, {
            type: 'INJECT_PROMPT',
            target: provider,
            text: message.text,
            files: message.files
          });
          sendResponse(response);
          break;
        }

        case 'OPEN_SIDEPANEL': {
          if (sender.tab?.windowId) {
            await chrome.sidePanel.open({ windowId: sender.tab.windowId });
          } else {
            const [currentTab] = await chrome.tabs.query({ active: true, currentWindow: true });
            if (currentTab?.windowId) {
              await chrome.sidePanel.open({ windowId: currentTab.windowId });
            }
          }
          sendResponse({ success: true });
          break;
        }

        default:
          sendResponse({ error: 'Unknown message type' });
      }
    } catch (err) {
      sendResponse({ error: err.message });
    }
  })();
  return true; // Keep channel open for async response
});

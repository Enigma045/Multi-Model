// Popup / Side Panel script for Claude & Gemini AI Assistant

const chatContainer = document.getElementById('chat-container');
const welcomeBox = document.getElementById('welcome-message');
const welcomeTitle = document.getElementById('welcome-title');
const promptInput = document.getElementById('prompt-input');
const btnSend = document.getElementById('btn-send');
const btnClear = document.getElementById('btn-clear');
const btnSidepanel = document.getElementById('btn-sidepanel');
const statusBadge = document.getElementById('status-badge');
const connectBanner = document.getElementById('connect-banner');
const bannerText = document.getElementById('banner-text');
const btnOpenTab = document.getElementById('btn-open-tab');
const tabClaude = document.getElementById('tab-claude');
const tabGemini = document.getElementById('tab-gemini');
const tabGrok = document.getElementById('tab-grok');

let activeModel = 'claude'; // 'claude' or 'gemini'
let isGenerating = false;
let currentStreamingBubble = null;
let messages = [];

// Initialize
document.addEventListener('DOMContentLoaded', async () => {
  const savedState = await chrome.storage.local.get(['activeModel', 'chatHistory']);
  if (savedState?.activeModel) {
    activeModel = savedState.activeModel;
  }
  if (savedState?.chatHistory && Array.isArray(savedState.chatHistory)) {
    messages = savedState.chatHistory;
  }

  updateModelUI();
  renderMessages();
  await checkConnection();
  setupEventListeners();
  setInterval(checkConnection, 4000);
});

// Switch active model
function setModel(model) {
  if (activeModel === model) return;
  activeModel = model;
  chrome.storage.local.set({ activeModel: model });
  updateModelUI();
  checkConnection();
}

function updateModelUI() {
  document.body.setAttribute('data-active-model', activeModel);

  if (activeModel === 'claude') {
    tabClaude.classList.add('active');
    tabGemini.classList.remove('active');
    tabGrok.classList.remove('active');
    promptInput.placeholder = 'Ask Claude anything... (Enter to send)';
    bannerText.textContent = 'claude.ai tab is not open';
    btnOpenTab.textContent = 'Open Claude';
    welcomeTitle.textContent = 'Chat with Claude';
  } else if (activeModel === 'gemini') {
    tabGemini.classList.add('active');
    tabClaude.classList.remove('active');
    tabGrok.classList.remove('active');
    promptInput.placeholder = 'Ask Gemini anything... (Enter to send)';
    bannerText.textContent = 'gemini.google.com tab is not open';
    btnOpenTab.textContent = 'Open Gemini';
    welcomeTitle.textContent = 'Chat with Gemini';
  } else {
    tabGrok.classList.add('active');
    tabClaude.classList.remove('active');
    tabGemini.classList.remove('active');
    promptInput.placeholder = 'Ask Grok anything... (Enter to send)';
    bannerText.textContent = 'grok.com / x.com tab is not open';
    btnOpenTab.textContent = 'Open Grok';
    welcomeTitle.textContent = 'Chat with Grok';
  }
}

// Check tab connection for active model
async function checkConnection() {
  try {
    const res = await chrome.runtime.sendMessage({ type: 'CHECK_TAB', provider: activeModel });
    if (res?.hasTab) {
      statusBadge.textContent = 'Connected';
      statusBadge.className = 'status-badge online';
      connectBanner.classList.add('hidden');
    } else {
      statusBadge.textContent = 'Not open';
      statusBadge.className = 'status-badge offline';
      connectBanner.classList.remove('hidden');
    }
  } catch (err) {
    statusBadge.textContent = 'Error';
    statusBadge.className = 'status-badge offline';
  }
}

// Setup Event Listeners
function setupEventListeners() {
  tabClaude.addEventListener('click', () => setModel('claude'));
  tabGemini.addEventListener('click', () => setModel('gemini'));
  tabGrok.addEventListener('click', () => setModel('grok'));

  btnSend.addEventListener('click', handleSend);

  promptInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  });

  promptInput.addEventListener('input', () => {
    promptInput.style.height = 'auto';
    promptInput.style.height = Math.min(promptInput.scrollHeight, 100) + 'px';
  });

  btnOpenTab.addEventListener('click', async () => {
    await chrome.runtime.sendMessage({ type: 'OPEN_TAB', provider: activeModel });
    setTimeout(checkConnection, 1000);
  });

  btnClear.addEventListener('click', async () => {
    messages = [];
    await chrome.storage.local.set({ chatHistory: [] });
    renderMessages();
  });

  btnSidepanel.addEventListener('click', async () => {
    await chrome.runtime.sendMessage({ type: 'OPEN_SIDEPANEL' });
    window.close();
  });

  document.querySelectorAll('.prompt-chip').forEach(chip => {
    chip.addEventListener('click', () => {
      promptInput.value = chip.dataset.prompt;
      promptInput.focus();
      promptInput.dispatchEvent(new Event('input'));
    });
  });

  chrome.runtime.onMessage.addListener((msg) => {
    if (msg.type === 'AI_STREAMING' || msg.type === 'CLAUDE_STREAMING') {
      updateStreamingMessage(msg.text, msg.provider || activeModel);
    } else if (msg.type === 'AI_DONE' || msg.type === 'CLAUDE_DONE') {
      finishStreamingMessage(msg.text, msg.provider || activeModel);
    }
  });
}

// Handle sending prompt
async function handleSend() {
  const text = promptInput.value.trim();
  if (!text || isGenerating) return;

  const currentProvider = activeModel;
  addMessage('user', text);
  promptInput.value = '';
  promptInput.style.height = 'auto';
  promptInput.focus();

  isGenerating = true;
  btnSend.disabled = true;
  const label = currentProvider === 'gemini' ? 'Gemini' : currentProvider === 'grok' ? 'Grok' : 'Claude';
  currentStreamingBubble = createMessageElement(currentProvider, `${label} is thinking...`, label);
  currentStreamingBubble.querySelector('.msg-bubble').classList.add('streaming-cursor');
  chatContainer.appendChild(currentStreamingBubble);
  scrollToBottom();

  try {
    const res = await chrome.runtime.sendMessage({
      type: 'SEND_PROMPT',
      provider: currentProvider,
      text
    });

    if (res?.error) {
      updateStreamingMessage(`⚠️ ${res.error}`, currentProvider);
      finishStreamingMessage(`⚠️ ${res.error}`, currentProvider);
    }
  } catch (err) {
    updateStreamingMessage(`⚠️ Failed to communicate with ${label}: ${err.message}`, currentProvider);
    finishStreamingMessage(`⚠️ Failed to communicate with ${label}: ${err.message}`, currentProvider);
  }
}

function updateStreamingMessage(text, provider) {
  if (!currentStreamingBubble) return;
  const bubble = currentStreamingBubble.querySelector('.msg-bubble');
  bubble.textContent = text;
  scrollToBottom();
}

function finishStreamingMessage(finalText, provider) {
  if (!currentStreamingBubble) return;
  const bubble = currentStreamingBubble.querySelector('.msg-bubble');
  bubble.classList.remove('streaming-cursor');
  if (finalText) bubble.textContent = finalText;

  const sender = provider || activeModel;
  messages.push({ sender, text: bubble.textContent });
  chrome.storage.local.set({ chatHistory: messages });

  isGenerating = false;
  btnSend.disabled = false;
  currentStreamingBubble = null;
  scrollToBottom();
}

function addMessage(sender, text) {
  messages.push({ sender, text });
  chrome.storage.local.set({ chatHistory: messages });
  renderMessages();
}

function renderMessages() {
  chatContainer.innerHTML = '';

  if (messages.length === 0) {
    chatContainer.appendChild(welcomeBox);
    return;
  }

  messages.forEach(msg => {
    const label = msg.sender === 'gemini' ? 'Gemini' : msg.sender === 'grok' ? 'Grok' : msg.sender === 'claude' ? 'Claude' : 'You';
    const el = createMessageElement(msg.sender, msg.text, label);
    chatContainer.appendChild(el);
  });

  scrollToBottom();
}

function createMessageElement(sender, text, label) {
  const wrapper = document.createElement('div');
  wrapper.className = `message ${sender}`;

  if (sender !== 'user' && label) {
    const header = document.createElement('div');
    header.className = 'msg-header';
    header.textContent = label;
    wrapper.appendChild(header);
  }

  const bubble = document.createElement('div');
  bubble.className = 'msg-bubble';
  bubble.textContent = text;

  wrapper.appendChild(bubble);
  return wrapper;
}

function scrollToBottom() {
  chatContainer.scrollTop = chatContainer.scrollHeight;
}

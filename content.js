// Content script running on https://claude.ai/*, https://gemini.google.com/*, and https://grok.com/* (or https://x.com/i/grok*)
// Bridges both the extension UI and the Terminal CLI (via local bridge server)

const IS_GEMINI = window.location.hostname.includes('gemini.google.com');
const IS_GROK = window.location.hostname.includes('grok.com') || window.location.hostname.includes('x.com');
const PROVIDER_NAME = IS_GEMINI ? 'gemini' : IS_GROK ? 'grok' : 'claude';
const DISPLAY_NAME = IS_GEMINI ? 'Gemini' : IS_GROK ? 'Grok' : 'Claude';

console.log(`[${DISPLAY_NAME} Assistant] Content script active on ${window.location.hostname}`);

// Handle messages from extension popup/background
chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message.type === 'PING') {
    sendResponse({ ready: true, provider: PROVIDER_NAME, url: window.location.href });
    return false;
  }

  if (message.type === 'INJECT_PROMPT') {
    // If target is specified and doesn't match this tab, ignore
    if (message.target && message.target !== 'all' && message.target.toLowerCase() !== PROVIDER_NAME) {
      sendResponse({ skipped: true, provider: PROVIDER_NAME });
      return false;
    }

    injectAndSend(message.text, message.files, (finalText) => {
      chrome.runtime.sendMessage({ type: 'AI_DONE', provider: PROVIDER_NAME, text: finalText }).catch(() => {});
    })
      .then(() => sendResponse({ success: true, provider: PROVIDER_NAME }))
      .catch((err) => sendResponse({ success: false, error: err.message, provider: PROVIDER_NAME }));
    return true; // async response
  }
});

// Helper to find input field (Gemini, Grok, or Claude)
function findEditor() {
  if (IS_GEMINI) {
    return (
      document.querySelector('rich-textarea div[contenteditable="true"]') ||
      document.querySelector('div.ql-editor[contenteditable="true"]') ||
      document.querySelector('div[contenteditable="true"][role="textbox"]') ||
      document.querySelector('div[contenteditable="true"]') ||
      document.querySelector('textarea[aria-label*="prompt" i]') ||
      document.querySelector('textarea')
    );
  }

  if (IS_GROK) {
    return (
      document.querySelector('textarea[aria-label*="Grok" i]') ||
      document.querySelector('textarea[aria-label*="Ask" i]') ||
      document.querySelector('textarea[placeholder*="Ask Grok" i]') ||
      document.querySelector('textarea[placeholder*="Ask anything" i]') ||
      document.querySelector('textarea[placeholder*="Ask" i]') ||
      document.querySelector('div[contenteditable="true"][aria-label*="Grok" i]') ||
      document.querySelector('div[contenteditable="true"][aria-label*="Ask" i]') ||
      document.querySelector('div[contenteditable="true"][role="textbox"]') ||
      document.querySelector('div[contenteditable="true"]') ||
      document.querySelector('textarea')
    );
  }

  return (
    document.querySelector('div[contenteditable="true"].ProseMirror') ||
    document.querySelector('div[contenteditable="true"]') ||
    document.querySelector('div[role="textbox"]') ||
    document.querySelector('textarea')
  );
}

// Convert file payload (name, content, encoding, type) to native browser File object
function createFileObject(fileInfo) {
  try {
    let blob;
    const mimeType = fileInfo.type || 'application/octet-stream';
    if (fileInfo.encoding === 'base64') {
      const byteCharacters = atob(fileInfo.content || '');
      const byteNumbers = new Array(byteCharacters.length);
      for (let i = 0; i < byteCharacters.length; i++) {
        byteNumbers[i] = byteCharacters.charCodeAt(i);
      }
      const byteArray = new Uint8Array(byteNumbers);
      blob = new Blob([byteArray], { type: mimeType });
    } else {
      blob = new Blob([fileInfo.content || ''], { type: mimeType });
    }

    return new File([blob], fileInfo.name, {
      type: mimeType,
      lastModified: Date.now()
    });
  } catch (err) {
    console.error(`[${DISPLAY_NAME} Assistant] Failed to create File object for:`, fileInfo?.name, err);
    return null;
  }
}

// Find file input element on page
function findFileInput() {
  const editor = findEditor();
  const promptCard =
    editor?.closest('fieldset') ||
    editor?.closest('form') ||
    editor?.closest('[class*="input" i]') ||
    editor?.closest('[class*="prompt" i]') ||
    editor?.parentElement?.parentElement;

  if (promptCard) {
    const input = promptCard.querySelector('input[type="file"]');
    if (input) return input;
  }

  const allInputs = Array.from(document.querySelectorAll('input[type="file"]'));
  if (allInputs.length > 0) {
    return allInputs[allInputs.length - 1];
  }
  return null;
}

// Attach exact File objects to the web interface
async function attachFilesToWeb(fileObjects) {
  if (!fileObjects || fileObjects.length === 0) return false;

  console.log(`[${DISPLAY_NAME} Assistant] 📎 Attaching ${fileObjects.length} exact file(s):`, fileObjects.map(f => f.name));

  const dt = new DataTransfer();
  for (const f of fileObjects) {
    if (f) dt.items.add(f);
  }

  // 1. Direct file input assignment
  const fileInput = findFileInput();
  if (fileInput) {
    try {
      fileInput.files = dt.files;
      fileInput.dispatchEvent(new Event('change', { bubbles: true, composed: true }));
      fileInput.dispatchEvent(new Event('input', { bubbles: true, composed: true }));
      console.log(`[${DISPLAY_NAME} Assistant] Files attached via input[type="file"]`);
      await new Promise((r) => setTimeout(r, 600));
      return true;
    } catch (e) {
      console.warn(`[${DISPLAY_NAME} Assistant] input[type="file"] assignment failed, trying fallback:`, e);
    }
  }

  // 2. Fallback: Clipboard Paste event on editor
  const editor = findEditor();
  if (editor) {
    try {
      editor.focus();
      const pasteEvent = new ClipboardEvent('paste', {
        bubbles: true,
        cancelable: true,
        composed: true,
        clipboardData: dt
      });
      const dispatched = editor.dispatchEvent(pasteEvent);
      if (dispatched) {
        console.log(`[${DISPLAY_NAME} Assistant] Files dispatched via editor paste event`);
        await new Promise((r) => setTimeout(r, 600));
        return true;
      }
    } catch (e) {
      console.warn(`[${DISPLAY_NAME} Assistant] Paste event fallback failed, trying drag/drop:`, e);
    }
  }

  // 3. Fallback: Drag and Drop
  const dropTarget =
    document.querySelector('[data-testid*="drop" i]') ||
    editor?.closest('fieldset') ||
    editor?.closest('form') ||
    editor;

  if (dropTarget) {
    try {
      const dragEnter = new DragEvent('dragenter', { bubbles: true, cancelable: true, composed: true, dataTransfer: dt });
      const dragOver = new DragEvent('dragover', { bubbles: true, cancelable: true, composed: true, dataTransfer: dt });
      const drop = new DragEvent('drop', { bubbles: true, cancelable: true, composed: true, dataTransfer: dt });

      dropTarget.dispatchEvent(dragEnter);
      dropTarget.dispatchEvent(dragOver);
      dropTarget.dispatchEvent(drop);
      console.log(`[${DISPLAY_NAME} Assistant] Files dispatched via drop event`);
      await new Promise((r) => setTimeout(r, 600));
      return true;
    } catch (e) {
      console.warn(`[${DISPLAY_NAME} Assistant] Drop event fallback failed:`, e);
    }
  }

  return false;
}

// Wait until all file attachments are fully uploaded and processed
async function waitForAttachmentsReady(editor, maxWaitMs = 45000) {
  const startTime = Date.now();
  console.log(`[${DISPLAY_NAME} Assistant] ⏳ Waiting for file attachment(s) to finish uploading/processing...`);

  await new Promise((r) => setTimeout(r, 800));

  while (Date.now() - startTime < maxWaitMs) {
    const promptCard =
      editor?.closest('fieldset') ||
      editor?.closest('form') ||
      editor?.closest('[class*="input" i]') ||
      editor?.closest('[class*="prompt" i]') ||
      editor?.parentElement?.parentElement ||
      document.body;

    const isUploading =
      promptCard.querySelector('[class*="animate-spin" i]') ||
      promptCard.querySelector('[class*="loading" i]') ||
      promptCard.querySelector('[class*="uploading" i]') ||
      promptCard.querySelector('[class*="progress" i]') ||
      promptCard.querySelector('[aria-busy="true"]') ||
      promptCard.querySelector('mat-progress-spinner') ||
      promptCard.querySelector('progress') ||
      promptCard.querySelector('[role="progressbar"]');

    if (isUploading) {
      await new Promise((r) => setTimeout(r, 400));
      continue;
    }

    const sendBtn = findSendButton(editor);
    const isSendDisabled = sendBtn && (sendBtn.disabled || sendBtn.getAttribute('aria-disabled') === 'true');

    if (sendBtn && !isSendDisabled) {
      await new Promise((r) => setTimeout(r, 500));
      return true;
    }

    await new Promise((r) => setTimeout(r, 400));
  }

  return false;
}

// Find Send button
function findSendButton(editor) {
  if (IS_GEMINI) {
    const geminiCandidates = [
      document.querySelector('button[aria-label*="Send message" i]'),
      document.querySelector('button[aria-label*="Send prompt" i]'),
      document.querySelector('button[aria-label*="Send" i]'),
      document.querySelector('button.send-button'),
      document.querySelector('button[mattooltip*="Send" i]'),
      document.querySelector('button.send-button-container'),
      document.querySelector('button:has(mat-icon[fonticon*="send"])'),
      document.querySelector('button:has(mat-icon[data-mat-icon-name*="send"])')
    ].filter(Boolean);

    for (const btn of geminiCandidates) {
      const label = (
        (btn.getAttribute('aria-label') || '') +
        ' ' +
        (btn.getAttribute('mattooltip') || '') +
        ' ' +
        btn.className
      ).toLowerCase();
      if (!label.includes('stop') && !label.includes('cancel')) {
        return btn;
      }
    }
    return null;
  }

  if (IS_GROK) {
    if (editor) {
      const parentForm = editor.closest('form') || editor.closest('fieldset') || editor.parentElement?.parentElement;
      if (parentForm) {
        const formBtns = Array.from(parentForm.querySelectorAll('button'));
        const submitBtn = formBtns.find((b) => {
          const l = (
            (b.getAttribute('aria-label') || '') +
            ' ' +
            (b.getAttribute('data-testid') || '') +
            ' ' +
            b.type +
            ' ' +
            b.className
          ).toLowerCase();
          return (
            (l.includes('send') || l.includes('submit') || l.includes('grok') || b.type === 'submit') &&
            !l.includes('stop') &&
            !l.includes('cancel') &&
            !l.includes('attach') &&
            !l.includes('upload')
          );
        });
        if (submitBtn) return submitBtn;
      }
    }

    const grokCandidates = [
      document.querySelector('button[aria-label*="Grok" i]'),
      document.querySelector('button[aria-label*="Send" i]'),
      document.querySelector('button[aria-label*="Submit" i]'),
      document.querySelector('button[data-testid*="send" i]'),
      document.querySelector('button[data-testid*="grok" i]'),
      document.querySelector('button[type="submit"]'),
      document.querySelector('button:has(svg)')
    ].filter(Boolean);

    for (const btn of grokCandidates) {
      const label = (
        (btn.getAttribute('aria-label') || '') +
        ' ' +
        (btn.getAttribute('data-testid') || '') +
        ' ' +
        btn.className
      ).toLowerCase();
      if (!label.includes('stop') && !label.includes('cancel') && !label.includes('attach') && !label.includes('upload')) {
        return btn;
      }
    }
    return null;
  }

  if (!editor) return null;

  const promptCard =
    editor.closest('fieldset') ||
    editor.closest('form') ||
    editor.closest('[class*="input" i]') ||
    editor.closest('[class*="prompt" i]') ||
    editor.parentElement?.parentElement;

  if (!promptCard) return null;

  const buttons = Array.from(promptCard.querySelectorAll('button')).filter((btn) => {
    if (
      btn.closest('nav') ||
      btn.closest('aside') ||
      btn.closest('header') ||
      btn.closest('[role="navigation"]') ||
      btn.closest('[data-testid*="user" i]') ||
      btn.closest('[data-testid*="profile" i]')
    ) {
      return false;
    }

    const info = (
      (btn.getAttribute('aria-label') || '') +
      ' ' +
      (btn.getAttribute('data-testid') || '') +
      ' ' +
      btn.className
    ).toLowerCase();

    // Ignore file attach, model selector, or stop buttons
    if (info.includes('attach') || info.includes('upload') || info.includes('model') || info.includes('style') || info.includes('mic') || info.includes('stop') || info.includes('cancel')) {
      return false;
    }

    return true;
  });

  if (buttons.length === 0) return null;

  // 1. Look for send or submit aria-label
  const sendLabeled = buttons.find((btn) => {
    const label = (btn.getAttribute('aria-label') || '').toLowerCase();
    return (label.includes('send') || label.includes('submit')) && !label.includes('stop');
  });
  if (sendLabeled) return sendLabeled;

  // 2. Look for up-arrow SVG inside prompt card
  const arrowUp = buttons.find((btn) => {
    const svg = btn.querySelector('svg');
    return svg !== null;
  });
  if (arrowUp) return arrowUp;

  // 3. Fallback: bottom-right button inside the prompt card
  return buttons[buttons.length - 1];
}

// Wait for send button to be ready and clickable
async function waitForSendReady(editor, maxWaitMs = 8000) {
  const start = Date.now();
  while (Date.now() - start < maxWaitMs) {
    const sendBtn = findSendButton(editor);
    if (sendBtn) {
      const isDisabled = sendBtn.disabled || sendBtn.getAttribute('aria-disabled') === 'true';
      if (!isDisabled) {
        return sendBtn;
      }
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  return findSendButton(editor);
}

// Check if currently generating response
function isCurrentlyGenerating() {
  if (IS_GEMINI) {
    // Primary signal: the Stop button appears while Gemini is generating
    const stopBtn =
      document.querySelector('button[aria-label*="Stop response" i]') ||
      document.querySelector('button[aria-label*="Stop generating" i]') ||
      document.querySelector('button[mattooltip*="Stop response" i]') ||
      document.querySelector('button[mattooltip*="Stop generating" i]') ||
      document.querySelector('button.stop-button');

    if (stopBtn) return true;

    // Secondary: spinner *only* inside model-response (not page-level loading spinners)
    const responseArea = document.querySelector('model-response, message-content, conversation-container');
    if (responseArea) {
      const localSpinner =
        responseArea.querySelector('mat-progress-spinner') ||
        responseArea.querySelector('div.sparkle-spinner') ||
        responseArea.querySelector('div.streaming-animation') ||
        responseArea.querySelector('.loading-indicator');
      if (localSpinner) return true;
    }

    return false;
  }

  if (IS_GROK) {
    return (
      document.querySelector('button[aria-label*="Stop" i]') !== null ||
      document.querySelector('div[aria-label*="Stop" i]') !== null ||
      document.querySelector('button[aria-label*="Cancel" i]') !== null
    );
  }

  return (
    document.querySelector('button[aria-label*="Stop Response" i]') !== null ||
    document.querySelector('button[aria-label*="Stop generating" i]') !== null ||
    document.querySelector('button[aria-label*="Stop" i]') !== null ||
    document.querySelector('div[data-is-streaming="true"]') !== null ||
    document.querySelector('button[aria-label*="Cancel" i]') !== null
  );
}

// Trigger send reliably (Single clean action: button click OR Enter key, never double trigger)
async function triggerSend(editor) {
  if (!editor) return;

  const sendBtn = await waitForSendReady(editor, 4000);

  // Preferred: single click on send button if found and not generating
  if (sendBtn) {
    const label = (
      (sendBtn.getAttribute('aria-label') || '') +
      ' ' +
      (sendBtn.getAttribute('mattooltip') || '') +
      ' ' +
      sendBtn.className
    ).toLowerCase();

    const isStopBtn = label.includes('stop') || label.includes('cancel') || label.includes('pause');

    if (!isStopBtn) {
      console.log(`[${DISPLAY_NAME} Assistant] Clicking prompt send button`);
      if (sendBtn.disabled || sendBtn.getAttribute('aria-disabled') === 'true') {
        sendBtn.disabled = false;
        sendBtn.removeAttribute('aria-disabled');
      }

      sendBtn.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true }));
      sendBtn.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }));
      sendBtn.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, cancelable: true }));
      sendBtn.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true }));
      sendBtn.click();
      return; // Single click executed, done!
    }
  }

  // Fallback: Dispatch Enter key only if no send button was clicked
  console.log(`[${DISPLAY_NAME} Assistant] Dispatching Enter key on editor`);
  const eventOptions = {
    key: 'Enter',
    code: 'Enter',
    keyCode: 13,
    which: 13,
    charCode: 13,
    bubbles: true,
    cancelable: true,
    composed: true
  };

  editor.dispatchEvent(new KeyboardEvent('keydown', eventOptions));
  editor.dispatchEvent(new KeyboardEvent('keypress', eventOptions));
  editor.dispatchEvent(new KeyboardEvent('keyup', eventOptions));
}

// Inject prompt and/or attached files into AI
async function injectAndSend(promptText, files, onComplete) {
  if (typeof files === 'function') {
    onComplete = files;
    files = null;
  }

  // Wait up to 8s for the editor to appear
  let editor = null;
  const editorDeadline = Date.now() + 8000;
  while (!editor && Date.now() < editorDeadline) {
    editor = findEditor();
    if (!editor) await new Promise((r) => setTimeout(r, 300));
  }

  if (!editor) {
    throw new Error(`${DISPLAY_NAME} input box not found. Make sure you are logged in to ${window.location.hostname}`);
  }

  // 1. Attach exact files if provided
  if (files && Array.isArray(files) && files.length > 0) {
    const fileObjects = files.map(createFileObject).filter(Boolean);
    if (fileObjects.length > 0) {
      await attachFilesToWeb(fileObjects);
      await waitForAttachmentsReady(editor);
    }
  }

  const textToInject = (promptText || '').trim();
  if (textToInject) {
    editor.focus();

    if (editor.isContentEditable) {
      const range = document.createRange();
      range.selectNodeContents(editor);
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);

      const ok = document.execCommand('insertText', false, textToInject);
      if (!ok) {
        const p = editor.querySelector('p') || editor;
        p.textContent = textToInject;
      }
    } else {
      editor.value = textToInject;
    }

    // Fire input events for React / Angular / ProseMirror state
    editor.dispatchEvent(new InputEvent('beforeinput', { bubbles: true, cancelable: true, inputType: 'insertText', data: textToInject }));
    editor.dispatchEvent(new Event('input', { bubbles: true }));
    editor.dispatchEvent(new Event('change', { bubbles: true }));

    await new Promise((r) => setTimeout(r, 200));
  } else {
    editor.focus();
    await new Promise((r) => setTimeout(r, 100));
  }

  // Trigger send
  await triggerSend(editor);

  if (onComplete) {
    observeResponse(onComplete);
  }
}

// Scrape assistant message
function getLatestAssistantMessage() {
  if (IS_GEMINI) {
    const geminiSelectors = [
      'model-response',
      'message-content',
      'div.model-response-text',
      'div.markdown-main-panel',
      'div.markdown',
      'div.response-content',
      'response-container',
      'div[data-test-id*="model-response"]',
      'div[class*="model-response"]',
      'div[class*="response-container"]',
      'div.sparkle-content',
      'div.presented-content'
    ];
    for (const selector of geminiSelectors) {
      const list = document.querySelectorAll(selector);
      if (list.length > 0) {
        return list[list.length - 1];
      }
    }

    const conv = document.querySelector('conversation-container, main, div[role="main"]');
    if (conv) {
      const markdowns = conv.querySelectorAll('div.markdown, div.markdown-main-panel');
      if (markdowns.length > 0) {
        return markdowns[markdowns.length - 1];
      }
    }
    return null;
  }

  if (IS_GROK) {
    const grokSelectors = [
      'div[data-testid*="message-text" i]',
      'div.message-text',
      'div.message-bubble',
      'div.response-bubble',
      'div[class*="response-message"]',
      'div[class*="message-bubble"]',
      'div[dir="auto"].grok-message',
      'div[data-testid*="grok" i]',
      'div.markdown-body',
      'div.prose',
      'div[class*="prose"]'
    ];
    for (const selector of grokSelectors) {
      const list = document.querySelectorAll(selector);
      if (list.length > 0) {
        return list[list.length - 1];
      }
    }
    return null;
  }

  const selectors = [
    '[data-message-author-role="assistant"]',
    'div[data-is-streaming="true"]',
    'div[data-is-streaming="false"]',
    'div[data-is-streaming]',
    '.font-claude-message',
    '.font-claude-response',
    '.font-claude-chat',
    '[data-testid*="assistant-message"]',
    '[data-testid*="chat-message-assistant"]',
    '[data-testid*="assistant"]',
    '[data-testid*="message-content"]',
    'div.standard-markdown',
    'div.rendered-markdown',
    'div.font-claude-message div.grid-cols-1',
    '[class*="font-claude-message"]',
    '[class*="font-claude"]',
    '.prose',
    'div[class*="markdown"]'
  ];

  for (const selector of selectors) {
    const list = document.querySelectorAll(selector);
    if (list.length > 0) {
      // Find the last element not inside a user query
      for (let i = list.length - 1; i >= 0; i--) {
        const el = list[i];
        if (!el.closest('[data-message-author-role="user"]') && !el.closest('.font-user-message')) {
          return el;
        }
      }
      return list[list.length - 1];
    }
  }
  return null;
}

// Scrape user message
function getLatestUserMessage() {
  if (IS_GEMINI) {
    const geminiUserSelectors = [
      'user-query',
      'div.user-query-container',
      '[data-test-id="user-query"]',
      'div.query-text',
      'div[class*="user-query"]'
    ];
    for (const selector of geminiUserSelectors) {
      const list = document.querySelectorAll(selector);
      if (list.length > 0) {
        return list[list.length - 1];
      }
    }
    return null;
  }

  if (IS_GROK) {
    const grokUserSelectors = [
      'div[data-testid*="user" i]',
      'div.user-message',
      'div[class*="user-message"]',
      'div.query-text',
      'div[dir="auto"].user-query'
    ];
    for (const selector of grokUserSelectors) {
      const list = document.querySelectorAll(selector);
      if (list.length > 0) {
        return list[list.length - 1];
      }
    }
    return null;
  }

  const selectors = [
    '[data-message-author-role="user"]',
    '.font-user-message',
    'div[data-testid*="user-message"]',
    'div[data-testid*="chat-message-user"]',
    '[class*="font-user"]',
    '[class*="user-message"]'
  ];

  for (const selector of selectors) {
    const list = document.querySelectorAll(selector);
    if (list.length > 0) {
      return list[list.length - 1];
    }
  }
  return null;
}

// Observe response stream for single prompt injection
function observeResponse(onComplete) {
  let lastText = '';
  let responseStarted = false;
  let lastChangeTime = null; // Only start timing after first token arrives
  let completed = false;

  const finish = (text) => {
    if (completed) return;
    completed = true;
    observer.disconnect();
    clearInterval(fallbackInterval);
    console.log(`[${DISPLAY_NAME} Assistant] ✅ Response finished (length: ${text.length})`);
    if (onComplete) onComplete(text);
  };

  const checkCompletion = () => {
    if (completed) return;

    const latestEl = getLatestAssistantMessage();
    if (latestEl) {
      const text = (latestEl.innerText || latestEl.textContent || '').trim();
      if (text && text !== lastText) {
        responseStarted = true;
        lastText = text;
        lastChangeTime = Date.now(); // Reset only when new content arrives

        chrome.runtime.sendMessage({
          type: 'AI_STREAMING',
          provider: PROVIDER_NAME,
          text
        }).catch(() => {});
      }
    }

    // Only evaluate completion once response has started
    if (!responseStarted || !lastText || lastChangeTime === null) return;

    const isGenerating = isCurrentlyGenerating();
    const elapsedSinceChange = Date.now() - lastChangeTime;

    // Completed if:
    // 1) Not generating AND text stable for 2s
    // 2) Text stable for 5s regardless of generating state (stuck fallback)
    if ((!isGenerating && elapsedSinceChange > 2000) || elapsedSinceChange > 5000) {
      finish(lastText);
    }
  };

  const observer = new MutationObserver(checkCompletion);
  observer.observe(document.body, {
    childList: true,
    subtree: true,
    characterData: true
  });

  // Poll every 400ms as a safety net in case MutationObserver misses events
  const fallbackInterval = setInterval(checkCompletion, 400);

  // Hard timeout: 4 minutes max
  setTimeout(() => {
    if (!completed && lastText) finish(lastText);
    else if (!completed) {
      completed = true;
      observer.disconnect();
      clearInterval(fallbackInterval);
    }
  }, 240000);
}

// --- WebSocket Bridge for Continuous AI Observation ---
let bridgeWs = null;
let reconnectTimer = null;

function connectWebSocketBridge() {
  if (bridgeWs && (bridgeWs.readyState === WebSocket.OPEN || bridgeWs.readyState === WebSocket.CONNECTING)) {
    return;
  }

  try {
    bridgeWs = new WebSocket('ws://127.0.0.1:5005/ws');

    bridgeWs.onopen = () => {
      console.log(`[${DISPLAY_NAME} Assistant] ⚡ Persistent WebSocket bridge connected`);
      sendToBridgeWs({ type: 'REGISTER', provider: PROVIDER_NAME, url: window.location.href });
    };

    bridgeWs.onmessage = async (event) => {
      try {
        const msg = JSON.parse(event.data);
        if (msg.type === 'PROMPT' && (msg.prompt || (msg.files && msg.files.length > 0))) {
          const target = (msg.target || msg.provider || msg.model || '').toLowerCase().trim();
          // If a specific target is set and it does not match this tab's provider, skip!
          if (target && target !== 'all' && target !== PROVIDER_NAME) {
            console.log(`[${DISPLAY_NAME} Assistant] ⏩ Skipping prompt (target was '${target}', this tab is '${PROVIDER_NAME}')`);
            return;
          }
          // If no target was specified at all, default to claude only
          if (!target && PROVIDER_NAME !== 'claude') {
            console.log(`[${DISPLAY_NAME} Assistant] ⏩ Skipping untargeted prompt (not claude)`);
            return;
          }

          console.log(`[${DISPLAY_NAME} Assistant] 🚀 Processing prompt for ${DISPLAY_NAME}:`, msg.prompt, 'Files:', msg.files?.length || 0);
          await injectAndSend(msg.prompt, msg.files, async (finalText) => {
            if (finalText) {
              console.log(`[${DISPLAY_NAME} Assistant] 📤 Direct injection completed -> sending response to bridge (length: ${finalText.length})`);
              const sentWs = sendToBridgeWs({ type: 'DONE', provider: PROVIDER_NAME, text: finalText });
              if (!sentWs) {
                try {
                  await fetch('http://127.0.0.1:5005/response', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ response: finalText, provider: PROVIDER_NAME })
                  });
                } catch (e) {}
              }
            }
          });
        }
      } catch (err) {
        console.error(`[${DISPLAY_NAME} Assistant] Error handling WebSocket message:`, err);
      }
    };

    bridgeWs.onclose = () => {
      bridgeWs = null;
      clearTimeout(reconnectTimer);
      reconnectTimer = setTimeout(connectWebSocketBridge, 2000);
    };

    bridgeWs.onerror = () => {
      if (bridgeWs) bridgeWs.close();
    };
  } catch (e) {
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(connectWebSocketBridge, 2000);
  }
}

function sendToBridgeWs(payload) {
  if (bridgeWs && bridgeWs.readyState === WebSocket.OPEN) {
    if (!payload.provider) {
      payload.provider = PROVIDER_NAME;
    }
    bridgeWs.send(JSON.stringify(payload));
    return true;
  }
  return false;
}

// Connect WebSocket bridge
connectWebSocketBridge();
setInterval(connectWebSocketBridge, 4000);

// --- Global Observer for ALL AI Messages (Web & Terminal) ---
let globalLastSentText = '';
let globalLastUserText = '';
let globalInactivityTimer = null;
let globalLastAssistantText = '';
let globalLastAssistantChangeTime = 0;
let globalCompletionScheduled = false;

async function emitCompletedResponse(text) {
  if (text === globalLastSentText) return; // Already sent this exact response
  globalLastSentText = text;
  console.log(`[${DISPLAY_NAME} Assistant] 📤 Global observer: Completed response (length: ${text.length})`);

  const sentWs = sendToBridgeWs({ type: 'DONE', provider: PROVIDER_NAME, text });
  if (!sentWs) {
    try {
      await fetch('http://127.0.0.1:5005/response', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ response: text, provider: PROVIDER_NAME })
      });
    } catch (e) {
      // Bridge offline, silent ignore
    }
  }
}

function startGlobalMessageObserver() {
  const checkGlobal = () => {
    // 1. Observe user prompts typed on web UI
    const latestUserEl = getLatestUserMessage();
    if (latestUserEl) {
      const userText = (latestUserEl.innerText || latestUserEl.textContent || '').trim();
      if (userText && userText !== globalLastUserText) {
        globalLastUserText = userText;
        sendToBridgeWs({ type: 'USER_MESSAGE', provider: PROVIDER_NAME, text: userText });
      }
    }

    const latestEl = getLatestAssistantMessage();
    if (!latestEl) return;

    const text = (latestEl.innerText || latestEl.textContent || '').trim();
    if (!text) return;

    const isGenerating = isCurrentlyGenerating();

    // Track changes to assistant text
    if (text !== globalLastAssistantText) {
      globalLastAssistantText = text;
      globalLastAssistantChangeTime = Date.now();
      globalCompletionScheduled = false; // Reset when new text arrives

      // Stream live tokens
      if (isGenerating) {
        sendToBridgeWs({ type: 'STREAMING', provider: PROVIDER_NAME, text });
      }
    }

    // Only check for completion if we have text and it's been stable for a bit
    if (!globalLastAssistantText || globalLastAssistantChangeTime === 0) return;

    const elapsedStable = Date.now() - globalLastAssistantChangeTime;

    // Emit DONE when:
    // 1) Not generating and stable for 2s
    // 2) Stable for 6s regardless (stuck/no spinner)
    if (!globalCompletionScheduled && ((!isGenerating && elapsedStable > 2000) || elapsedStable > 6000)) {
      globalCompletionScheduled = true;
      emitCompletedResponse(globalLastAssistantText);
    }
  };

  const observer = new MutationObserver(checkGlobal);
  observer.observe(document.body, {
    childList: true,
    subtree: true,
    characterData: true
  });

  // Periodic fallback check every 600ms
  setInterval(checkGlobal, 600);
}

// Start global watcher
startGlobalMessageObserver();

// --- Terminal Bridge Polling (HTTP Fallback) ---
async function pollTerminalBridge() {
  if (bridgeWs && bridgeWs.readyState === WebSocket.OPEN) {
    return;
  }

  try {
    const response = await fetch('http://127.0.0.1:5005/poll', { cache: 'no-store' });
    if (!response.ok) return;

    const data = await response.json();
    if (data && (data.prompt || (data.files && data.files.length > 0))) {
      const target = (data.target || data.model || data.provider || '').toLowerCase().trim();
      if (target && target !== 'all' && target !== PROVIDER_NAME) {
        return;
      }
      if (!target && PROVIDER_NAME !== 'claude') {
        return;
      }

      console.log(`[${DISPLAY_NAME} Assistant] Received prompt/files from Terminal (HTTP fallback):`, data.prompt, 'Files:', data.files?.length || 0);

      await injectAndSend(data.prompt, data.files, async (answer) => {
        console.log(`[${DISPLAY_NAME} Assistant] Returning answer to Terminal Bridge`);
        try {
          await fetch('http://127.0.0.1:5005/response', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ response: answer, provider: PROVIDER_NAME })
          });
        } catch (postErr) {
          console.error(`[${DISPLAY_NAME} Assistant] Failed to send answer to bridge:`, postErr);
        }
      });
    }
  } catch (err) {
    // Local bridge not running; silent ignore
  }
}

// Poll every 1.5 seconds as fallback
setInterval(pollTerminalBridge, 1500);

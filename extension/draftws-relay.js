// Forwards draft-room messages from the page to the extension background.
window.addEventListener("message", (ev) => {
  if (ev.source === window && ev.data && ev.data.__draftpick) {
    chrome.runtime.sendMessage({ type: "draftws", url: ev.data.url, data: ev.data.data });
  }
});

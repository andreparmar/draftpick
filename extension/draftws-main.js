// Runs inside the ESPN draft room page. Mirrors every live draft message
// (the draft room's own WebSocket) to the relay so draftpick sees picks live.
(() => {
  const Native = window.WebSocket;
  function Wrapped(url, protocols) {
    const ws = protocols === undefined ? new Native(url) : new Native(url, protocols);
    ws.addEventListener("message", (ev) => {
      if (typeof ev.data === "string") window.postMessage({ __draftpick: true, url: String(url), data: ev.data }, "*");
    });
    return ws;
  }
  Wrapped.prototype = Native.prototype;
  Object.assign(Wrapped, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
  window.WebSocket = Wrapped;
})();

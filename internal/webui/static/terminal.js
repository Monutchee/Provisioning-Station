// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0
import { hterm, lib } from "./vendor/hterm/hterm.js";
import { TerminalOutput } from "./terminal-output.js";

const scrollbackLimit = 10000;
let initialize;

async function initializeHterm() {
  await hterm.initPromise;
  const response = await fetch("/vendor/hterm/messages.en.json");
  if (!response.ok) throw new Error("The embedded terminal messages could not be loaded.");
  hterm.messageManager.addMessages(await response.json());
  // The pinned upstream status response omits the CSI bracket for DSR 5.
  const deviceStatus = hterm.VT.CSI.n;
  hterm.VT.CSI.n = function (parseState) {
    if (parseState.args[0] == 5) this.terminal.io.sendString("\x1b[0n");
    else deviceStatus.call(this, parseState);
  };
  hterm.notify = () => {};
  hterm.openUrl = () => {};
  for (const code of ["0", "2", "8", "9", "50", "52", "1337", "777"]) hterm.VT.OSC[code] = () => {};
}

export async function createTerminal(element, onInput) {
  initialize ||= initializeHterm().catch((error) => { initialize = null; throw error; });
  await initialize;
  const terminal = new hterm.Terminal({ storage: new lib.Storage.Memory() });
  terminal.vt.warnUnimplemented = false;
  terminal.setWindowTitle = () => {};
  const output = new TerminalOutput();
  let decoder = new TextDecoder("utf-8");
  let disposed = false;
  let observer;
  let io;
  const fit = () => { if (!disposed) terminal.scrollPort_.resize(); };

  // hterm has no rolling-history setting. Keep its row indices and viewport
  // cache consistent when dropping only the oldest rows (pinned upstream API).
  const trimScrollback = () => {
    const removed = terminal.scrollbackRows_.length - scrollbackLimit;
    if (removed <= 0) return;
    const port = terminal.scrollPort_;
    const atEnd = port.isScrolledEnd;
    const top = port.getTopRowIndex();
    port.selection.sync();
    if (port.selection.startRow && port.selection.startRow.rowIndex < removed) {
      port.getDocument().getSelection().removeAllRanges();
    }
    terminal.scrollbackRows_.splice(0, removed);
    terminal.scrollbackRows_.forEach((row, index) => { row.rowIndex = index; });
    terminal.renumberRows_(0, terminal.primaryScreen_.getHeight(), terminal.primaryScreen_);
    terminal.renumberRows_(0, terminal.alternateScreen_.getHeight(), terminal.alternateScreen_);
    port.resetCache();
    if (atEnd) terminal.scrollEnd();
    else port.scrollRowToTop(Math.max(0, top - removed));
    // Full redraw preserves selected row nodes; invalidate removes them.
    port.redraw_();
    terminal.syncCursorPosition_();
  };

  try {
    await new Promise((resolve, reject) => {
      terminal.onTerminalReady = () => {
        try {
          const preferences = {
            "background-color": "#0c1210", "foreground-color": "#dce9e1",
            "cursor-color": "#56c79d", "cursor-shape": "BLOCK", "cursor-blink": true,
            "font-family": "ui-monospace, SFMono-Regular, Consolas, monospace", "font-size": 13,
            "terminal-encoding": "utf-8-locked", "enable-8-bit-control": false, "enable-clipboard-write": false,
            "allow-images-inline": false, "desktop-notification-bell": false,
            "audible-bell-sound": "", "copy-on-select": false,
            "backspace-sends-backspace": false,
          };
          for (const [key, value] of Object.entries(preferences)) terminal.prefs_.set(key, value);
          io = terminal.io.push();
          // Both keyboard/paste input and automatic terminal replies use the
          // caller's ready/controller/socket guard. Observers never transmit.
          io.onVTKeystroke = io.sendString = (text) => { if (!disposed) onInput(text); };
          io.onTerminalResize = trimScrollback;
          terminal.installKeyboard();
          if (globalThis.ResizeObserver) {
            observer = new ResizeObserver(fit);
            observer.observe(element);
          } else window.addEventListener("resize", fit);
          fit();
          resolve();
        } catch (error) { reject(error); }
      };
      terminal.decorate(element);
    });
  } catch (error) {
    disposed = true;
    observer?.disconnect();
    window.removeEventListener("resize", fit);
    terminal.uninstallKeyboard();
    terminal.setCursorBlink(false);
    element.replaceChildren();
    throw error;
  }

  const print = (text) => {
    // Bound transient parser/history allocation even for a large transcript.
    for (let offset = 0; offset < text.length;) {
      let end = Math.min(text.length, offset + 16384);
      // Do not split a UTF-16 surrogate pair before hterm counts columns.
      if (end < text.length && /[\uD800-\uDBFF]/.test(text[end - 1])) end--;
      io.print(output.push(text.slice(offset, end)));
      offset = end;
      trimScrollback();
    }
  };
  return {
    write(data) {
      if (disposed) return;
      if (typeof data === "string") print(data);
      else {
        for (let offset = 0; offset < data.length; offset += 16384) {
          print(decoder.decode(data.subarray(offset, offset + 16384), { stream: true }));
        }
      }
    },
    writeln(text) { this.write(text + "\r\n"); },
    reset() {
      if (disposed) return;
      decoder = new TextDecoder("utf-8");
      output.clear();
      terminal.reset();
      terminal.wipeContents();
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      observer?.disconnect();
      window.removeEventListener("resize", fit);
      terminal.uninstallKeyboard();
      terminal.setCursorBlink(false);
      io.onVTKeystroke = io.sendString = io.onTerminalResize = () => {};
      terminal.wipeContents();
      for (const timer of Object.values(terminal.timeouts_)) clearTimeout(timer);
      for (const timer of Object.values(terminal.scrollPort_.timeouts_)) clearTimeout(timer);
      clearInterval(terminal.scrollPort_.autoScrollInterval_);
      output.clear();
      element.replaceChildren();
    },
  };
}

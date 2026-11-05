// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Bound control strings across frames before the upstream parser sees them.
// Malformed OSC and cursor arguments can otherwise be logged by hterm verbatim.
export class TerminalOutput {
  mode;
  pending = "";
  discarded = false;
  stringEscape = false;
  started = 0;

  clear() {
    this.mode = undefined;
    this.pending = "";
    this.discarded = false;
    this.stringEscape = false;
  }

  push(text, now = Date.now()) {
    let output = "";
    if (this.mode && now - this.started > 20000) this.clear();
    for (const char of text) {
      if (char === "\x18" || char === "\x1a") { this.clear(); continue; }
      if (!this.mode) {
        if (char === "\x1b") {
          this.mode = "escape"; this.pending = char; this.started = now;
        } else output += char;
        continue;
      }
      if (this.mode === "string") {
        if (char === "\x07" || (this.stringEscape && char === "\\")) {
          const sequence = this.pending + char;
          if (!this.discarded && this.valid(sequence)) output += sequence;
          this.clear();
          continue;
        }
        if (this.stringEscape) {
          this.clear(); this.mode = "escape"; this.pending = "\x1b"; this.started = now;
        } else {
          this.stringEscape = char === "\x1b";
          this.append(char);
          continue;
        }
      }
      if (char === "\x1b") {
        this.clear(); this.mode = "escape"; this.pending = char; this.started = now;
        continue;
      }
      this.append(char);
      if (this.mode === "escape" && this.pending.length === 2) {
        if (char === "[") { this.mode = "csi"; continue; }
        if ("]P^_".includes(char)) { this.mode = "string"; continue; }
      }
      const final = this.mode === "csi" ? char >= "@" && char <= "~" : char >= "0" && char <= "~";
      if (final) {
        if (!this.discarded && this.valid(this.pending)) output += this.pending;
        this.clear();
      }
    }
    return output;
  }

  append(char) {
    if (this.discarded) return;
    if (this.pending.length >= 16384) {
      this.discarded = true;
      this.pending = "";
    } else this.pending += char;
  }

  valid(sequence) {
    if (sequence.startsWith("\x1b]")) {
      if (!/^\x1b\]\d+;?[^\r\n]*(?:\x07|\x1b\\)$/.test(sequence)) return false;
      if (sequence.startsWith("\x1b]4;")) {
        const args = sequence.slice(4).replace(/(?:\x07|\x1b\\)$/, "").split(";");
        if (args.length % 2 !== 0) return false;
        for (let i = 0; i < args.length; i += 2) {
          if (!/^\d{1,3}$/.test(args[i]) || Number(args[i]) > 255) return false;
        }
      }
    }
    if (sequence.startsWith("\x1b[") && sequence.endsWith(" q")) return /^\x1b\[[0-6]? q$/.test(sequence);
    return true;
  }
}

// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

"use strict";

const state = {
  token: sessionStorage.getItem("mnc-station-token") || "",
  capabilities: null,
  artifacts: [],
  targets: [],
  serialPorts: [],
  serialWarnings: [],
  targetSerialPortIds: new Map(),
  targetLoading: false,
  uploadingArtifact: false,
  jobs: [],
  selectedArtifact: "",
  selectedJob: "",
  jobTimer: null,
  toastTimer: null,
  terminal: null,
  fitAddon: null,
  terminalInput: null,
  consoleSocket: null,
  consoleSessionId: "",
  consoleAccess: "",
  consoleReady: false,
  consoleGeneration: 0,
};

const elements = Object.fromEntries([
  "agent-state", "agent-version", "xsdb-state", "tftp-listen", "serial-state", "auth-notice",
  "token-form", "api-token", "drop-zone", "artifact-file", "upload-progress",
  "upload-status", "upload-status-icon", "upload-status-title", "upload-status-message",
  "artifact-select", "artifact-count", "artifact-card", "artifact-vendor",
  "artifact-name", "artifact-details", "artifact-sha", "artifact-built",
  "boot-form", "hw-server-url", "discover-targets", "target-list",
  "tftp-server-ip", "tftp-server-ip-options", "board-ip", "serial-baud", "form-error", "start-button", "refresh-jobs",
  "empty-jobs", "job-workspace", "job-select", "job-state", "job-title",
  "job-meta", "cancel-button", "timeline", "console-status", "console-port",
  "console-port-detail", "console-connect", "console-disconnect", "console-transcript",
  "serial-terminal", "toast",
].map((id) => [id, document.getElementById(id)]));

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (state.token) headers.set("Authorization", `Bearer ${state.token}`);
  if (options.body && typeof options.body === "string") headers.set("Content-Type", "application/json");
  const response = await fetch(path, { ...options, headers });
  const contentType = response.headers.get("Content-Type") || "";
  const payload = contentType.includes("application/json") ? await response.json() : null;
  if (!response.ok) {
    const error = new Error(payload?.error?.message || `${response.status} ${response.statusText}`);
    error.status = response.status;
    throw error;
  }
  return payload;
}

async function apiBytes(path) {
  const headers = new Headers();
  if (state.token) headers.set("Authorization", `Bearer ${state.token}`);
  const response = await fetch(path, { headers });
  if (!response.ok) {
    const contentType = response.headers.get("Content-Type") || "";
    const payload = contentType.includes("application/json") ? await response.json() : null;
    const error = new Error(payload?.error?.message || `${response.status} ${response.statusText}`);
    error.status = response.status;
    throw error;
  }
  return new Uint8Array(await response.arrayBuffer());
}

function setConnected(connected, label) {
  elements["agent-state"].classList.toggle("online", connected);
  elements["agent-state"].classList.toggle("error", !connected);
  elements["agent-state"].querySelector("span:last-child").textContent = label;
}

async function loadCapabilities() {
  try {
    state.capabilities = await api("/api/v1/capabilities");
    elements["agent-version"].textContent = state.capabilities.version || "Local";
    elements["tftp-listen"].textContent = state.capabilities.tftpListen;
    elements["xsdb-state"].textContent = state.capabilities.xsdb.available ? "Ready" : "Not found";
    elements["xsdb-state"].title = state.capabilities.xsdb.path || state.capabilities.xsdb.error || "";
    elements["serial-baud"].value = state.capabilities.serial?.defaultBaudRate || 115200;
    populateStationAddresses();
    elements["auth-notice"].classList.add("hidden");
    setConnected(true, "Agent online");
    await Promise.all([
      loadArtifacts(),
      loadJobs(),
      loadSerialPorts().catch(markSerialUnavailable),
    ]);
  } catch (error) {
    if (error.status === 401) {
      elements["auth-notice"].classList.remove("hidden");
      setConnected(false, "Token required");
      return;
    }
    setConnected(false, "Agent unavailable");
    showToast(error.message);
  }
}

function markSerialUnavailable(error) {
  state.serialPorts = [];
  state.serialWarnings = [{ message: error.message }];
  elements["serial-state"].textContent = "Unavailable";
  elements["serial-state"].title = error.message;
  renderTargets();
  renderConsolePorts();
}

async function loadSerialPorts() {
  const payload = await api("/api/v1/serial/ports");
  state.serialPorts = payload.ports || [];
  state.serialWarnings = payload.warnings || [];
  elements["serial-state"].textContent = state.serialPorts.length ?
    `${state.serialPorts.length} port${state.serialPorts.length === 1 ? "" : "s"}` : "None found";
  elements["serial-state"].title = state.serialWarnings.map((warning) => warning.message).join("\n");
  renderTargets();
  renderConsolePorts();
}

function populateStationAddresses() {
  const interfaces = state.capabilities?.network?.ipv4Interfaces || [];
  const addresses = state.capabilities?.network?.ipv4Addresses || [];
  const options = elements["tftp-server-ip-options"];
  options.replaceChildren(...interfaces.map((networkInterface) => {
    const option = document.createElement("option");
    option.value = networkInterface.address;
    option.label = networkInterface.name;
    return option;
  }));
  const preferred = state.capabilities?.network?.preferredTftpServerIp || addresses[0] || "";
  if (!elements["tftp-server-ip"].value && preferred) {
    elements["tftp-server-ip"].value = preferred;
  }
}

async function loadArtifacts() {
  const payload = await api("/api/v1/artifacts");
  state.artifacts = payload.artifacts || [];
  if (!state.selectedArtifact && state.artifacts.length) state.selectedArtifact = state.artifacts[0].id;
  if (state.selectedArtifact && !state.artifacts.some((item) => item.id === state.selectedArtifact)) {
    state.selectedArtifact = state.artifacts[0]?.id || "";
  }
  renderArtifacts();
}

function renderArtifacts() {
  const select = elements["artifact-select"];
  select.replaceChildren();
  if (!state.artifacts.length) {
    select.append(new Option("Import an artifact first", ""));
  } else {
    for (const item of state.artifacts) {
      const meta = item.manifest.artifact;
      select.append(new Option(`${meta.name} · ${meta.buildId}`, item.id));
    }
  }
  select.value = state.selectedArtifact;
  elements["artifact-count"].textContent = `${state.artifacts.length} artifact${state.artifacts.length === 1 ? "" : "s"}`;
  const selected = state.artifacts.find((item) => item.id === state.selectedArtifact);
  elements["artifact-card"].classList.toggle("hidden", !selected);
  updateStartButton();
  if (!selected) return;
  const meta = selected.manifest.artifact;
  elements["artifact-vendor"].textContent = `${meta.vendor} · ${meta.operation}`;
  elements["artifact-name"].textContent = meta.name;
  elements["artifact-details"].textContent = `${meta.product} / ${meta.machine} · ${meta.version} · ${formatBytes(selected.compressedBytes)}`;
  elements["artifact-sha"].textContent = selected.id;
  elements["artifact-sha"].title = selected.id;
  elements["artifact-built"].textContent = formatTime(meta.createdUtc);
}

async function discoverTargets() {
  showFormError("");
  const hardwareServerURL = elements["hw-server-url"].value.trim();
  if (!hardwareServerURL) {
    showFormError("Enter a hardware server URL before scanning devices.");
    return;
  }
  state.targetLoading = true;
  state.targets = [];
  state.targetSerialPortIds.clear();
  renderTargets();
  elements["discover-targets"].disabled = true;
  elements["discover-targets"].textContent = "Scanning…";
  updateStartButton();
  try {
    const query = new URLSearchParams({ hwServerUrl: hardwareServerURL });
    const payload = await api(`/api/v1/xilinx/targets?${query}`);
    state.targets = payload.targets || [];
    for (const target of state.targets) {
      const portId = target.serialAssociation === "matched" ? target.serialPort?.id || "" : "";
      state.targetSerialPortIds.set(target.id, portId);
    }
    await loadSerialPorts().catch(markSerialUnavailable);
    if (!state.targets.length) {
      showFormError("No ZynqMP PSU targets were found on this hardware server.");
    }
  } catch (error) {
    showFormError(error.message);
  } finally {
    state.targetLoading = false;
    renderTargets();
    elements["discover-targets"].disabled = false;
    elements["discover-targets"].textContent = "Scan devices";
    renderConsolePorts();
    updateStartButton();
  }
}

function renderTargets() {
  const list = elements["target-list"];
  const previouslySelected = new Set(
    Array.from(list.querySelectorAll('input[type="checkbox"]:checked'), (input) => input.value),
  );
  list.replaceChildren();
  if (state.targetLoading) {
    const message = document.createElement("p");
    message.textContent = "Connecting to hw_server and scanning JTAG devices…";
    list.append(message);
    return;
  }
  if (!state.targets.length) {
    const message = document.createElement("p");
    message.textContent = "Scan the hardware server to select a device.";
    list.append(message);
    return;
  }
  const ports = availableConsolePorts();
  const availablePortIds = new Set(ports.map((port) => port.id));
  state.targets.forEach((target, index) => {
    const serialAvailable = target.serialAssociation === "matched" && target.serialPort;
    let selectedPortId = state.targetSerialPortIds.get(target.id) || "";
    if (selectedPortId && !availablePortIds.has(selectedPortId)) {
      selectedPortId = "";
      state.targetSerialPortIds.set(target.id, "");
    }
    const option = document.createElement("div");
    option.className = "target-option";
    const choice = document.createElement("label");
    choice.className = "target-choice";
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.id = `target-${target.id}`;
    checkbox.value = target.id;
    checkbox.checked = previouslySelected.has(target.id) || (!previouslySelected.size && index === 0);
    checkbox.addEventListener("change", () => {
      const port = selectedSerialPort(target);
      if (checkbox.checked && port) {
        elements["console-port"].value = port.id;
        renderConsolePortDetail();
      }
      updateStartButton();
    });
    const description = document.createElement("span");
    const name = document.createElement("strong");
    name.textContent = target.cableName || target.cableSerial || `JTAG target ${target.id}`;
    const details = document.createElement("small");
    const properties = [];
    if (target.cableSerial) properties.push(`serial ${target.cableSerial}`);
    if (target.deviceName) properties.push(target.deviceName);
    if (target.deviceIndex !== undefined && target.deviceIndex !== "") properties.push(`device ${target.deviceIndex}`);
    properties.push(`XSDB target ${target.id}`);
    details.textContent = properties.join(" · ");
    const serialMatch = document.createElement("small");
    serialMatch.className = `serial-match${serialAvailable ? "" : " unmatched"}`;
    if (serialAvailable) {
      serialMatch.textContent = `Automatically matched UART ${target.serialPort.name} · FTDI channel ${target.serialPort.channel}`;
    } else if (target.serialAssociation === "ambiguous") {
      serialMatch.textContent = "Automatic UART match unavailable: duplicate FTDI EEPROM serial";
    } else {
      serialMatch.textContent = "No automatic UART match; select a port or use JTAG only";
    }
    description.append(name, details, serialMatch);
    choice.append(checkbox, description);

    const serialField = document.createElement("label");
    serialField.className = "target-serial-field";
    const serialLabel = document.createElement("span");
    serialLabel.textContent = "Serial capture";
    const serialSelect = document.createElement("select");
    serialSelect.setAttribute("aria-label", `Serial capture for ${name.textContent}`);
    serialSelect.append(new Option("No serial capture (JTAG only)", ""));
    for (const port of ports) {
      const automatic = serialAvailable && target.serialPort.id === port.id;
      const suffix = automatic ? " · automatic match" : "";
      serialSelect.append(new Option(`${serialPortLabel(port)}${suffix}`, port.id));
    }
    serialSelect.value = selectedPortId;
    serialSelect.addEventListener("change", () => {
      state.targetSerialPortIds.set(target.id, serialSelect.value);
      if (serialSelect.value) {
        elements["console-port"].value = serialSelect.value;
        renderConsolePortDetail();
      }
      updateStartButton();
    });
    serialField.append(serialLabel, serialSelect);
    option.append(choice, serialField);
    list.append(option);
  });
}

function selectedTargets() {
  const selectedIDs = new Set(
    Array.from(elements["target-list"].querySelectorAll('input[type="checkbox"]:checked'), (input) => input.value),
  );
  return state.targets.filter((target) => selectedIDs.has(target.id));
}

function selectedSerialPort(target) {
  const portId = state.targetSerialPortIds.get(target.id) || "";
  return availableConsolePorts().find((port) => port.id === portId);
}

function updateStartButton() {
  const artifactSelected = state.artifacts.some((item) => item.id === state.selectedArtifact);
  const targets = selectedTargets();
  const serialSelected = targets.some((target) => selectedSerialPort(target));
  const baudRate = Number(elements["serial-baud"].value);
  const validBaud = !serialSelected || (Number.isInteger(baudRate) && baudRate >= 300 && baudRate <= 4000000);
  elements["start-button"].disabled = !artifactSelected || !state.capabilities?.xsdb?.available ||
    state.targetLoading || targets.length === 0 || !validBaud;
}

async function uploadArtifact(file) {
  if (!file || state.uploadingArtifact) return;
  state.uploadingArtifact = true;
  showArtifactUploadStatus("progress", "Uploading and verifying", file.name);
  elements["upload-progress"].classList.remove("hidden");
  elements["artifact-file"].disabled = true;
  elements["drop-zone"].setAttribute("aria-busy", "true");
  const form = new FormData();
  form.append("artifact", file, file.name);
  try {
    const imported = await api("/api/v1/artifacts", { method: "POST", body: form });
    state.selectedArtifact = imported.id;
    await loadArtifacts();
    showArtifactUploadStatus(
      "success",
      "Upload and verification complete",
      `${file.name} was verified and imported as ${imported.manifest.artifact.name}.`,
    );
  } catch (error) {
    showArtifactUploadStatus("error", "Artifact upload failed", artifactUploadError(error, file));
  } finally {
    state.uploadingArtifact = false;
    elements["upload-progress"].classList.add("hidden");
    elements["artifact-file"].disabled = false;
    elements["artifact-file"].value = "";
    elements["drop-zone"].removeAttribute("aria-busy");
  }
}

function artifactUploadError(error, file) {
  if (error.message.includes("unexpected EOF") || error.message.includes("ended early")) {
    return `${file.name} ended early during verification. Wait for the artifact export to finish, then select the file again.`;
  }
  return error.message;
}

function showArtifactUploadStatus(kind, title, message) {
  const visible = Boolean(kind);
  const status = elements["upload-status"];
  status.className = visible ? `upload-status ${kind}` : "upload-status hidden";
  status.setAttribute("aria-live", kind === "error" ? "assertive" : "polite");
  elements["upload-status-icon"].textContent = { progress: "…", success: "✓", error: "!" }[kind] || "";
  elements["upload-status-title"].textContent = title || "";
  elements["upload-status-message"].textContent = message || "";
  elements["drop-zone"].classList.toggle("upload-success", kind === "success");
  elements["drop-zone"].classList.toggle("upload-error", kind === "error");
}

async function loadJobs(selectNewest = false) {
  const payload = await api("/api/v1/jobs");
  state.jobs = payload.jobs || [];
  if (selectNewest || !state.selectedJob) state.selectedJob = state.jobs[0]?.id || "";
  if (state.selectedJob && !state.jobs.some((item) => item.id === state.selectedJob)) {
    state.selectedJob = state.jobs[0]?.id || "";
  }
  renderJobs();
  if (state.selectedJob) await loadEvents();
}

function renderJobs() {
  const hasJobs = state.jobs.length > 0;
  elements["empty-jobs"].classList.toggle("hidden", hasJobs);
  elements["job-workspace"].classList.toggle("hidden", !hasJobs);
  if (!hasJobs) {
    elements["console-transcript"].classList.add("hidden");
    return;
  }
  const select = elements["job-select"];
  select.replaceChildren();
  for (const job of state.jobs) {
    select.append(new Option(`${job.state.toUpperCase()} · ${shortId(job.id)} · ${formatTime(job.createdUtc)}`, job.id));
  }
  select.value = state.selectedJob;
  const job = selectedJob();
  if (!job) return;
  const artifact = state.artifacts.find((item) => item.id === job.request.artifactId);
  elements["job-state"].textContent = job.state;
  elements["job-state"].className = `job-state ${job.state}`;
  elements["job-title"].textContent = artifact?.manifest?.artifact?.name || "JTAG boot";
  const target = job.request.targetId ? ` · target ${job.request.targetId}` : "";
  elements["job-meta"].textContent = `${job.request.hwServerUrl}${target} · ${formatTime(job.createdUtc)}`;
  elements["cancel-button"].classList.toggle("hidden", ["succeeded", "failed", "canceled"].includes(job.state));
  elements["console-transcript"].classList.toggle("hidden", !job.serialCapture);
  scheduleJobRefresh(job);
}

function scheduleJobRefresh(job) {
  clearTimeout(state.jobTimer);
  if (["queued", "running"].includes(job.state)) {
    state.jobTimer = setTimeout(() => loadJobs(false).catch((error) => showToast(error.message)), 1200);
  }
}

async function loadEvents() {
  const payload = await api(`/api/v1/jobs/${state.selectedJob}/events`);
  const timeline = elements.timeline;
  timeline.replaceChildren();
  for (const event of payload.events || []) timeline.append(renderEvent(event));
  timeline.scrollTop = timeline.scrollHeight;
}

function renderEvent(event) {
  const row = document.createElement("div");
  row.className = `event ${event.level}`;
  const dot = document.createElement("span");
  dot.className = "event-dot";
  const time = document.createElement("time");
  time.textContent = new Date(event.time).toLocaleTimeString([], { hour12: false });
  const message = document.createElement("p");
  message.textContent = event.message;
  row.append(dot, time, message);
  return row;
}

async function createJob(event) {
  event.preventDefault();
  showFormError("");
  const baseRequest = {
    artifactId: state.selectedArtifact,
    hwServerUrl: elements["hw-server-url"].value.trim(),
    tftpServerIp: elements["tftp-server-ip"].value.trim(),
  };
  const targets = selectedTargets();
  if (!targets.length) {
    showFormError("Scan and select at least one JTAG device.");
    return;
  }
  const boardIP = elements["board-ip"].value.trim();
  if (targets.length > 1 && boardIP) {
    showFormError("Leave Board IPv4 empty when booting multiple devices so each board can obtain a unique DHCP address.");
    return;
  }
  if (boardIP) baseRequest.boardIp = boardIP;
  const serialPorts = targets.map((target) => selectedSerialPort(target)).filter(Boolean);
  const baudRate = Number(elements["serial-baud"].value);
  if (serialPorts.length && (!Number.isInteger(baudRate) || baudRate < 300 || baudRate > 4000000)) {
    showFormError("Serial baud must be an integer between 300 and 4000000.");
    return;
  }
  if (new Set(serialPorts.map((port) => port.id)).size !== serialPorts.length) {
    showFormError("Select a different serial port for each JTAG device, or choose JTAG only.");
    return;
  }
  elements["start-button"].disabled = true;
  const created = [];
  try {
    for (const target of targets) {
      const targetRequest = { ...baseRequest, targetId: target.id };
      if (target.cableSerial) targetRequest.targetCableSerial = target.cableSerial;
      if (target.deviceIndex !== undefined && target.deviceIndex !== "") {
        targetRequest.targetDeviceIndex = target.deviceIndex;
      }
      const serialPort = selectedSerialPort(target);
      if (serialPort) {
        targetRequest.serialConsole = { portId: serialPort.id, baudRate };
        const automaticallyMatched = target.serialAssociation === "matched" && target.serialPort?.id === serialPort.id;
        if (!automaticallyMatched) targetRequest.serialConsole.selection = "manual";
      }
      const job = await api("/api/v1/jobs", {
        method: "POST", body: JSON.stringify(targetRequest),
      });
      created.push(job);
    }
    state.selectedJob = created[created.length - 1].id;
    await loadJobs(true);
    showToast(`${created.length} JTAG boot job${created.length === 1 ? "" : "s"} queued`);
  } catch (error) {
    const prefix = created.length ? `${created.length} job${created.length === 1 ? "" : "s"} queued; ` : "";
    showFormError(prefix + error.message);
  } finally {
    updateStartButton();
  }
}

async function cancelJob() {
  const job = selectedJob();
  if (!job) return;
  try {
    await api(`/api/v1/jobs/${job.id}/cancel`, { method: "POST" });
    await loadJobs();
    showToast("Cancellation requested");
  } catch (error) {
    showToast(error.message);
  }
}

function availableConsolePorts() {
  const ports = new Map(state.serialPorts.map((port) => [port.id, port]));
  for (const target of state.targets) {
    if (target.serialPort) ports.set(target.serialPort.id, target.serialPort);
  }
  return Array.from(ports.values());
}

function serialPortLabel(port) {
  const details = [port.name];
  if (port.vendorId && port.productId) details.push(`USB ${port.vendorId}:${port.productId}`);
  if (port.usbSerial) details.push(`serial ${port.usbSerial}`);
  if (port.channel) details.push(`channel ${port.channel}`);
  return details.join(" · ");
}

function renderConsolePorts() {
  const select = elements["console-port"];
  const selected = select.value;
  const ports = availableConsolePorts();
  select.replaceChildren();
  if (!ports.length) {
    select.append(new Option("No serial consoles detected", ""));
  } else {
    for (const port of ports) {
      const target = state.targets.find((item) => item.serialPort?.id === port.id);
      const identity = target?.cableName || target?.cableSerial;
      const mode = port.busy ? ` · active at ${port.activeBaudRate} baud${port.hasController ? " · read-only available" : ""}` : "";
      const pairing = identity ? `${identity} · ` : "";
      select.append(new Option(`${pairing}${serialPortLabel(port)}${mode}`, port.id));
    }
  }
  if (ports.some((port) => port.id === selected)) select.value = selected;
  select.disabled = Boolean(state.consoleSocket) || !ports.length;
  elements["serial-baud"].disabled = Boolean(state.consoleSocket);
  elements["console-connect"].disabled = Boolean(state.consoleSocket) || !select.value;
  elements["console-connect"].classList.toggle("hidden", Boolean(state.consoleSocket));
  elements["console-disconnect"].classList.toggle("hidden", !state.consoleSocket);
  renderConsolePortDetail();
}

function renderConsolePortDetail() {
  const port = availableConsolePorts().find((item) => item.id === elements["console-port"].value);
  if (!port) {
    const warning = state.serialWarnings[0]?.message;
    elements["console-port-detail"].textContent = warning || "Scan JTAG devices to see their paired UART channels.";
    return;
  }
  const target = state.targets.find((item) => item.serialPort?.id === port.id);
  const manualTarget = state.targets.find((item) =>
    state.targetSerialPortIds.get(item.id) === port.id && item.serialPort?.id !== port.id,
  );
  const pairing = target ? `Paired with JTAG cable ${target.cableSerial || target.cableName}. ` :
    manualTarget ? `Selected manually for JTAG cable ${manualTarget.cableSerial || manualTarget.cableName}. ` : "";
  const active = port.busy ? ` Active at ${port.activeBaudRate} baud.` : "";
  const lease = port.hasController ? " Another client holds the write lease." : " A write lease is available.";
  const identity = [];
  if (port.vendorId && port.productId) identity.push(`USB ${port.vendorId}:${port.productId}`);
  if (port.usbSerial) identity.push(`serial ${port.usbSerial}`);
  if (port.channel) identity.push(`channel ${port.channel}`);
  const description = identity.length ? `${identity.join(", ")}.` : "Manually selectable operating-system serial port.";
  elements["console-port-detail"].textContent = `${pairing}${description}${active}${lease}`;
}

function ensureTerminal() {
  if (state.terminal) return;
  if (!globalThis.Terminal || !globalThis.FitAddon?.FitAddon) {
    throw new Error("The embedded terminal assets could not be loaded.");
  }
  state.terminal = new globalThis.Terminal({
    allowProposedApi: false,
    convertEol: false,
    cursorBlink: true,
    cursorStyle: "block",
    fontFamily: "ui-monospace, SFMono-Regular, Consolas, monospace",
    fontSize: 13,
    linkHandler: {
      activate: () => showToast("Links emitted by the target are disabled"),
      allowNonHttpProtocols: false,
    },
    scrollback: 10000,
    theme: {
      background: "#0c1210", foreground: "#dce9e1", cursor: "#56c79d", selectionBackground: "#315345",
    },
  });
  state.fitAddon = new globalThis.FitAddon.FitAddon();
  state.terminal.loadAddon(state.fitAddon);
  state.terminal.open(elements["serial-terminal"]);
  state.fitAddon.fit();
  state.terminal.writeln("\x1b[1;32mMonutchee serial console\x1b[0m");
  state.terminal.writeln("Select a serial port and connect.\r\n");
  state.terminalInput = state.terminal.onData((data) => {
    if (!state.consoleReady || state.consoleAccess !== "controller" || state.consoleSocket?.readyState !== WebSocket.OPEN) return;
    if (state.consoleSocket.bufferedAmount > (1 << 20)) {
      showToast("Serial input paused while the browser catches up");
      return;
    }
    state.consoleSocket.send(new TextEncoder().encode(data));
  });
  const resize = () => {
    try { state.fitAddon.fit(); } catch (_) { /* terminal may be between layouts */ }
  };
  if (globalThis.ResizeObserver) {
    new ResizeObserver(resize).observe(elements["serial-terminal"]);
  } else {
    window.addEventListener("resize", resize);
  }
}

function setConsoleStatus(label, mode = "") {
  elements["console-status"].textContent = label;
  elements["console-status"].className = `badge console-status${mode ? ` ${mode}` : ""}`;
}

async function requestSerialSession(portId, baudRate, access) {
  return api("/api/v1/serial/sessions", {
    method: "POST",
    body: JSON.stringify({ portId, baudRate, access }),
  });
}

async function connectConsole() {
  const portId = elements["console-port"].value;
  const baudRate = Number(elements["serial-baud"].value);
  if (!portId) return;
  if (!Number.isInteger(baudRate) || baudRate < 300 || baudRate > 4000000) {
    showToast("Serial baud must be an integer between 300 and 4000000");
    return;
  }
  try {
    ensureTerminal();
  } catch (error) {
    showToast(error.message);
    return;
  }
  elements["console-connect"].disabled = true;
  setConsoleStatus("Connecting");
  let access = "controller";
  let session;
  try {
    try {
      session = await requestSerialSession(portId, baudRate, access);
    } catch (error) {
      if (access !== "controller" || error.status !== 409) throw error;
      access = "observer";
      await loadSerialPorts();
      const activePort = availableConsolePorts().find((port) => port.id === portId);
      const observerBaud = activePort?.activeBaudRate || baudRate;
      elements["serial-baud"].value = observerBaud;
      session = await requestSerialSession(portId, observerBaud, access);
    }
    const websocketURL = new URL(session.websocketPath, window.location.href);
    websocketURL.protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(websocketURL);
    socket.binaryType = "arraybuffer";
    const generation = ++state.consoleGeneration;
    state.consoleSocket = socket;
    state.consoleSessionId = session.id;
    state.consoleAccess = access;
    state.consoleReady = false;
    renderConsolePorts();
    socket.addEventListener("open", () => {
      socket.send(JSON.stringify({ type: "attach", token: session.attachToken }));
    });
    socket.addEventListener("message", async (event) => {
      if (generation !== state.consoleGeneration) return;
      if (typeof event.data === "string") {
        let message;
        try { message = JSON.parse(event.data); } catch (_) { return; }
        if (message.type === "ready") {
          state.consoleReady = true;
          state.consoleAccess = message.access;
          const readOnly = message.access !== "controller";
          setConsoleStatus(readOnly ? "Connected · read-only" : "Connected · controller", readOnly ? "observer" : "connected");
          state.terminal.writeln(`\x1b[2mConnected to ${session.port.name} at ${message.baudRate} baud (${message.access}).\x1b[0m\r`);
        }
        return;
      }
      const bytes = event.data instanceof ArrayBuffer ? new Uint8Array(event.data) : new Uint8Array(await event.data.arrayBuffer());
      state.terminal.write(bytes);
    });
    socket.addEventListener("error", () => {
      if (generation === state.consoleGeneration) showToast("Serial WebSocket connection failed");
    });
    socket.addEventListener("close", (event) => {
      if (generation !== state.consoleGeneration) return;
      api(`/api/v1/serial/sessions/${session.id}`, { method: "DELETE" }).catch(() => {});
      state.consoleSocket = null;
      state.consoleSessionId = "";
      state.consoleReady = false;
      state.consoleAccess = "";
      setConsoleStatus("Disconnected");
      renderConsolePorts();
      if (event.code !== 1000) state.terminal.writeln(`\r\n\x1b[1;31mSerial connection closed (${event.reason || event.code}).\x1b[0m`);
    });
  } catch (error) {
    if (session?.id) {
      api(`/api/v1/serial/sessions/${session.id}`, { method: "DELETE" }).catch(() => {});
    }
    state.consoleSocket = null;
    state.consoleSessionId = "";
    state.consoleReady = false;
    state.consoleAccess = "";
    setConsoleStatus("Disconnected");
    renderConsolePorts();
    showToast(error.message);
  }
}

function disconnectConsole() {
  const socket = state.consoleSocket;
  const sessionId = state.consoleSessionId;
  state.consoleGeneration += 1;
  state.consoleSocket = null;
  state.consoleSessionId = "";
  state.consoleReady = false;
  state.consoleAccess = "";
  if (socket) socket.close(1000, "operator disconnected");
  else if (sessionId) api(`/api/v1/serial/sessions/${sessionId}`, { method: "DELETE" }).catch(() => {});
  setConsoleStatus("Disconnected");
  renderConsolePorts();
}

function selectConsoleForJob() {
  const portId = selectedJob()?.request?.serialConsole?.portId;
  if (!portId || state.consoleSocket) return;
  if (availableConsolePorts().some((port) => port.id === portId)) {
    elements["console-port"].value = portId;
    renderConsolePortDetail();
  }
}

async function loadJobTranscript() {
  const job = selectedJob();
  if (!job?.serialCapture) return;
  try {
    ensureTerminal();
    disconnectConsole();
    setConsoleStatus("Loading transcript");
    const data = await apiBytes(`/api/v1/jobs/${job.id}/serial-transcript`);
    state.terminal.reset();
    state.terminal.write(data);
    const suffix = job.serialCapture.truncated ? " · truncated" : "";
    setConsoleStatus(`Transcript · ${formatBytes(data.length)}${suffix}`, job.serialCapture.truncated ? "observer" : "connected");
  } catch (error) {
    setConsoleStatus("Disconnected");
    showToast(error.message);
  }
}

function selectedJob() { return state.jobs.find((item) => item.id === state.selectedJob); }

function showFormError(message) {
  elements["form-error"].textContent = message;
  elements["form-error"].classList.toggle("hidden", !message);
}

function showToast(message) {
  clearTimeout(state.toastTimer);
  elements.toast.textContent = message;
  elements.toast.classList.remove("hidden");
  state.toastTimer = setTimeout(() => elements.toast.classList.add("hidden"), 4200);
}

function formatBytes(bytes) {
  if (!Number.isFinite(bytes)) return "—";
  const units = ["B", "KiB", "MiB", "GiB"];
  let value = bytes;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) { value /= 1024; index += 1; }
  return `${value.toFixed(index ? 1 : 0)} ${units[index]}`;
}

function formatTime(value) { return new Date(value).toLocaleString(); }
function shortId(value) { return value.slice(0, 8); }

elements["token-form"].addEventListener("submit", (event) => {
  event.preventDefault();
  state.token = elements["api-token"].value;
  sessionStorage.setItem("mnc-station-token", state.token);
  loadCapabilities();
});
elements["drop-zone"].addEventListener("click", () => elements["artifact-file"].click());
elements["drop-zone"].addEventListener("keydown", (event) => {
  if (event.key === "Enter" || event.key === " ") { event.preventDefault(); elements["artifact-file"].click(); }
});
elements["artifact-file"].addEventListener("change", () => uploadArtifact(elements["artifact-file"].files[0]));
for (const name of ["dragenter", "dragover"]) {
  elements["drop-zone"].addEventListener(name, (event) => { event.preventDefault(); elements["drop-zone"].classList.add("dragging"); });
}
for (const name of ["dragleave", "drop"]) {
  elements["drop-zone"].addEventListener(name, (event) => { event.preventDefault(); elements["drop-zone"].classList.remove("dragging"); });
}
elements["drop-zone"].addEventListener("drop", (event) => uploadArtifact(event.dataTransfer.files[0]));
elements["artifact-select"].addEventListener("change", (event) => { state.selectedArtifact = event.target.value; renderArtifacts(); });
elements["boot-form"].addEventListener("submit", createJob);
elements["discover-targets"].addEventListener("click", discoverTargets);
elements["hw-server-url"].addEventListener("input", () => {
  state.targets = [];
  state.targetSerialPortIds.clear();
  renderTargets();
  updateStartButton();
});
elements["serial-baud"].addEventListener("input", updateStartButton);
elements["refresh-jobs"].addEventListener("click", () => loadJobs().catch((error) => showToast(error.message)));
elements["job-select"].addEventListener("change", (event) => {
  state.selectedJob = event.target.value;
  renderJobs();
  selectConsoleForJob();
  loadEvents();
});
elements["cancel-button"].addEventListener("click", cancelJob);
elements["console-port"].addEventListener("change", renderConsolePortDetail);
elements["console-connect"].addEventListener("click", connectConsole);
elements["console-disconnect"].addEventListener("click", disconnectConsole);
elements["console-transcript"].addEventListener("click", loadJobTranscript);
window.addEventListener("beforeunload", () => state.consoleSocket?.close(1000, "page closed"));

try { ensureTerminal(); } catch (error) { showToast(error.message); }
loadCapabilities();

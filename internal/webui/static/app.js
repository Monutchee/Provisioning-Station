// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

"use strict";

const state = {
  token: sessionStorage.getItem("mnc-station-token") || "",
  capabilities: null,
  artifacts: [],
  targets: [],
  targetLoading: false,
  jobs: [],
  selectedArtifact: "",
  selectedJob: "",
  eventSource: null,
  jobTimer: null,
  toastTimer: null,
};

const elements = Object.fromEntries([
  "agent-state", "agent-version", "xsdb-state", "tftp-listen", "auth-notice",
  "token-form", "api-token", "drop-zone", "artifact-file", "upload-progress",
  "artifact-select", "artifact-count", "artifact-card", "artifact-vendor",
  "artifact-name", "artifact-details", "artifact-sha", "artifact-built",
  "boot-form", "hw-server-url", "discover-targets", "target-list",
  "tftp-server-ip", "tftp-server-ip-options", "board-ip", "form-error", "start-button", "refresh-jobs",
  "empty-jobs", "job-workspace", "job-select", "job-state", "job-title",
  "job-meta", "cancel-button", "timeline", "toast",
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

function authQuery() {
  return state.token ? `?access_token=${encodeURIComponent(state.token)}` : "";
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
    populateStationAddresses();
    elements["auth-notice"].classList.add("hidden");
    setConnected(true, "Agent online");
    await Promise.all([loadArtifacts(), loadJobs()]);
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
  renderTargets();
  elements["discover-targets"].disabled = true;
  elements["discover-targets"].textContent = "Scanning…";
  updateStartButton();
  try {
    const query = new URLSearchParams({ hwServerUrl: hardwareServerURL });
    const payload = await api(`/api/v1/xilinx/targets?${query}`);
    state.targets = payload.targets || [];
    if (!state.targets.length) showFormError("No ZynqMP PSU targets were found on this hardware server.");
  } catch (error) {
    showFormError(error.message);
  } finally {
    state.targetLoading = false;
    renderTargets();
    elements["discover-targets"].disabled = false;
    elements["discover-targets"].textContent = "Scan devices";
    updateStartButton();
  }
}

function renderTargets() {
  const list = elements["target-list"];
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
  state.targets.forEach((target, index) => {
    const option = document.createElement("label");
    option.className = "target-option";
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.value = target.id;
    checkbox.checked = index === 0;
    checkbox.addEventListener("change", updateStartButton);
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
    description.append(name, details);
    option.append(checkbox, description);
    list.append(option);
  });
}

function selectedTargetIDs() {
  return Array.from(elements["target-list"].querySelectorAll('input[type="checkbox"]:checked'), (input) => input.value);
}

function updateStartButton() {
  const artifactSelected = state.artifacts.some((item) => item.id === state.selectedArtifact);
  elements["start-button"].disabled = !artifactSelected || !state.capabilities?.xsdb?.available ||
    state.targetLoading || selectedTargetIDs().length === 0;
}

async function uploadArtifact(file) {
  if (!file) return;
  elements["upload-progress"].classList.remove("hidden");
  const form = new FormData();
  form.append("artifact", file, file.name);
  try {
    const imported = await api("/api/v1/artifacts", { method: "POST", body: form });
    state.selectedArtifact = imported.id;
    await loadArtifacts();
    showToast(`${imported.manifest.artifact.name} verified and imported`);
  } catch (error) {
    showFormError(error.message);
  } finally {
    elements["upload-progress"].classList.add("hidden");
    elements["artifact-file"].value = "";
  }
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
  if (!hasJobs) return;
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
  const targetIDs = selectedTargetIDs();
  if (!targetIDs.length) {
    showFormError("Scan and select at least one JTAG device.");
    return;
  }
  const boardIP = elements["board-ip"].value.trim();
  if (targetIDs.length > 1 && boardIP) {
    showFormError("Leave Board IPv4 empty when booting multiple devices so each board can obtain a unique DHCP address.");
    return;
  }
  if (boardIP) baseRequest.boardIp = boardIP;
  elements["start-button"].disabled = true;
  const created = [];
  try {
    for (const targetID of targetIDs) {
      const job = await api("/api/v1/jobs", {
        method: "POST", body: JSON.stringify({ ...baseRequest, targetId: targetID }),
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
  renderTargets();
  updateStartButton();
});
elements["refresh-jobs"].addEventListener("click", () => loadJobs().catch((error) => showToast(error.message)));
elements["job-select"].addEventListener("change", (event) => { state.selectedJob = event.target.value; renderJobs(); loadEvents(); });
elements["cancel-button"].addEventListener("click", cancelJob);

loadCapabilities();

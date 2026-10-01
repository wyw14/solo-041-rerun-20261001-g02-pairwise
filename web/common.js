const output = document.querySelector("#output");

async function request(path, options = {}) {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    ...options,
  });
  const data = await response.json().catch(() => ({ error: response.statusText }));
  if (!response.ok) {
    const error = new Error(data.error || response.statusText);
    error.status = response.status;
    error.data = data;
    throw error;
  }
  return data;
}

function show(value) {
  if (output) output.textContent = JSON.stringify(value, null, 2);
}

function showError(error) {
  show({ error: error.message });
}

async function refreshHealth() {
  const badge = document.querySelector("#health");
  if (!badge) return;
  try {
    const health = await request("/healthz");
    badge.textContent = `${health.process_lines} lines online`;
  } catch (error) {
    badge.textContent = "service unavailable";
    badge.previousElementSibling.style.background = "#b5423a";
  }
}

refreshHealth();

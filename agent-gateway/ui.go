package main

const uiHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>kronaxis agent-gateway</title>
<style>
  :root { color-scheme: dark light; }
  body { font: 14px/1.4 ui-monospace, Menlo, Consolas, monospace; margin: 24px; max-width: 1100px; }
  h1 { font-size: 18px; margin: 0 0 8px; }
  .sub { color: #888; margin-bottom: 24px; }
  .grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-bottom: 24px; }
  .card { padding: 12px; border: 1px solid #333; border-radius: 6px; }
  .num { font-size: 22px; font-weight: 600; }
  .lbl { color: #888; font-size: 12px; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid #2a2a2a; font-size: 13px; }
  th { color: #888; font-weight: 500; }
  td.id { font-size: 11px; color: #888; }
  td.ok { color: #6c6; }
  td.error { color: #f66; }
  td.stub { color: #fa0; }
  .pill { display: inline-block; padding: 1px 6px; border-radius: 10px; background: #333; font-size: 11px; }
  .footer { color: #666; margin-top: 24px; font-size: 12px; }
  .footer code { background: #2a2a2a; padding: 1px 4px; border-radius: 3px; }
</style>
</head>
<body>
<h1>kronaxis agent-gateway</h1>
<div class="sub" id="sub">Live request feed. /metrics for Prometheus, /v1/models for adapters.</div>

<div class="grid">
  <div class="card"><div class="num" id="reqTotal">0</div><div class="lbl">Total requests</div></div>
  <div class="card"><div class="num" id="reqActive">0</div><div class="lbl">Active</div></div>
  <div class="card"><div class="num" id="costTotal">$0.00</div><div class="lbl">Total cost</div></div>
  <div class="card"><div class="num" id="errorTotal">0</div><div class="lbl">Errors</div></div>
</div>

<table>
  <thead>
    <tr><th>id</th><th>adapter</th><th>model</th><th>status</th><th>turns</th><th>cost</th><th>duration</th><th>error</th></tr>
  </thead>
  <tbody id="rows"></tbody>
</table>

<div class="footer">
  Endpoints:
  <code>POST /v1/chat/completions</code>
  <code>GET /v1/models</code>
  <code>POST /v1/workspaces</code>
  <code>GET /healthz</code>
  <code>GET /metrics</code>
</div>

<script>
const rows = document.getElementById('rows');
let total = 0, errors = 0, cost = 0;

const ev = new EventSource('/api/live');
ev.onmessage = (e) => {
  if (!e.data) return;
  let r;
  try { r = JSON.parse(e.data); } catch { return; }
  total++;
  if (r.status === 'error') errors++;
  if (r.cost_usd) cost += r.cost_usd;
  document.getElementById('reqTotal').textContent = total;
  document.getElementById('errorTotal').textContent = errors;
  document.getElementById('costTotal').textContent = '$' + cost.toFixed(4);

  const tr = document.createElement('tr');
  const cls = r.status || 'ok';
  tr.innerHTML =
    '<td class="id">' + (r.id||'') + '</td>' +
    '<td>' + (r.adapter||'') + '</td>' +
    '<td>' + (r.model||'') + '</td>' +
    '<td class="' + cls + '">' + cls + '</td>' +
    '<td>' + (r.num_turns||0) + '</td>' +
    '<td>$' + ((r.cost_usd||0).toFixed(4)) + '</td>' +
    '<td>' + (r.duration_ms||0) + 'ms</td>' +
    '<td>' + (r.error||'') + '</td>';
  rows.insertBefore(tr, rows.firstChild);
  while (rows.rows.length > 50) rows.deleteRow(rows.rows.length - 1);
};
</script>
</body>
</html>`

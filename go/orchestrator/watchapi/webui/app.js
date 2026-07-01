// Thin consumer of the §8 API — zero business logic, just fetch + DOM.
// Token (if the surface is guarded) comes from ?token= for local convenience.
const tok = new URLSearchParams(location.search).get("token") || "";
const H = tok ? { Authorization: "Bearer " + tok } : {};
const el = (id) => document.getElementById(id);
const j = (p) => fetch(p, { headers: H }).then((r) => r.json());
const post = (p, b) =>
  fetch(p, {
    method: "POST",
    headers: { ...H, "Content-Type": "application/json" },
    body: b ? JSON.stringify(b) : null,
  });

async function refresh() {
  const tickets = await j("/api/tickets?status=needs_human");
  el("tickets").innerHTML =
    tickets
      .map((t) => {
        // orchestrator.Ticket has no json tags → PascalCase on the wire; be tolerant.
        const id = t.id ?? t.ID,
          title = t.title ?? t.Title;
        return `<div class="row">${title} <span class="muted">[${id}]</span>
           <button onclick="approve('${id}')">Approve</button>
           <button onclick="reject('${id}')">Reject</button></div>`;
      })
      .join("") || '<span class="muted">none</span>';

  const revs = await j("/api/board/revisions");
  el("revisions").innerHTML = revs
    .map((r) => `<div class="row">${r.seq}. <b>${r.author}</b>: ${r.message}</div>`)
    .join("");

  const board = await j("/api/board/current");
  el("fragments").innerHTML = (board.fragments || [])
    .map((f) => `<div class="row"><b>${f.id}</b>: ${f.body} <span class="muted">(@${f.last_changed_in})</span></div>`)
    .join("");

  const runs = await j("/api/runs");
  el("runs").innerHTML = runs
    .map((r) => `<div class="row">${r.seq}. ${r.scope} @${r.board_rev}: ${r.output}</div>`)
    .join("");
}

async function approve(id) {
  await post(`/api/tickets/${id}/approve`);
  refresh();
}
async function reject(id) {
  const note = prompt("Note (optional):") || "";
  await post(`/api/tickets/${id}/reject`, { note });
  refresh();
}
async function feedback() {
  await post("/api/feedback", { target_ref: el("fb-ref").value, note: el("fb-note").value });
  refresh();
}
async function trigger() {
  await post("/api/trigger");
  el("status").textContent = "triggered";
  refresh();
}

refresh();

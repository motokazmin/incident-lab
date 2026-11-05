export interface Event { at: string; kind: string; text: string }
export interface Item { name: string; pass: boolean; detail: string; hint?: string }
export interface Hint { title: string; text: string }
export interface Report { passed: boolean; items: Item[]; finished: string }
export interface LabView {
  id: string; scenario: string; title: string; briefing: string; debrief?: string;
  state: "booting" | "healthy" | "alert" | "verifying" | "resolved" | "ended";
  seed: string; created: string; events: Event[]; report?: Report;
  hints: Hint[]; hintsTotal: number;
}
export interface Sample { t: number; sent: number; failed: number; p50ms: number; p95ms: number }

async function call(path: string, init?: RequestInit): Promise<Response> {
  const r = await fetch(path, init);
  if (!r.ok) throw new Error((await r.text()).trim() || `Ошибка ${r.status}`);
  return r;
}

export interface ScenarioInfo { id: string; title: string; summary: string; stack: string }

export const scenarios = async (): Promise<ScenarioInfo[]> => (await call("/api/scenarios")).json();

export const startLab = async (scenario: string): Promise<LabView> =>
  (await call("/api/labs", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ scenario }),
  })).json();

export const getLab = async (id: string): Promise<LabView> => (await call(`/api/labs/${id}`)).json();
export const stopLab = async (id: string) => { await call(`/api/labs/${id}`, { method: "DELETE" }); };
export const verify = async (id: string) => { await call(`/api/labs/${id}/verify`, { method: "POST" }); };
export const openHint = async (id: string, n: number): Promise<LabView> =>
  (await call(`/api/labs/${id}/hints/${n}`, { method: "POST" })).json();
export const metrics = async (id: string, since: number): Promise<Sample[]> =>
  (await call(`/api/labs/${id}/metrics?since=${since}`)).json();
export const files = async (id: string): Promise<string[]> => (await call(`/api/labs/${id}/files`)).json();
const q = (p: string) => encodeURIComponent(p);
export const readFile = async (id: string, p: string) => (await call(`/api/labs/${id}/file?path=${q(p)}`)).text();
export const writeFile = async (id: string, p: string, body: string) => {
  await call(`/api/labs/${id}/file?path=${q(p)}`, { method: "PUT", body });
};

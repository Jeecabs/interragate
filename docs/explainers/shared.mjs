document.documentElement.dataset.theme = new URLSearchParams(location.search).get('theme') === 'dark' ? 'dark' : 'light';

export const escape = s => String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export const rich = s => escape(s).replace(/`([^`]+)`/g, '<code>$1</code>');
export const plain = s => escape(s.replace(/`/g, ''));
export const mark = `<svg class="mark" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10.35" fill="none" stroke="currentColor" stroke-opacity=".22" stroke-width="1.3"/><circle cx="12" cy="12" r="6.6" fill="none" stroke="currentColor" stroke-opacity=".5" stroke-width="1.3"/><circle cx="12" cy="12" r="2.75" fill="currentColor"/></svg>`;
export const brand = `<div class="brand">${mark}<span>interragate</span></div>`;
export function severity(level, label = level[0].toUpperCase() + level.slice(1)) {
  const shape = level === 'critical' ? `<path d="M5.2 1h5.6L15 5.2v5.6L10.8 15H5.2L1 10.8V5.2z" fill="var(--sev-critical)"/><path d="M8 4.4v4.4" stroke="#fff" stroke-width="1.8" stroke-linecap="round"/><circle cx="8" cy="11.4" r="1.05" fill="#fff"/>` : level === 'high' ? `<path d="M8 1.3 15.2 14H.8z" fill="var(--sev-high)"/><path d="M8 6v3.6" stroke="#0b0b0c" stroke-width="1.7" stroke-linecap="round"/><circle cx="8" cy="11.6" r=".95" fill="#0b0b0c"/>` : level === 'medium' ? `<path d="M8 1.2 14.8 8 8 14.8 1.2 8z" fill="var(--sev-medium)"/>` : `<circle cx="8" cy="8" r="4.6" fill="var(--sev-low)"/>`;
  return `<span class="sev"><svg viewBox="0 0 16 16" aria-hidden="true">${shape}</svg><span>${escape(label)}</span></span>`;
}
export function square(action, cosmetic = false, address = '') {
  const shape = action === 'replace' ? '<path d="M1 16 16 1v15z" fill="currentColor"/><path d="M1 1h15v15H1z" fill="none" stroke="currentColor" stroke-width="1.5"/>' : action === 'delete' ? '<path d="M1 1h15v15H1zM1 1l15 15M16 1 1 16" fill="none" stroke="currentColor" stroke-width="1.5"/>' : cosmetic ? '<path d="M1 1h15v15H1z" fill="none" stroke="currentColor" stroke-width="1.5"/>' : '<path d="M0 0h17v17H0z" fill="currentColor"/>';
  return `<svg class="unit ${action}${cosmetic ? ' cosmetic' : ''}" viewBox="0 0 17 17" role="img" aria-label="${escape(address ? `${address}: ${action}${cosmetic ? ', cosmetic' : ''}` : action)}">${shape}</svg>`;
}
export const units = changes => `<div class="units">${changes.map(c => square(c.action, !!c.cosmetic, c.address)).join('')}</div>`;
export const legend = () => `<div class="legend"><span>${square('delete')}destroy</span><span>${square('replace')}replace</span><span>${square('update')}update</span><span>${square('update', true)}tags or formatting</span><span>${square('create')}create</span></div>`;
export async function load() {
  const response = await fetch('./sample-report.json');
  if (!response.ok) throw new Error(`Sample report: HTTP ${response.status}`);
  const report = await response.json();
  if (report.schema !== 1) throw new Error(`Unsupported report schema: ${report.schema}`);
  return report;
}
export async function finish(html) {
  document.querySelector('main').innerHTML = html;
  await document.fonts.ready;
  await Promise.all([document.fonts.load('400 16px Geist'), document.fonts.load('400 13px "Geist Mono"'), document.fonts.load('400 25px "Geist Pixel"')]);
  document.documentElement.dataset.ready = 'true';
}
export const change = (r, address) => {
  const c = r.changes.find(c => c.address === address);
  if (!c) throw new Error(`Missing sample change: ${address}`);
  return c;
};
export const narrative = (r, starts) => {
  const n = r.narrative.find(n => n.text.startsWith(starts));
  if (!n) throw new Error(`Missing sample narrative: ${starts}`);
  return n;
};

export function applyModel(r) {
  const a=r.apply, steps=a.steps, failure=steps.find(s=>s.fails);
  if(!failure) throw new Error('Sample no longer has an expected apply failure');
  const nodes=new Map([...steps,...(a.joins||[])].map(s=>[s.id,s]));
  function waits(id, target, seen=new Set()) {
    if(id===target) return true;
    if(seen.has(id)) return false;
    seen.add(id);
    return (nodes.get(id)?.after||[]).some(dep=>waits(dep,target,seen));
  }
  const skipped=steps.filter(s=>s.id!==failure.id&&waits(s.id,failure.id));
  const skippedIDs=new Set(skipped.map(s=>s.id));
  const stranded=r.changes.filter(c=>c.action==='replace'&&steps.some(s=>s.address===c.address&&s.op==='destroy'&&!s.fails&&!skippedIDs.has(s.id))&&steps.some(s=>s.address===c.address&&s.op==='create'&&skippedIDs.has(s.id)));
  const skippedResources=new Set(skipped.filter(s=>s.address!==failure.address).map(s=>s.address)).size;
  if(!a.narrative[0].text.includes(`The ${skippedResources} resources`)) throw new Error('Skipped resource count differs from the narrative');
  return {failure,skippedIDs,stranded,skippedResources};
}

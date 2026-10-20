// Regenerate README figures, site figures, report captures and the social card:
//   node docs/explainers/render.mjs
// Requires Go, Node 24+ and /usr/bin/chromium; no npm dependencies.
// Only headless Chromium is launched. Its temporary profile is removed
// afterwards. Sources use ?theme=dark (light is the default).
import { createServer } from 'node:http';
import { spawn, execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { readFile, writeFile, mkdir, mkdtemp, rm } from 'node:fs/promises';
import { resolve, dirname, extname, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { inflateSync } from 'node:zlib';
import { captureSite } from './capture.mjs';

const run = promisify(execFile);
const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '../..');
const output = resolve(root, 'docs/images');
const names = ['hero', 'meaning', 'branch', 'apply', 'pipeline'];
// The CLI the figures show. Its output is captured as it is, never edited here.
const CLI_PATH = './cmd/interragate';
const report = await run('go', ['run', CLI_PATH, 'demo', 'aws', '-f', 'json'], {cwd:root,maxBuffer:8e6});
const summary = await run('go', ['run', CLI_PATH, 'demo', 'aws', '--no-open', '--no-color'], {cwd:root,maxBuffer:2e6});
await writeFile(resolve(here, 'sample-report.json'), report.stdout);
await writeFile(resolve(here, 'sample-terminal.txt'), summary.stdout);
await mkdir(output, {recursive:true});
await run('go', ['run', 'site/build.go', '-o', '_site'], {cwd:root,maxBuffer:8e6});

const types = {'.html':'text/html','.js':'text/javascript','.mjs':'text/javascript','.css':'text/css','.json':'application/json','.woff2':'font/woff2','.png':'image/png','.webp':'image/webp','.svg':'image/svg+xml','.md':'text/plain'};
const server = createServer(async (req,res) => {
  try {
    const pathname = decodeURIComponent(new URL(req.url, 'http://127.0.0.1').pathname);
    const path = resolve(root, '.' + pathname + (pathname.endsWith('/')?'index.html':''));
    if (!path.startsWith(root + sep)) {res.writeHead(403).end();return;}
    const bytes=await readFile(path);
    res.writeHead(200, {'Content-Type':types[extname(path)] || 'application/octet-stream','Cache-Control':'no-store'}).end(bytes);
  } catch {res.writeHead(404).end();}
});
await new Promise(done=>server.listen(0,'127.0.0.1',done));
const port=server.address().port;
// Keep the prefix short: Chromium's SingletonSocket must fit sockaddr_un.
const profile=await mkdtemp('/tmp/ig-render-');
const chromium=spawn('/usr/bin/chromium',[
  '--headless=new','--remote-debugging-port=0',`--user-data-dir=${profile}`,
  '--no-first-run','--no-default-browser-check','--disable-background-networking',
  '--disable-component-update','--disable-sync','--disable-extensions',
  '--disable-background-timer-throttling','--disable-renderer-backgrounding',
  '--disable-backgrounding-occluded-windows',
  '--disable-breakpad','--disable-crash-reporter','--disable-dev-shm-usage',
  // The CDP background override below is the transparent equivalent of
  // --default-background-color=00000000. That CLI switch makes Chromium 152
  // enter its one-shot capture path and close targets during CDP navigation.
  '--hide-scrollbars','about:blank'
],{stdio:['ignore','ignore','pipe'],env:{...process.env,TMPDIR:profile,XDG_CONFIG_HOME:profile,XDG_CACHE_HOME:profile}});

let socket;
const pending=new Map();
let id=0;
function send(method,params={},sessionId) {
  if(process.env.INTERRAGATE_RENDER_DEBUG)console.error('CDP',method,method==='Runtime.evaluate'?params.expression.slice(0,90):'');
  const message={id:++id,method,params};
  if(sessionId) message.sessionId=sessionId;
  return new Promise((accept,reject)=>{
    const timer=setTimeout(()=>{pending.delete(message.id);reject(new Error(`CDP timeout: ${method}`));},30000);
    pending.set(message.id,{accept,reject,timer});
    socket.send(JSON.stringify(message));
  });
}
const evidence=[];
const pageErrors=[];
function verifyPixels(png, theme) {
  const chunks=[];let width,height;
  for(let offset=8;offset<png.length;){
    const length=png.readUInt32BE(offset),kind=png.toString('ascii',offset+4,offset+8),data=png.subarray(offset+8,offset+8+length);
    if(kind==='IHDR'){
      width=data.readUInt32BE(0);height=data.readUInt32BE(4);
      if(data[8]!==8||data[9]!==6)throw new Error('Expected an 8-bit RGBA screenshot');
    }
    if(kind==='IDAT')chunks.push(data);
    offset+=length+12;
  }
  const packed=inflateSync(Buffer.concat(chunks)),stride=width*4;
  let previous=Buffer.alloc(stride),offset=0;
  const samples=new Map();
  for(let y=0;y<height;y++){
    const filter=packed[offset++],row=Buffer.from(packed.subarray(offset,offset+stride));offset+=stride;
    for(let x=0;x<stride;x++){
      const left=x>=4?row[x-4]:0,above=previous[x],corner=x>=4?previous[x-4]:0;
      let prediction=0;
      if(filter===1)prediction=left;
      else if(filter===2)prediction=above;
      else if(filter===3)prediction=Math.floor((left+above)/2);
      else if(filter===4){const p=left+above-corner,a=Math.abs(p-left),b=Math.abs(p-above),c=Math.abs(p-corner);prediction=a<=b&&a<=c?left:b<=c?above:corner;}
      else if(filter!==0)throw new Error(`Unknown PNG filter ${filter}`);
      row[x]=(row[x]+prediction)&255;
    }
    if(y===0||y===height-1)for(const x of [0,width-1])samples.set(`${x},${y}`,[...row.subarray(x*4,x*4+4)]);
    if(y===10)samples.set('page',[...row.subarray(width/2*4,width/2*4+4)]);
    previous=row;
  }
  const expected=theme==='dark'?[15,15,14,255]:[247,246,243,255];
  if([...samples].some(([key,value])=>key==='page'?value.some((v,i)=>v!==expected[i]):value[3]!==0))throw new Error(`Incorrect background: ${JSON.stringify([...samples])}`);
  return {transparentCorners:true,pageColour:expected};
}
try {
  const endpoint=await new Promise((accept,reject)=>{
    let logs='';
    const timer=setTimeout(()=>reject(new Error(`Chromium did not start: ${logs.slice(-1800)}`)),15000);
    chromium.stderr.on('data',chunk=>{
      if(process.env.INTERRAGATE_RENDER_DEBUG)process.stderr.write(chunk);
      logs+=chunk;
      const match=logs.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if(match){clearTimeout(timer);accept(match[1]);}
    });
    chromium.on('error',reject);
    chromium.on('exit',code=>{clearTimeout(timer);reject(new Error(`Chromium exited ${code}: ${logs.slice(-1800)}`));});
  });
  socket=new WebSocket(endpoint);
  await new Promise((accept,reject)=>{socket.addEventListener('open',accept,{once:true});socket.addEventListener('error',reject,{once:true});});
  socket.addEventListener('message',event=>{
    const message=JSON.parse(event.data), waiter=pending.get(message.id);
    if(message.method==='Runtime.exceptionThrown')pageErrors.push(message.params.exceptionDetails);
    if(message.method==='Log.entryAdded'&&message.params.entry.level==='error')pageErrors.push(message.params.entry);
    if(message.method==='Runtime.consoleAPICalled'&&message.params.type==='error')pageErrors.push(message.params.args);
    if(!waiter)return;
    pending.delete(message.id);clearTimeout(waiter.timer);
    if(message.error)waiter.reject(new Error(JSON.stringify(message.error)));
    else waiter.accept(message.result);
  });
  for(const name of names) for(const theme of ['light','dark']) {
    const {targetId}=await send('Target.createTarget',{url:'about:blank'});
    const {sessionId}=await send('Target.attachToTarget',{targetId,flatten:true});
    const call=(method,params)=>send(method,params,sessionId);
    await call('Page.enable');
    await call('Runtime.enable');
    await call('DOM.enable');
    await call('CSS.enable');
    await call('Emulation.setDeviceMetricsOverride',{width:800,height:1100,deviceScaleFactor:2,mobile:false});
    await call('Emulation.setDefaultBackgroundColorOverride',{color:{r:0,g:0,b:0,a:0}});
    const url=`http://127.0.0.1:${port}/docs/explainers/${name}.html?theme=${theme}`;
    await call('Page.navigate',{url});
    let loaded=false;
    for(let attempt=0;attempt<100;attempt++){
      const ready=await call('Runtime.evaluate',{expression:`document.documentElement.dataset.ready==='true'`,returnByValue:true});
      if(ready.result?.value===true){loaded=true;break;}
      await new Promise(done=>setTimeout(done,100));
    }
    if(!loaded)throw new Error(`${name}: explainer did not become ready`);
    const result=await call('Runtime.evaluate',{expression:`(()=>{
      const main=document.querySelector('main'), rect=main.getBoundingClientRect();
      const bad=[];let minFont=Infinity;
      for(const el of main.querySelectorAll('*')){
        if(el.closest('svg'))continue;
        const style=getComputedStyle(el),r=el.getBoundingClientRect();
        const ownText=[...el.childNodes].some(n=>n.nodeType===3&&n.textContent.trim());
        if(ownText){
          const size=parseFloat(style.fontSize);minFont=Math.min(minFont,size);
          if(size<13)bad.push(el.tagName+': font below 13px');
          const range=document.createRange();range.selectNodeContents(el);
          for(const line of range.getClientRects())if(line.left < rect.left-.5||line.right>rect.right+.5||line.bottom>rect.bottom+.5)bad.push(el.tagName+': clipped text');
        }
        if(el.scrollWidth>el.clientWidth+1&&r.width>0)bad.push(el.tagName+'.'+el.className+': horizontal overflow');
      }
      for(const pre of main.querySelectorAll('pre.raw')){
        if(getComputedStyle(pre).whiteSpace!=='pre')bad.push('Terraform excerpt may wrap');
      }
      for(const track of main.querySelectorAll('.gantt-track')){
        const labels=[...track.querySelectorAll('.bar-label')].map(el=>el.getBoundingClientRect()).sort((a,b)=>a.left-b.left);
        if(labels.some((r,i)=>i>0&&r.left<labels[i-1].right+4))bad.push('Overlapping operation labels');
        if([...track.querySelectorAll('.gantt-bar')].some(el=>el.getBoundingClientRect().width<17.9))bad.push('Operation below 18px minimum width');
      }
      return {width:rect.width,height:Math.ceil(rect.height),minFont,bad,theme:document.documentElement.dataset.theme,fonts:[...document.fonts].map(f=>({family:f.family,status:f.status})),text:main.innerText};
    })()`,returnByValue:true});
    const check=result.result.value;
    if(check.bad.length)throw new Error(`${name}/${theme}: ${JSON.stringify(check.bad)}`);
    if(check.fonts.some(f=>f.status!=='loaded'))throw new Error(`${name}/${theme}: unloaded font`);
    // CDP reports the font that actually drew each glyph, including fallback.
    const {root:dom}=await call('DOM.getDocument',{depth:-1,pierce:true});
    const textElements=[];
    function walk(n,inSVG=false){
      const svg=inSVG||n.nodeName==='svg';
      if(!svg&&(n.children||[]).some(c=>c.nodeType===3&&c.nodeValue.trim()))textElements.push(n.nodeId);
      for(const c of n.children||[])walk(c,svg);
    }
    walk(dom);
    const actual=new Map();
    for(const nodeId of textElements){
      const {fonts}=await call('CSS.getPlatformFontsForNode',{nodeId});
      for(const font of fonts)if(font.glyphCount>0)actual.set(font.familyName,{family:font.familyName,custom:font.isCustomFont});
    }
    const platformFonts=[...actual.values()];
    if(platformFonts.some(f=>!f.custom||!/^Geist/.test(f.family)))throw new Error(`${name}/${theme}: fallback font ${JSON.stringify(platformFonts)}`);
    const {data}=await call('Page.captureScreenshot',{format:'png',captureBeyondViewport:true,clip:{x:0,y:0,width:800,height:check.height,scale:1}});
    const bytes=Buffer.from(data,'base64');
    if(bytes.readUInt32BE(16)!==1600||bytes.readUInt32BE(20)!==check.height*2)throw new Error('Unexpected PNG dimensions');
    const pixels=verifyPixels(bytes,theme);
    const file=`explain-${name}${theme==='dark'?'-dark':''}.png`;
    await writeFile(resolve(output,file),bytes);
    evidence.push({file,cssSize:[800,check.height],pngSize:[1600,check.height*2],minimumFontPx:check.minFont,fonts:platformFonts,overflow:check.bad,...pixels,sha256:createHash('sha256').update(bytes).digest('hex')});
    console.log(`${file}: 1600 × ${check.height*2}; ${platformFonts.map(f=>f.family).join(', ')}; min ${check.minFont}px; no overflow`);
    await send('Target.closeTarget',{targetId});
  }
  await captureSite({send,root,port,here,run,evidence,pageErrors});
  await writeFile(resolve(here,'render-checks.json'),JSON.stringify({sample:`go run ${CLI_PATH} demo aws`,sourcePlan:'testdata/plans/aws-webapp.terraform.json',sourcePlanSha256:createHash('sha256').update(await readFile(resolve(root,'testdata/plans/aws-webapp.terraform.json'))).digest('hex'),images:evidence},null,2)+'\n');
} finally {
  if(socket?.readyState===WebSocket.OPEN)try{await send('Browser.close');}catch{}
  if(socket)socket.close();
  if(chromium.exitCode===null){
    await new Promise(done=>{
      const timer=setTimeout(()=>{chromium.kill('SIGKILL');done();},3000);
      chromium.once('exit',()=>{clearTimeout(timer);done();});
    });
  }
  server.close();
  await rm(profile,{recursive:true,force:true,maxRetries:4,retryDelay:200});
}

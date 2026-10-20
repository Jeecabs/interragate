import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {resolve} from 'node:path';

// Shares render.mjs's single headless Chromium and loopback server.
export async function captureSite({send, root, port, here, run, evidence, pageErrors}) {
  const origin=`http://127.0.0.1:${port}`;
  const checksDir=resolve(here,'checks');
  await mkdir(checksDir,{recursive:true});
  async function open(path, width, height, theme, scale=2, ready="document.readyState==='complete'") {
    const {targetId}=await send('Target.createTarget',{url:'about:blank'});
    const {sessionId}=await send('Target.attachToTarget',{targetId,flatten:true});
    const call=(method,params)=>send(method,params,sessionId);
    for(const domain of ['Page','Runtime','Log']) await call(`${domain}.enable`);
    await call('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:scale,mobile:false});
    await call('Emulation.setEmulatedMedia',{features:[{name:'prefers-color-scheme',value:theme},{name:'prefers-reduced-motion',value:'reduce'}]});
    await call('Emulation.setDefaultBackgroundColorOverride',{color:{r:0,g:0,b:0,a:0}});
    await call('Page.navigate',{url:origin+path});
    let loaded=false;
    for(let attempt=0;attempt<150;attempt++) {
      const result=await call('Runtime.evaluate',{expression:ready,returnByValue:true});
      if(result.result?.value===true){loaded=true;break;}
      await new Promise(done=>setTimeout(done,100));
    }
    if(!loaded) throw new Error(`Page did not become ready: ${path}`);
    const evaluate=async expression=>{
      const result=await call('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});
      if(result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
      return result.result.value;
    };
    await evaluate(`(async()=>{await document.fonts.ready;await new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(done)));return true})()`);
    return {call,evaluate,close:()=>send('Target.closeTarget',{targetId})};
  }
  for(const theme of ['light','dark']) {
    const suffix=theme==='dark'?'-dark':'';
    const views={overview:'',map:'#view=map&r=aws_db_instance.orders',apply:'#view=apply&r=aws_db_instance.orders',diff:'#r=aws_iam_role_policy.app'};
    for(const [name,hash] of Object.entries(views)) {
      const page=await open(`/_site/demo/${hash}`,1280,800,theme,2,"document.readyState==='complete'&&!!document.querySelector('.brand')");
      try {
        // The report opens resource deep links at the workspace. Keep the
        // header in frame; show its first diff in the detail column.
        if(name==='apply')await page.evaluate("window.scrollTo(0,document.querySelector('.apply-legend').getBoundingClientRect().top+scrollY-80);true");
        if(name==='diff')await page.evaluate(`(()=>{
          document.querySelector('.workspace').scrollIntoView();
          const core=document.querySelector('.detail > .core');
          const heading=core.querySelector('.diff')?.previousElementSibling;
          if(!heading)throw new Error('Policy diff heading not found');
          core.scrollTop+=heading.getBoundingClientRect().top-core.getBoundingClientRect().top-28;
          return true;
        })()`);
        await page.evaluate("new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(()=>done(true))))");
        const {data}=await page.call('Page.captureScreenshot',{format:'webp',quality:90});
        const file=`${name}${suffix}.webp`;
        await writeFile(resolve(root,'site/images',file),Buffer.from(data,'base64'));
        evidence.push({file:`site/images/${file}`,cssSize:[1280,800],deviceScaleFactor:2});
        console.log(`${file}: report at 1280 × 800, scale 2`);
      } finally {await page.close();}
    }
  }
  const social=await open('/docs/explainers/landing.html?panel=social',1200,630,'light',1,"document.documentElement.dataset.ready==='true'");
  try {
    const {data}=await social.call('Page.captureScreenshot',{format:'png'});
    await writeFile(resolve(root,'site/og.png'),Buffer.from(data,'base64'));
    evidence.push({file:'site/og.png',cssSize:[1200,630]});
    console.log('og.png: 1200 × 630');
  } finally {await social.close();}

  // Rebuild so the served page contains the newly captured assets.
  await run('go',['run','site/build.go','-o','_site'],{cwd:root,maxBuffer:8e6});
  for(const theme of ['light','dark']) for(const width of [1440,390]) {
    const page=await open('/_site/',width,1000,theme,1);
    try {
      await page.evaluate(`(async()=>{
        document.querySelectorAll('img[loading]').forEach(img=>img.loading='eager');
        await Promise.all([...document.images].map(img=>img.decode()));
        return true;
      })()`);
      const check=await page.evaluate(`(async()=>{
        const bad=[],links=[];
        if(document.documentElement.scrollWidth>innerWidth)bad.push('Page overflows');
        if(document.title.indexOf('interragate')<0)bad.push('Wrong title');
        for(const img of document.images)if(!img.complete||!img.naturalWidth)bad.push('Broken image: '+img.src);
        for(const a of document.querySelectorAll('a[href]')){
          const u=new URL(a.href);
          if(u.origin!==location.origin)continue;
          const response=await fetch(u.pathname);
          if(!response.ok)bad.push('Broken link: '+a.getAttribute('href'));
          else if(u.hash&&!u.hash.includes('=')){
            const doc=new DOMParser().parseFromString(await response.text(),'text/html');
            if(!doc.getElementById(decodeURIComponent(u.hash.slice(1))))bad.push('Missing anchor: '+u.hash);
          }
          links.push(a.getAttribute('href'));
        }
        const tabs=[...document.querySelectorAll('[role=tab]')];
        for(const tab of tabs){tab.click();if(tab.getAttribute('aria-selected')!=='true'||!document.getElementById(tab.getAttribute('aria-controls')).classList.contains('on'))bad.push('Tab failed: '+tab.textContent)}
        tabs[0].click();
        tabs[0].dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}));
        if(document.activeElement!==tabs[1])bad.push('Tab keyboard navigation failed');
        tabs[0].click();document.activeElement.blur();
        // Every scene of the story has its steps, ends on its result with
        // reduced motion, and fits its frame.
        const scenes=[...document.querySelectorAll('.scene')];
        if(scenes.length!==7)bad.push('Expected 7 scenes, found '+scenes.length);
        for(const s of scenes){
          const steps=s.querySelectorAll('.beats button').length;
          if(steps!==+s.dataset.beats)bad.push('Scene steps: '+s.className);
          if(!s.classList.contains('b'+s.dataset.beats))bad.push('Scene not at its result: '+s.className);
          const art=s.querySelector('.art'),frame=art.getBoundingClientRect();
          for(const el of art.querySelectorAll('*')){const r=el.getBoundingClientRect();if(r.width&&(r.right>frame.right+1||r.left<frame.left-1))bad.push('Scene overflows: '+s.className+' '+el.className);}
        }
        return {width:innerWidth,height:document.documentElement.scrollHeight,theme:getComputedStyle(document.body).backgroundColor,links:[...new Set(links)],tabs:tabs.length,copyButtons:document.querySelectorAll('.copy').length,bad};
      })()`);
      await send('Browser.grantPermissions',{origin,permissions:['clipboardReadWrite','clipboardSanitizedWrite']});
      check.clipboard=await page.evaluate(`(async()=>{
        const bad=[];
        for(const box of document.querySelectorAll('.codebox')){
          const pre=box.querySelector('pre'),button=box.querySelector('button');
          button.click();
          for(let i=0;i<30&&!button.classList.contains('done');i++)await new Promise(done=>setTimeout(done,20));
          const expected=(pre.getAttribute('data-copy')||pre.textContent).trim()+'\\n';
          if(await navigator.clipboard.readText()!==expected)bad.push(expected);
        }
        return {buttons:document.querySelectorAll('.copy').length,bad};
      })()`);
      if(check.bad.length||check.clipboard.bad.length)throw new Error(`Site ${width}/${theme}: ${JSON.stringify(check)}`);
      await page.evaluate('new Promise(done=>setTimeout(()=>done(true),1700))');
      await page.evaluate('window.scrollTo(0,0);true');
      const {data}=await page.call('Page.captureScreenshot',{format:'png',captureBeyondViewport:true,clip:{x:0,y:0,width,height:check.height,scale:1}});
      await writeFile(resolve(checksDir,`site-${width}-${theme}.png`),Buffer.from(data,'base64'));
      evidence.push({page:`site/${width}/${theme}`,...check});
      console.log(`Site ${width}/${theme}: images, links, four tabs, keyboard, seven scenes and ${check.clipboard.buttons} copy buttons pass`);
    } finally {await page.close();}
  }
  const rules=await open('/_site/rules/',390,844,'dark',1,"document.readyState==='complete'&&!!document.querySelector('[data-filter]:not([hidden])')");
  try {
    const result=await rules.evaluate(`(()=>{
      const filter=document.querySelector('[data-filter]');
      filter.value='protected-delete';filter.dispatchEvent(new Event('input'));
      const match=document.querySelectorAll('.rule:not([hidden])').length;
      filter.value='no such rule xyz';filter.dispatchEvent(new Event('input'));
      const empty=!document.querySelector('.no-match').hidden;
      filter.value='';filter.dispatchEvent(new Event('input'));
      return {match,empty,overflow:document.documentElement.scrollWidth>innerWidth};
    })()`);
    if(result.match!==1||!result.empty||result.overflow)throw new Error(`Rules filter failed: ${JSON.stringify(result)}`);
    evidence.push({page:'rules/390/dark',...result});
  } finally {await rules.close();}
  if(pageErrors.length)throw new Error(`Browser errors: ${JSON.stringify(pageErrors)}`);
}

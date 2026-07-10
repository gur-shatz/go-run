package logviewer

var logviewerHTML = []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Log files</title>
<style>
:root{color-scheme:dark;--bg:#101418;--panel:#161c22;--line:#2a333d;--text:#e7edf3;--muted:#9aa8b5;--accent:#58a6ff;--warn:#f2cc60;--err:#ff7b72;--ok:#7ee787}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:13px/1.45 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
.app{display:grid;grid-template-columns:280px 1fr;height:100vh;min-height:520px}.side{border-right:1px solid var(--line);background:var(--panel);overflow:auto}.side h1{font-size:15px;margin:14px 14px 8px}.streams{display:flex;flex-direction:column}.stream{border:0;border-top:1px solid var(--line);background:transparent;color:inherit;text-align:left;padding:10px 14px;cursor:pointer}.stream:hover,.stream.active{background:#1f2730}.name{font-weight:650;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.meta{color:var(--muted);font-size:12px;margin-top:2px}.main{display:grid;grid-template-rows:auto 1fr;min-width:0}.bar{display:flex;gap:8px;align-items:center;padding:10px;border-bottom:1px solid var(--line);background:#11171d}.bar input,.bar select{background:#0c1116;color:var(--text);border:1px solid var(--line);border-radius:6px;padding:7px 8px;min-width:0}.bar input[type=search]{flex:1}.bar button{background:#212a33;color:var(--text);border:1px solid var(--line);border-radius:6px;padding:7px 10px;cursor:pointer}.bar button:hover{border-color:var(--accent)}.rows{overflow:auto;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}.row{display:grid;grid-template-columns:210px 70px 120px 210px minmax(260px,1fr);gap:10px;border-bottom:1px solid #202832;padding:5px 10px;white-space:pre-wrap;overflow-wrap:anywhere}.row:hover{background:#151c23}.row.context{opacity:.55}.row.context:hover{opacity:1}.row.context.top{border-top:1px solid var(--accent)}.row.context.bottom{border-bottom:1px solid var(--accent)}.row.match{opacity:1;box-shadow:inset 3px 0 0 var(--accent);background:rgba(88,166,255,.08)}.time,.logger,.caller,.fields{color:var(--muted)}.level{font-weight:750}.level.ERROR,.level.FATAL{color:var(--err)}.level.WARN{color:var(--warn)}.level.INFO{color:var(--ok)}.msg{min-width:0}.empty{padding:24px;color:var(--muted)}@media(max-width:800px){.app{grid-template-columns:1fr;grid-template-rows:210px 1fr}.side{border-right:0;border-bottom:1px solid var(--line)}.row{grid-template-columns:120px 58px minmax(180px,1fr)}.logger,.caller{display:none}.bar{flex-wrap:wrap}.bar input[type=search]{flex-basis:100%}}
</style>
</head>
<body>
<div class="app">
  <aside class="side"><h1>Log files</h1><div id="streams" class="streams"></div></aside>
  <main class="main">
    <div class="bar">
      <input id="search" type="search" placeholder="Search">
      <select id="context"><option value="0">Context 0</option><option value="1">Context 1</option><option value="3" selected>Context 3</option><option value="5">Context 5</option></select>
      <select id="level"><option value="">All levels</option><option>DEBUG</option><option>INFO</option><option>WARN</option><option>ERROR</option></select>
      <button id="tail">Newest</button>
      <label style="display:flex;align-items:center;gap:4px;color:var(--muted);white-space:nowrap"><input id="tailMode" type="checkbox"> Tail mode</label>
      <button id="older">Older</button>
      <button id="newer">Newer</button>
      <span id="position" style="display:flex;align-items:center;gap:6px;min-width:150px;color:var(--muted);font-size:12px;white-space:nowrap"></span>
    </div>
    <div id="rows" class="rows"><div class="empty">Select a stream</div></div>
  </main>
</div>
<script>
let current="",prev="",next="",tailTimer=0,committedSearch="",pageSignature="";
const streamsEl=document.getElementById("streams"),rowsEl=document.getElementById("rows"),searchEl=document.getElementById("search"),levelEl=document.getElementById("level"),contextEl=document.getElementById("context"),positionEl=document.getElementById("position"),tailModeEl=document.getElementById("tailMode"),tailBtn=document.getElementById("tail"),olderBtn=document.getElementById("older"),newerBtn=document.getElementById("newer");
async function getJSON(url){const r=await fetch(url);if(!r.ok)throw new Error(await r.text());return r.json()}
function fmtSize(n){if(n>1e9)return(n/1e9).toFixed(1)+" GB";if(n>1e6)return(n/1e6).toFixed(1)+" MB";if(n>1e3)return(n/1e3).toFixed(1)+" KB";return n+" B"}
function escapeHTML(s){return String(s||"").replace(/[&<>"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]))}
function basePath(){const path=location.pathname.endsWith("/")?location.pathname:location.pathname+"/";return path}
function currentFromPath(){const parts=location.pathname.split("/").filter(Boolean);return parts.length?decodeURIComponent(parts[parts.length-1]):""}
async function loadStreams(){try{const data=await getJSON("../index.json");streamsEl.innerHTML="";for(const s of data.entries||[]){const id=s.name;const b=document.createElement("button");b.className="stream"+(id===current?" active":"");b.innerHTML='<div class="name">'+escapeHTML(id)+'</div><div class="meta">'+escapeHTML(s.description||"")+'</div>';b.onclick=()=>{location.href="../"+encodeURIComponent(id)+"/"};streamsEl.appendChild(b)}}catch(e){streamsEl.innerHTML=""}}
async function openCurrent(){current=currentFromPath();prev=next="";if(current)await loadPage("tail?limit=200")}
function filterURL(base){const p=new URLSearchParams();p.set("limit","200");if(committedSearch)p.set("q",committedSearch);if(committedSearch&&contextEl.value&&!tailModeEl.checked)p.set("context",contextEl.value);if(levelEl.value&&!tailModeEl.checked)p.set("level",levelEl.value);return base+(base.includes("?")?"&":"?")+p.toString()}
function fmtBytes(n){if(n>1e9)return(n/1e9).toFixed(1)+" GB";if(n>1e6)return(n/1e6).toFixed(1)+" MB";if(n>1e3)return(n/1e3).toFixed(1)+" KB";return n+" B"}
function setPosition(r){if(!r){positionEl.innerHTML="";return}const total=Math.max(1,r.total_bytes||1),start=Math.max(0,Math.min(100,r.start_absolute*100/total)),end=Math.max(start,Math.min(100,r.end_absolute*100/total)),width=Math.max(1,end-start),label=Math.round(end)+"%";const tip=r.entry_count+" entries\nstream bytes: "+r.start_absolute+"-"+r.end_absolute+" of "+r.total_bytes+"\nsegment: "+r.start_path+" ["+r.start_offset+"] to "+r.end_path+" ["+r.end_offset+"]\nsegments: "+r.segment_count;positionEl.title=tip;positionEl.innerHTML='<span style="position:relative;display:inline-block;width:96px;height:8px;border:1px solid var(--line);background:#0c1116;border-radius:999px;overflow:hidden"><span style="position:absolute;left:'+start+'%;width:'+width+'%;top:0;bottom:0;background:var(--accent)"></span></span><span>'+escapeHTML(label)+'</span>'}
function signature(page){const r=page&&page.range||{};return [page&&page.prev_cursor,page&&page.next_cursor,r.entry_count,r.start_absolute,r.end_absolute,r.total_bytes].join("|")}
async function loadPage(url,append,quiet){try{if(!append&&!quiet)rowsEl.innerHTML='<div class="empty">Loading</div>';const page=await getJSON(filterURL(url));const sig=signature(page);if(quiet&&sig===pageSignature)return;pageSignature=sig;prev=page.bof?"":(page.prev_cursor||prev);next=page.eof?"":(page.next_cursor||next);setPosition(page.range);render(page.entries,append,page)}catch(e){if(!append&&!quiet)rowsEl.innerHTML='<div class="empty">'+escapeHTML(e.message)+'</div>';else if(!quiet)rowsEl.insertAdjacentHTML("beforeend",'<div class="empty">'+escapeHTML(e.message)+'</div>')}}
function render(entries,append,page){entries=entries||[];const searching=!!committedSearch&&contextEl.value!=="0";const html=entries.map(e=>{let cls=e.match?'row match':(searching?'row context':'row');if(e.context_top)cls+=' top';if(e.context_bottom)cls+=' bottom';return '<div class="'+cls+'"><div class="time">'+escapeHTML(e.time||"")+'</div><div class="level '+escapeHTML(e.level)+'">'+escapeHTML(e.level)+'</div><div class="logger">'+escapeHTML(e.logger)+'</div><div class="caller">'+escapeHTML(e.caller)+'</div><div class="msg">'+escapeHTML(e.message||e.raw)+' <span class="fields">'+escapeHTML(Object.entries(e.fields||{}).map(([k,v])=>k+"="+v).join(" "))+'</span></div></div>'}).join("");const empty=page&&page.bof?'Start of log':(page&&page.eof?'End of log':'No matching entries');if(append)rowsEl.insertAdjacentHTML("beforeend",html||'<div class="empty">'+empty+'</div>');else rowsEl.innerHTML=html||'<div class="empty">'+empty+'</div>'}
function newestURL(){return "tail?limit="+(tailModeEl.checked?"20":"200")}
function loadNewest(quiet){return current&&loadPage(newestURL(),false,quiet)}
function setTailMode(){clearInterval(tailTimer);tailTimer=0;const on=tailModeEl.checked;tailBtn.disabled=on;olderBtn.disabled=on;newerBtn.disabled=on;levelEl.disabled=on;contextEl.disabled=on;if(on){loadNewest(false);tailTimer=setInterval(()=>loadNewest(true),5000)}}
function runSearch(){if(!current)return;committedSearch=searchEl.value;if(tailModeEl.checked)loadNewest();else loadPage(committedSearch?"search":"tail?limit=200")}
tailBtn.onclick=()=>loadNewest();
olderBtn.onclick=()=>current&&prev&&loadPage("page?direction=backward&cursor="+encodeURIComponent(prev));
newerBtn.onclick=()=>current&&next&&loadPage("page?direction=forward&cursor="+encodeURIComponent(next),true);
tailModeEl.onchange=setTailMode;
searchEl.onkeydown=e=>{if(e.key==="Enter"&&current)runSearch()};levelEl.onchange=()=>current&&loadPage("search");contextEl.onchange=()=>current&&committedSearch&&loadPage("search");
loadStreams().then(openCurrent).catch(e=>rowsEl.innerHTML='<div class="empty">'+escapeHTML(e.message)+'</div>');
</script>
</body>
</html>`)

var logstreamHTML = []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Log stream</title>
<style>
:root{color-scheme:dark;--bg:#101418;--line:#2a333d;--text:#e7edf3;--muted:#9aa8b5;--accent:#58a6ff;--warn:#f2cc60;--err:#ff7b72;--ok:#7ee787}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:13px/1.45 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
.app{display:grid;grid-template-rows:auto 1fr;height:100vh;min-height:420px}.bar{display:flex;gap:8px;align-items:center;padding:10px;border-bottom:1px solid var(--line);background:#11171d}.bar input,.bar select{background:#0c1116;color:var(--text);border:1px solid var(--line);border-radius:6px;padding:7px 8px;min-width:0}.bar input[type=search]{flex:1}.bar button{background:#212a33;color:var(--text);border:1px solid var(--line);border-radius:6px;padding:7px 10px;cursor:pointer}.bar button:hover{border-color:var(--accent)}.rows{overflow:auto;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}.row{display:grid;grid-template-columns:210px 70px 120px 210px minmax(260px,1fr);gap:10px;border-bottom:1px solid #202832;padding:5px 10px;white-space:pre-wrap;overflow-wrap:anywhere}.row:hover{background:#151c23}.row.context{opacity:.55}.row.context:hover{opacity:1}.row.context.top{border-top:1px solid var(--accent)}.row.context.bottom{border-bottom:1px solid var(--accent)}.row.match{opacity:1;box-shadow:inset 3px 0 0 var(--accent);background:rgba(88,166,255,.08)}.time,.logger,.caller,.fields{color:var(--muted)}.level{font-weight:750}.level.ERROR,.level.FATAL{color:var(--err)}.level.WARN{color:var(--warn)}.level.INFO{color:var(--ok)}.msg{min-width:0}.empty{padding:24px;color:var(--muted)}@media(max-width:800px){.row{grid-template-columns:120px 58px minmax(180px,1fr)}.logger,.caller{display:none}.bar{flex-wrap:wrap}.bar input[type=search]{flex-basis:100%}}
</style>
</head>
<body>
<div class="app">
  <div class="bar">
    <input id="search" type="search" placeholder="Search">
    <select id="context"><option value="0">Context 0</option><option value="1">Context 1</option><option value="3" selected>Context 3</option><option value="5">Context 5</option></select>
    <select id="level"><option value="">All levels</option><option>DEBUG</option><option>INFO</option><option>WARN</option><option>ERROR</option></select>
    <button id="tail">Newest</button>
    <label style="display:flex;align-items:center;gap:4px;color:var(--muted);white-space:nowrap"><input id="tailMode" type="checkbox"> Tail mode</label>
    <button id="older">Older</button>
    <button id="newer">Newer</button>
    <span id="position" style="display:flex;align-items:center;gap:6px;min-width:150px;color:var(--muted);font-size:12px;white-space:nowrap"></span>
  </div>
  <div id="rows" class="rows"><div class="empty">Loading</div></div>
</div>
<script>
let prev="",next="",tailTimer=0,committedSearch="",pageSignature="";
const rowsEl=document.getElementById("rows"),searchEl=document.getElementById("search"),levelEl=document.getElementById("level"),contextEl=document.getElementById("context"),positionEl=document.getElementById("position"),tailModeEl=document.getElementById("tailMode"),tailBtn=document.getElementById("tail"),olderBtn=document.getElementById("older"),newerBtn=document.getElementById("newer");
async function getJSON(url){const r=await fetch(url);if(!r.ok)throw new Error(await r.text());return r.json()}
function escapeHTML(s){return String(s||"").replace(/[&<>"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]))}
function filterURL(base){const p=new URLSearchParams();p.set("limit","200");if(committedSearch)p.set("q",committedSearch);if(committedSearch&&contextEl.value&&!tailModeEl.checked)p.set("context",contextEl.value);if(levelEl.value&&!tailModeEl.checked)p.set("level",levelEl.value);return base+(base.includes("?")?"&":"?")+p.toString()}
function fmtBytes(n){if(n>1e9)return(n/1e9).toFixed(1)+" GB";if(n>1e6)return(n/1e6).toFixed(1)+" MB";if(n>1e3)return(n/1e3).toFixed(1)+" KB";return n+" B"}
function setPosition(r){if(!r){positionEl.innerHTML="";return}const total=Math.max(1,r.total_bytes||1),start=Math.max(0,Math.min(100,r.start_absolute*100/total)),end=Math.max(start,Math.min(100,r.end_absolute*100/total)),width=Math.max(1,end-start),label=Math.round(end)+"%";const tip=r.entry_count+" entries\nstream bytes: "+r.start_absolute+"-"+r.end_absolute+" of "+r.total_bytes+"\nsegment: "+r.start_path+" ["+r.start_offset+"] to "+r.end_path+" ["+r.end_offset+"]\nsegments: "+r.segment_count;positionEl.title=tip;positionEl.innerHTML='<span style="position:relative;display:inline-block;width:96px;height:8px;border:1px solid var(--line);background:#0c1116;border-radius:999px;overflow:hidden"><span style="position:absolute;left:'+start+'%;width:'+width+'%;top:0;bottom:0;background:var(--accent)"></span></span><span>'+escapeHTML(label)+'</span>'}
function signature(page){const r=page&&page.range||{};return [page&&page.prev_cursor,page&&page.next_cursor,r.entry_count,r.start_absolute,r.end_absolute,r.total_bytes].join("|")}
async function loadPage(url,append,quiet){try{if(!append&&!quiet)rowsEl.innerHTML='<div class="empty">Loading</div>';const page=await getJSON(filterURL(url));const sig=signature(page);if(quiet&&sig===pageSignature)return;pageSignature=sig;prev=page.bof?"":(page.prev_cursor||prev);next=page.eof?"":(page.next_cursor||next);setPosition(page.range);render(page.entries,append,page)}catch(e){if(!append&&!quiet)rowsEl.innerHTML='<div class="empty">'+escapeHTML(e.message)+'</div>';else if(!quiet)rowsEl.insertAdjacentHTML("beforeend",'<div class="empty">'+escapeHTML(e.message)+'</div>')}}
function render(entries,append,page){entries=entries||[];const searching=!!committedSearch&&contextEl.value!=="0";const html=entries.map(e=>{let cls=e.match?'row match':(searching?'row context':'row');if(e.context_top)cls+=' top';if(e.context_bottom)cls+=' bottom';return '<div class="'+cls+'"><div class="time">'+escapeHTML(e.time||"")+'</div><div class="level '+escapeHTML(e.level)+'">'+escapeHTML(e.level)+'</div><div class="logger">'+escapeHTML(e.logger)+'</div><div class="caller">'+escapeHTML(e.caller)+'</div><div class="msg">'+escapeHTML(e.message||e.raw)+' <span class="fields">'+escapeHTML(Object.entries(e.fields||{}).map(([k,v])=>k+"="+v).join(" "))+'</span></div></div>'}).join("");const empty=page&&page.bof?'Start of log':(page&&page.eof?'End of log':'No matching entries');if(append)rowsEl.insertAdjacentHTML("beforeend",html||'<div class="empty">'+empty+'</div>');else rowsEl.innerHTML=html||'<div class="empty">'+empty+'</div>'}
function newestURL(){return "tail?limit="+(tailModeEl.checked?"20":"200")}
function loadNewest(quiet){return loadPage(newestURL(),false,quiet)}
function setTailMode(){clearInterval(tailTimer);tailTimer=0;const on=tailModeEl.checked;tailBtn.disabled=on;olderBtn.disabled=on;newerBtn.disabled=on;levelEl.disabled=on;contextEl.disabled=on;if(on){loadNewest(false);tailTimer=setInterval(()=>loadNewest(true),5000)}}
function runSearch(){committedSearch=searchEl.value;if(tailModeEl.checked)loadNewest();else loadPage(committedSearch?"search":"tail?limit=200")}
tailBtn.onclick=()=>loadNewest();
olderBtn.onclick=()=>prev&&loadPage("page?direction=backward&cursor="+encodeURIComponent(prev));
newerBtn.onclick=()=>next&&loadPage("page?direction=forward&cursor="+encodeURIComponent(next),true);
tailModeEl.onchange=setTailMode;
searchEl.onkeydown=e=>{if(e.key==="Enter")runSearch()};levelEl.onchange=()=>loadPage("search");contextEl.onchange=()=>committedSearch&&loadPage("search");
loadPage("tail?limit=200").catch(e=>rowsEl.innerHTML='<div class="empty">'+escapeHTML(e.message)+'</div>');
</script>
</body>
</html>`)

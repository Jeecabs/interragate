/* interragate report: a self-contained viewer for one analysed plan.
   Every string from the plan is inserted with textContent; nothing is parsed
   as HTML. No network access, no dependencies. */
// Copyright (C) 2026 Jeecabs and interragate contributors
// SPDX-License-Identifier: AGPL-3.0-only
// Phosphor SVG icons retain their MIT licence; see third_party/phosphor/LICENSE.
(function () {
  "use strict";

  var data = JSON.parse(document.getElementById("interragate-data").textContent);
  // The summary interragate writes for a pull request, ready to paste.
  var prComment = (function () {
    var el = document.getElementById("interragate-markdown");
    try { return el ? JSON.parse(el.textContent) : ""; } catch (e) { return ""; }
  })();
  var changes = data.changes || [];
  var findings = data.findings || [];
  var drift = data.drift || [];
  var outputs = data.outputs || [];
  var graph = data.graph || { nodes: [], edges: [] };
  var graphNodes = graph.nodes || [];
  var graphEdges = graph.edges || [];
  var plan = data.plan || {};
  var summary = data.summary || {};
  // Set when the page is served by interragate watch (see the live section).
  var liveData = (function () {
    var el = document.getElementById("interragate-live");
    if (!el) return null;
    try { return JSON.parse(el.textContent); } catch (e) { return null; }
  })();
  var sinceByAddr = new Map();
  if (liveData && liveData.delta) (liveData.delta.changes || []).forEach(function (c) { sinceByAddr.set(c.address, c); });

  // ---------------------------------------------------------------- indexes

  var byAddr = new Map();
  var byResource = new Map();
  changes.forEach(function (c) {
    byAddr.set(c.address, c);
    if (!byResource.has(c.resource)) byResource.set(c.resource, []);
    byResource.get(c.resource).push(c);
  });
  var findingById = new Map(findings.map(function (f) { return [f.id, f]; }));
  var nodeById = new Map(graphNodes.map(function (n) { return [n.id, n]; }));
  var ups = new Map(), downs = new Map();
  graphNodes.forEach(function (n) { ups.set(n.id, []); downs.set(n.id, []); });
  graphEdges.forEach(function (e) {
    if (!ups.has(e[1]) || !downs.has(e[0])) return;
    ups.get(e[1]).push(e[0]);
    downs.get(e[0]).push(e[1]);
  });
  var outputNames = new Set(outputs.map(function (o) { return o.name; }));

  var TOOL = plan.tool === "opentofu" ? "OpenTofu" : "Terraform";
  var SEVERITIES = ["critical", "high", "medium", "low", "info", "none"];
  var sevRank = { none: 0, info: 1, low: 2, medium: 3, high: 4, critical: 5 };
  var SEV_LABEL = { critical: "Critical", high: "High", medium: "Medium", low: "Low", info: "Info", none: "None" };

  // ------------------------------------------------------------------ state

  var state = {
    tab: "changes",
    sel: null, // { kind: "change" | "resource", id }
    q: "",
    actions: { create: true, update: true, replace: true, delete: true, other: true },
    cosmetic: false,
    unchanged: false,
    collapsed: new Set(),
    mapScope: "changed",
    mapGroup: "module",
    view: null, // map camera: { k, x, y }
    mapTime: "plan", // the map draws the plan's changes, or the infrastructure before or after the apply
    groupBy: "module", // how the change list is grouped: module, kind or action
    noiseDrift: false,
  };

  function reducedMotion() {
    return !!(window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
  }

  // ---------------------------------------------------------------- helpers

  function h(tag, props) {
    var el = document.createElement(tag);
    if (props) {
      Object.keys(props).forEach(function (k) {
        var v = props[k];
        if (v === undefined || v === null || v === false) return;
        if (k === "class") el.className = v;
        else if (k === "text") el.textContent = v;
        else if (k.slice(0, 2) === "on") el.addEventListener(k.slice(2), v);
        else el.setAttribute(k, v === true ? "" : v);
      });
    }
    for (var i = 2; i < arguments.length; i++) append(el, arguments[i]);
    return el;
  }

  function append(el, child) {
    if (child === undefined || child === null || child === false) return;
    if (Array.isArray(child)) { child.forEach(function (c) { append(el, c); }); return; }
    if (typeof child === "string" || typeof child === "number") el.appendChild(document.createTextNode(String(child)));
    else el.appendChild(child);
  }

  var SVGNS = "http://www.w3.org/2000/svg";
  function s(tag, attrs) {
    var el = document.createElementNS(SVGNS, tag);
    if (attrs) Object.keys(attrs).forEach(function (k) { if (attrs[k] !== undefined && attrs[k] !== null) el.setAttribute(k, attrs[k]); });
    for (var i = 2; i < arguments.length; i++) {
      var c = arguments[i];
      if (c === undefined || c === null || c === false) continue;
      if (typeof c === "string") el.appendChild(document.createTextNode(c));
      else el.appendChild(c);
    }
    return el;
  }

  // Phosphor Icons, Light weight (MIT, Copyright (c) 2023 Phosphor Icons). viewBox 0 0 256 256.
  var ICONS = {
    // Play and pause are drawn here as outlines (a shape with a hole cut by
    // winding), to match the light weight of the Phosphor set.
    play: ["M76,36L212,128L76,220Z M88,58.6L88,197.4L190.6,128Z"],
    download: ["M122,40H134V141.5L170.8,104.7L179.3,113.2L128,164.5L76.7,113.2L85.2,104.7L122,141.5Z", "M40,152H52V204H204V152H216V216H40Z"],
    pause: ["M64,40H108V216H64Z M76,52V204H96V52Z M148,40H192V216H148Z M160,52V204H180V52Z"],
    search: ["M228.24,219.76l-51.38-51.38a86.15,86.15,0,1,0-8.48,8.48l51.38,51.38a6,6,0,0,0,8.48-8.48ZM38,112a74,74,0,1,1,74,74A74.09,74.09,0,0,1,38,112Z"],
    copy: ["M216,34H88a6,6,0,0,0-6,6V82H40a6,6,0,0,0-6,6V216a6,6,0,0,0,6,6H168a6,6,0,0,0,6-6V174h42a6,6,0,0,0,6-6V40A6,6,0,0,0,216,34ZM162,210H46V94H162Zm48-48H174V88a6,6,0,0,0-6-6H94V46H210Z"],
    check: ["M228.24,76.24l-128,128a6,6,0,0,1-8.48,0l-56-56a6,6,0,0,1,8.48-8.48L96,191.51,219.76,67.76a6,6,0,0,1,8.48,8.48Z"],
    sun: ["M122,40V16a6,6,0,0,1,12,0V40a6,6,0,0,1-12,0Zm68,88a62,62,0,1,1-62-62A62.07,62.07,0,0,1,190,128Zm-12,0a50,50,0,1,0-50,50A50.06,50.06,0,0,0,178,128ZM59.76,68.24a6,6,0,1,0,8.48-8.48l-16-16a6,6,0,0,0-8.48,8.48Zm0,119.52-16,16a6,6,0,1,0,8.48,8.48l16-16a6,6,0,1,0-8.48-8.48ZM192,70a6,6,0,0,0,4.24-1.76l16-16a6,6,0,0,0-8.48-8.48l-16,16A6,6,0,0,0,192,70Zm4.24,117.76a6,6,0,0,0-8.48,8.48l16,16a6,6,0,0,0,8.48-8.48ZM46,128a6,6,0,0,0-6-6H16a6,6,0,0,0,0,12H40A6,6,0,0,0,46,128Zm82,82a6,6,0,0,0-6,6v24a6,6,0,0,0,12,0V216A6,6,0,0,0,128,210Zm112-88H216a6,6,0,0,0,0,12h24a6,6,0,0,0,0-12Z"],
    moon: ["M232.13,143.64a6,6,0,0,0-6-1.49A90.07,90.07,0,0,1,113.86,29.85a6,6,0,0,0-7.49-7.48A102.88,102.88,0,0,0,54.48,58.68,102,102,0,0,0,197.32,201.52a102.88,102.88,0,0,0,36.31-51.89A6,6,0,0,0,232.13,143.64Zm-42,48.29a90,90,0,0,1-126-126A90.9,90.9,0,0,1,99.65,37.66,102.06,102.06,0,0,0,218.34,156.35,90.9,90.9,0,0,1,190.1,191.93Z"],
    contrast: ["M128,26A102,102,0,1,0,230,128,102.12,102.12,0,0,0,128,26Zm6,12.2a89.86,89.86,0,0,1,20,3.63V214.17a89.86,89.86,0,0,1-20,3.63Zm32,8.23a90.48,90.48,0,0,1,20,12.81V196.76a90.48,90.48,0,0,1-20,12.81ZM38,128a90.12,90.12,0,0,1,84-89.8V217.8A90.12,90.12,0,0,1,38,128Zm160,56.5V71.5a89.81,89.81,0,0,1,0,113Z"],
    plus: ["M222,128a6,6,0,0,1-6,6H134v82a6,6,0,0,1-12,0V134H40a6,6,0,0,1,0-12h82V40a6,6,0,0,1,12,0v82h82A6,6,0,0,1,222,128Z"],
    minus: ["M222,128a6,6,0,0,1-6,6H40a6,6,0,0,1,0-12H216A6,6,0,0,1,222,128Z"],
    fit: ["M214,48V88a6,6,0,0,1-12,0V54H168a6,6,0,0,1,0-12h40A6,6,0,0,1,214,48ZM88,202H54V168a6,6,0,0,0-12,0v40a6,6,0,0,0,6,6H88a6,6,0,0,0,0-12Zm120-40a6,6,0,0,0-6,6v34H168a6,6,0,0,0,0,12h40a6,6,0,0,0,6-6V168A6,6,0,0,0,208,162ZM88,42H48a6,6,0,0,0-6,6V88a6,6,0,0,0,12,0V54H88a6,6,0,0,0,0-12Z"],
    arrowUpRight: ["M198,64V168a6,6,0,0,1-12,0V78.48L68.24,196.24a6,6,0,0,1-8.48-8.48L177.52,70H88a6,6,0,0,1,0-12H192A6,6,0,0,1,198,64Z"],
    arrowRight: ["M220.24,132.24l-72,72a6,6,0,0,1-8.48-8.48L201.51,134H40a6,6,0,0,1,0-12H201.51L139.76,60.24a6,6,0,0,1,8.48-8.48l72,72A6,6,0,0,1,220.24,132.24Z"],
    caretRight: ["M180.24,132.24l-80,80a6,6,0,0,1-8.48-8.48L167.51,128,91.76,52.24a6,6,0,0,1,8.48-8.48l80,80A6,6,0,0,1,180.24,132.24Z"],
    caretDown: ["M212.24,100.24l-80,80a6,6,0,0,1-8.48,0l-80-80a6,6,0,0,1,8.48-8.48L128,167.51l75.76-75.75a6,6,0,0,1,8.48,8.48Z"],
    warning: ["M235.07,189.09,147.61,37.22h0a22.75,22.75,0,0,0-39.22,0L20.93,189.09a21.53,21.53,0,0,0,0,21.72A22.35,22.35,0,0,0,40.55,222h174.9a22.35,22.35,0,0,0,19.6-11.19A21.53,21.53,0,0,0,235.07,189.09ZM224.66,204.8a10.46,10.46,0,0,1-9.21,5.2H40.55a10.46,10.46,0,0,1-9.21-5.2,9.51,9.51,0,0,1,0-9.72L118.79,43.21a10.75,10.75,0,0,1,18.42,0l87.46,151.87A9.51,9.51,0,0,1,224.66,204.8ZM122,144V104a6,6,0,0,1,12,0v40a6,6,0,0,1-12,0Zm16,36a10,10,0,1,1-10-10A10,10,0,0,1,138,180Z"],
    octagon: ["M122,136V80a6,6,0,0,1,12,0v56a6,6,0,0,1-12,0ZM230,91.55v72.9a13.92,13.92,0,0,1-4.1,9.9L174.35,225.9a13.92,13.92,0,0,1-9.9,4.1H91.55a13.92,13.92,0,0,1-9.9-4.1L30.1,174.35a13.92,13.92,0,0,1-4.1-9.9V91.55a13.92,13.92,0,0,1,4.1-9.9L81.65,30.1a13.92,13.92,0,0,1,9.9-4.1h72.9a13.92,13.92,0,0,1,9.9,4.1L225.9,81.65A13.92,13.92,0,0,1,230,91.55Zm-12,0a2,2,0,0,0-.59-1.42L165.87,38.59a2,2,0,0,0-1.42-.59H91.55a2,2,0,0,0-1.41.59L38.58,90.13A2,2,0,0,0,38,91.55v72.9a2,2,0,0,0,.59,1.42l51.54,51.54a2,2,0,0,0,1.42.59h72.9a2,2,0,0,0,1.41-.59l51.56-51.54a2,2,0,0,0,.58-1.42ZM128,162a10,10,0,1,0,10,10A10,10,0,0,0,128,162Z"],
    close: ["M204.24,195.76a6,6,0,1,1-8.48,8.48L128,136.49,60.24,204.24a6,6,0,0,1-8.48-8.48L119.51,128,51.76,60.24a6,6,0,0,1,8.48-8.48L128,119.51l67.76-67.75a6,6,0,0,1,8.48,8.48L136.49,128Z"],
    list: ["M82,64a6,6,0,0,1,6-6H216a6,6,0,0,1,0,12H88A6,6,0,0,1,82,64Zm134,58H88a6,6,0,0,0,0,12H216a6,6,0,0,0,0-12Zm0,64H88a6,6,0,0,0,0,12H216a6,6,0,0,0,0-12ZM44,54A10,10,0,1,0,54,64,10,10,0,0,0,44,54Zm0,128a10,10,0,1,0,10,10A10,10,0,0,0,44,182Zm0-64a10,10,0,1,0,10,10A10,10,0,0,0,44,118Z"],
    graph: ["M200,154a29.87,29.87,0,0,0-19.5,7.23L154.88,141.3A29.83,29.83,0,0,0,158,128a30.52,30.52,0,0,0-.22-3.6L174,119a30,30,0,1,0-4-15,30.52,30.52,0,0,0,.22,3.6L154,113a29.91,29.91,0,0,0-32.42-14.31l-8.14-18.3a30,30,0,1,0-11,4.88l8.14,18.3A29.92,29.92,0,0,0,102.06,143L74,168a30.08,30.08,0,1,0,8,9L110,152a29.91,29.91,0,0,0,37.47-1.23l25.62,19.93A30,30,0,1,0,200,154Zm0-68a18,18,0,1,1-18,18A18,18,0,0,1,200,86ZM78,56A18,18,0,1,1,96,74,18,18,0,0,1,78,56ZM56,210a18,18,0,1,1,18-18A18,18,0,0,1,56,210Zm72-64a18,18,0,1,1,18-18A18,18,0,0,1,128,146Zm72,56a18,18,0,1,1,18-18A18,18,0,0,1,200,202Z"],
    tree: ["M160,110h48a14,14,0,0,0,14-14V48a14,14,0,0,0-14-14H160a14,14,0,0,0-14,14V66H128a22,22,0,0,0-22,22v34H70V112A14,14,0,0,0,56,98H24a14,14,0,0,0-14,14v32a14,14,0,0,0,14,14H56a14,14,0,0,0,14-14V134h36v34a22,22,0,0,0,22,22h18v18a14,14,0,0,0,14,14h48a14,14,0,0,0,14-14V160a14,14,0,0,0-14-14H160a14,14,0,0,0-14,14v18H128a10,10,0,0,1-10-10V88a10,10,0,0,1,10-10h18V96A14,14,0,0,0,160,110ZM58,144a2,2,0,0,1-2,2H24a2,2,0,0,1-2-2V112a2,2,0,0,1,2-2H56a2,2,0,0,1,2,2Zm100,16a2,2,0,0,1,2-2h48a2,2,0,0,1,2,2v48a2,2,0,0,1-2,2H160a2,2,0,0,1-2-2Zm0-112a2,2,0,0,1,2-2h48a2,2,0,0,1,2,2V96a2,2,0,0,1-2,2H160a2,2,0,0,1-2-2Z"],
    shield: ["M122,136V96a6,6,0,0,1,12,0v40a6,6,0,0,1-12,0Zm6,26a10,10,0,1,0,10,10A10,10,0,0,0,128,162ZM222,56v56c0,51.94-25.12,83.4-46.2,100.64-22.73,18.6-45.27,24.89-46.22,25.15a6,6,0,0,1-3.16,0c-1-.26-23.49-6.55-46.22-25.15C59.12,195.4,34,163.94,34,112V56A14,14,0,0,1,48,42H208A14,14,0,0,1,222,56Zm-12,0a2,2,0,0,0-2-2H48a2,2,0,0,0-2,2v56c0,37.75,13.94,68.39,41.44,91.06A130.94,130.94,0,0,0,128,225.72a131.17,131.17,0,0,0,40.56-22.66C196.06,180.39,210,149.75,210,112Z"],
    drift: ["M222,48V96a6,6,0,0,1-6,6H168a6,6,0,0,1,0-12h33.52L183.47,72a81.51,81.51,0,0,0-57.53-24h-.46A81.5,81.5,0,0,0,68.19,71.28a6,6,0,1,1-8.38-8.58,93.38,93.38,0,0,1,65.67-26.76H126a93.45,93.45,0,0,1,66,27.53l18,18V48a6,6,0,0,1,12,0ZM187.81,184.72a81.5,81.5,0,0,1-57.29,23.34h-.46a81.51,81.51,0,0,1-57.53-24L54.48,166H88a6,6,0,0,0,0-12H40a6,6,0,0,0-6,6v48a6,6,0,0,0,12,0V174.48l18,18.05a93.45,93.45,0,0,0,66,27.53h.52a93.38,93.38,0,0,0,65.67-26.76,6,6,0,1,0-8.38-8.58Z"],
    outputs: ["M118,216a6,6,0,0,1-6,6H48a6,6,0,0,1-6-6V40a6,6,0,0,1,6-6h64a6,6,0,0,1,0,12H54V210h58A6,6,0,0,1,118,216Zm110.24-92.24-40-40a6,6,0,0,0-8.48,8.48L209.51,122H112a6,6,0,0,0,0,12h97.51l-29.75,29.76a6,6,0,1,0,8.48,8.48l40-40A6,6,0,0,0,228.24,123.76Z"],
    keyboard: ["M224,50H32A14,14,0,0,0,18,64V192a14,14,0,0,0,14,14H224a14,14,0,0,0,14-14V64A14,14,0,0,0,224,50Zm2,142a2,2,0,0,1-2,2H32a2,2,0,0,1-2-2V64a2,2,0,0,1,2-2H224a2,2,0,0,1,2,2Zm-20-64a6,6,0,0,1-6,6H56a6,6,0,0,1,0-12H200A6,6,0,0,1,206,128Zm0-32a6,6,0,0,1-6,6H56a6,6,0,0,1,0-12H200A6,6,0,0,1,206,96ZM70,160a6,6,0,0,1-6,6H56a6,6,0,0,1,0-12h8A6,6,0,0,1,70,160Zm96,0a6,6,0,0,1-6,6H96a6,6,0,0,1,0-12h64A6,6,0,0,1,166,160Zm40,0a6,6,0,0,1-6,6h-8a6,6,0,0,1,0-12h8A6,6,0,0,1,206,160Z"],
    info: ["M142,176a6,6,0,0,1-6,6,14,14,0,0,1-14-14V128a2,2,0,0,0-2-2,6,6,0,0,1,0-12,14,14,0,0,1,14,14v40a2,2,0,0,0,2,2A6,6,0,0,1,142,176ZM124,94a10,10,0,1,0-10-10A10,10,0,0,0,124,94Zm106,34A102,102,0,1,1,128,26,102.12,102.12,0,0,1,230,128Zm-12,0a90,90,0,1,0-90,90A90.1,90.1,0,0,0,218,128Z"],
    diff: ["M112,154a6,6,0,0,0-6,6v33.52L64.93,152.44A9.93,9.93,0,0,1,62,145.37v-52a30,30,0,1,0-12,0v52a21.88,21.88,0,0,0,6.44,15.56L97.52,202H64a6,6,0,0,0,0,12h48a6,6,0,0,0,6-6V160A6,6,0,0,0,112,154ZM38,64A18,18,0,1,1,56,82,18,18,0,0,1,38,64Zm168,98.6v-52a21.88,21.88,0,0,0-6.44-15.56L158.48,54H192a6,6,0,0,0,0-12H144a6,6,0,0,0-6,6V96a6,6,0,0,0,12,0V62.48l41.07,41.08a9.93,9.93,0,0,1,2.93,7.07v52a30,30,0,1,0,12,0ZM200,210a18,18,0,1,1,18-18A18,18,0,0,1,200,210Z"],
    cube: ["M222.72,67.9l-88-48.17a13.9,13.9,0,0,0-13.44,0l-88,48.18A14,14,0,0,0,26,80.18v95.64a14,14,0,0,0,7.28,12.27l88,48.18a13.92,13.92,0,0,0,13.44,0l88-48.18A14,14,0,0,0,230,175.82V80.18A14,14,0,0,0,222.72,67.9ZM127,30.25a2,2,0,0,1,1.92,0L212.51,76,128,122.24,43.49,76ZM39,177.57a2,2,0,0,1-1-1.75V86.66l84,46V223Zm177.92,0L134,223V132.64l84-46v89.16A2,2,0,0,1,217,177.57Z"],
    stack: ["M229.18,173a6,6,0,0,1-2.16,8.2l-96,56a6,6,0,0,1-6,0l-96-56a6,6,0,0,1,6-10.36l93,54.23,93-54.23A6,6,0,0,1,229.18,173ZM221,122.82l-93,54.23L35,122.82a6,6,0,0,0-6,10.36l96,56a6,6,0,0,0,6,0l96-56a6,6,0,0,0-6-10.36ZM26,80a6,6,0,0,1,3-5.18l96-56a6,6,0,0,1,6,0l96,56a6,6,0,0,1,0,10.36l-96,56a6,6,0,0,1-6,0l-96-56A6,6,0,0,1,26,80Zm17.91,0L128,129.05,212.09,80,128,31Z"],
    command: ["M180,146H158V110h22a34,34,0,1,0-34-34V98H110V76a34,34,0,1,0-34,34H98v36H76a34,34,0,1,0,34,34V158h36v22a34,34,0,1,0,34-34ZM158,76a22,22,0,1,1,22,22H158ZM54,76a22,22,0,0,1,44,0V98H76A22,22,0,0,1,54,76ZM98,180a22,22,0,1,1-22-22H98Zm12-70h36v36H110Zm70,92a22,22,0,0,1-22-22V158h22a22,22,0,0,1,0,44Z"],
  };

  function icon(name, cls) {
    var el = s("svg", { viewBox: "0 0 256 256", class: "i" + (cls ? " " + cls : ""), "aria-hidden": "true", focusable: "false" });
    (ICONS[name] || []).forEach(function (d) { el.appendChild(s("path", { d: d })); });
    return el;
  }

  // The mark: soundings closing in on a point.
  function brandMark() {
    return s("svg", { viewBox: "0 0 24 24", "aria-hidden": "true" },
      s("circle", { cx: "12", cy: "12", r: "10.35", fill: "none", stroke: "currentColor", "stroke-opacity": "0.22", "stroke-width": "1.3" }),
      s("circle", { cx: "12", cy: "12", r: "6.6", fill: "none", stroke: "currentColor", "stroke-opacity": "0.5", "stroke-width": "1.3" }),
      s("circle", { cx: "12", cy: "12", r: "2.75", fill: "currentColor" }));
  }

  // The tab's icon is the mark, in ink that suits the browser's theme. Under
  // interragate watch it carries a dot for the state: amber while a plan
  // runs, pink when the last one failed.
  function setFavicon(dot) {
    var svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">' +
      "<style>.m{fill:none;stroke:#121211}.d{fill:#121211}@media (prefers-color-scheme:dark){.m{stroke:#ecebe7}.d{fill:#ecebe7}}</style>" +
      '<circle class="m" cx="12" cy="12" r="10.2" stroke-width="1.7" stroke-opacity=".35"/>' +
      '<circle class="m" cx="12" cy="12" r="6.5" stroke-width="1.7" stroke-opacity=".7"/>' +
      '<circle class="d" cx="12" cy="12" r="3"/>' +
      (dot ? '<circle cx="19" cy="5" r="4.6" fill="' + dot + '"/>' : "") + "</svg>";
    var link = document.querySelector('link[rel="icon"]');
    if (!link) { link = document.createElement("link"); link.rel = "icon"; document.head.appendChild(link); }
    link.href = "data:image/svg+xml," + encodeURIComponent(svg);
  }

  function plural(n, one, many) { return n === 1 ? "1 " + one : n + " " + (many || one + "s"); }
  // A lone finding needs no count in its heading; several do.
  function findingsTitle(n) { return n === 1 ? "Finding" : plural(n, "finding"); }

  function actionKind(a) {
    if (a === "create" || a === "update" || a === "replace" || a === "delete") return a;
    return "other";
  }

  // Terraform's own notation, as the CLI prints it. A replacement says
  // which half happens first: -/+ destroys then creates, +/- the reverse.
  var GLYPH = { create: "+", update: "~", replace: "-/+", delete: "−", read: "<=", forget: "×", "no-op": "·", move: "→", import: "↓" };

  function changeGlyph(c) {
    if (c.importing && (c.action === "create" || c.action === "no-op")) return "↓";
    if (c.action === "no-op" && c.moved) return "→";
    if (c.action === "replace" && c.order === "create-first") return "+/-";
    return GLYPH[c.action] || "·";
  }

  function label(c) {
    switch (c.action) {
      case "create": return c.importing ? "import" : "create";
      case "update": return c.importing ? "import + update" : "update";
      case "replace": return c.importing ? "import + replace" : "replace";
      case "delete": return "destroy";
      case "read": return "read";
      case "forget": return "forget";
      case "no-op": return c.importing ? "import" : c.moved ? "move" : "no change";
    }
    return c.action;
  }

  // In text, an action is its symbol in the CLI's colour.
  function actMark(action, glyph, title) {
    return h("span", { class: "sym " + actionKind(action), title: title, "aria-label": title, role: "img" }, glyph);
  }

  // The map draws actions as filled cells; its legend repeats them.
  function tileMark(action, glyph, title) {
    var kind = actionKind(action);
    return h("span", { class: "tile " + (kind !== "other" ? kind : action === "no-op" ? "noop" : ""), title: title, "aria-label": title, role: "img" }, glyph);
  }

  function cap(s) { return s ? s.charAt(0).toUpperCase() + s.slice(1) : s; }

  function changeMark(c) { return actMark(c.action, changeGlyph(c), label(c)); }

  function sevColor(sev) {
    return "var(--sev-" + (sev === "critical" || sev === "high" || sev === "medium" ? sev : "low") + ")";
  }

  // Status shapes: octagon, triangle, diamond, dot. Colour, shape and label
  // together, so severity never rests on colour alone.
  function sevIcon(sev) {
    var color = sevColor(sev);
    switch (sev) {
      case "critical":
        return s("svg", { viewBox: "0 0 16 16", "aria-hidden": "true" }, s("path", { d: "M5.2 1h5.6L15 5.2v5.6L10.8 15H5.2L1 10.8V5.2z", style: "fill:" + color }), s("path", { d: "M8 4.4v4.4", stroke: "#fff", "stroke-width": "1.8", "stroke-linecap": "round" }), s("circle", { cx: "8", cy: "11.4", r: "1.05", fill: "#fff" }));
      case "high":
        return s("svg", { viewBox: "0 0 16 16", "aria-hidden": "true" }, s("path", { d: "M8 1.3 15.2 14H.8z", style: "fill:" + color }), s("path", { d: "M8 6v3.6", stroke: "#0b0b0c", "stroke-width": "1.7", "stroke-linecap": "round" }), s("circle", { cx: "8", cy: "11.6", r: "0.95", fill: "#0b0b0c" }));
      case "medium":
        return s("svg", { viewBox: "0 0 16 16", "aria-hidden": "true" }, s("path", { d: "M8 1.2 14.8 8 8 14.8 1.2 8z", style: "fill:" + color }));
      case "low":
        return s("svg", { viewBox: "0 0 16 16", "aria-hidden": "true" }, s("circle", { cx: "8", cy: "8", r: "4.6", style: "fill:" + color }));
      case "none":
        return s("svg", { viewBox: "0 0 16 16", "aria-hidden": "true" }, s("circle", { cx: "8", cy: "8", r: "6.5", style: "fill: var(--good)" }), s("path", { d: "m5 8.2 2 2 4-4.2", stroke: "#fff", "stroke-width": "1.7", fill: "none", "stroke-linecap": "round", "stroke-linejoin": "round" }));
      default:
        return s("svg", { viewBox: "0 0 16 16", "aria-hidden": "true" }, s("circle", { cx: "8", cy: "8", r: "4.4", fill: "none", style: "stroke: var(--muted)", "stroke-width": "1.5" }));
    }
  }

  function sevMark(sev, bare) {
    return h("span", { class: "sev" + (bare ? " bare" : "") }, sevIcon(sev), SEV_LABEL[sev] || sev);
  }

  function riskLabel(level) {
    if (level === "none") return "No changes";
    if (level === "info") return "Low risk";
    return (SEV_LABEL[level] || level) + " risk";
  }

  // Rich text: `code` spans become links when they name something in the plan.
  function rich(text) {
    var frag = document.createDocumentFragment();
    if (!text) return frag;
    var parts = String(text).split("`");
    parts.forEach(function (part, i) {
      if (i % 2 === 0 || i === parts.length - 1) {
        linkify(frag, i % 2 === 0 ? part : "`" + part);
        return;
      }
      frag.appendChild(refFor(part));
    });
    return frag;
  }

  // linkify writes plain text, with web addresses (a scanner's page about
  // its check, say) as links that open elsewhere.
  // Links go only to documentation: the scanners' and the providers' own
  // pages. Text in a report can carry values from the plan, and a value is
  // no place to take a reviewer's click.
  var DOC_HOSTS = /^(docs\.aws\.amazon\.com|registry\.terraform\.io|search\.opentofu\.org|avd\.aquasec\.com|docs\.prismacloud\.io|www\.checkov\.io|github\.com\/terraform-linters|cloud\.google\.com|learn\.microsoft\.com|developer\.hashicorp\.com)(\/|$)/;

  function linkify(frag, text) {
    var re = /https:\/\/[^\s<>"]+[^\s<>".,;:)]/g, last = 0, m;
    while ((m = re.exec(text))) {
      var rest = m[0].replace(/^https:\/\//, "");
      if (!DOC_HOSTS.test(rest)) continue;
      if (m.index > last) frag.appendChild(document.createTextNode(text.slice(last, m.index)));
      frag.appendChild(h("a", { class: "doc-link", href: m[0], target: "_blank", rel: "noopener noreferrer" }, rest));
      last = m.index + m[0].length;
    }
    if (last < text.length) frag.appendChild(document.createTextNode(text.slice(last)));
  }

  function refFor(text) {
    var target = resolve(text);
    if (!target) return h("code", { class: "plain" }, text);
    return h("button", {
      class: "ref", type: "button", title: "Show " + text,
      onclick: function (e) { e.stopPropagation(); go(target); },
    }, text);
  }

  // resolve maps text to something selectable: a change, a resource, an output.
  function resolve(text) {
    if (byAddr.has(text)) return { kind: "change", id: text };
    if (byResource.has(text)) {
      var inst = byResource.get(text);
      return inst.length === 1 ? { kind: "change", id: inst[0].address } : { kind: "resource", id: text };
    }
    if (nodeById.has(text)) return { kind: "resource", id: text };
    if (text.indexOf("output.") === 0 && outputNames.has(text.slice(7))) return { kind: "output", id: text.slice(7) };
    return null;
  }

  function copyText(text, button) {
    function done(ok) {
      var labelEl = button.querySelector(".copy-label");
      button.classList.toggle("done", ok);
      button.replaceChild(icon(ok ? "check" : "close"), button.querySelector("svg"));
      if (labelEl) labelEl.textContent = ok ? "Copied" : "Copy failed";
      clearTimeout(button.__reset);
      button.__reset = setTimeout(function () {
        button.classList.remove("done");
        button.replaceChild(icon("copy"), button.querySelector("svg"));
        if (labelEl) labelEl.textContent = labelEl.getAttribute("data-label") || "Copy";
      }, 1600);
    }
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(fallbackCopy(text)); });
    } else {
      done(fallbackCopy(text));
    }
  }

  // copyQuietly copies from somewhere with no button to show it worked, such
  // as Go to, and says so in a note that fades.
  var noteEl = null;
  function copyQuietly(text, said) {
    function show(ok) {
      if (!noteEl) { noteEl = h("div", { class: "copied-note", role: "status" }); document.body.appendChild(noteEl); }
      noteEl.textContent = ok ? said : "Could not copy: the browser refused.";
      noteEl.classList.add("show");
      clearTimeout(noteEl.__hide);
      noteEl.__hide = setTimeout(function () { noteEl.classList.remove("show"); }, 2200);
    }
    if (navigator.clipboard && window.isSecureContext) navigator.clipboard.writeText(text).then(function () { show(true); }, function () { show(fallbackCopy(text)); });
    else show(fallbackCopy(text));
  }

  function fallbackCopy(text) {
    var ta = h("textarea", { style: "position:fixed;top:0;left:0;opacity:0", "aria-hidden": "true" });
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
    ta.remove();
    return ok;
  }

  function copyButton(text, what, iconOnly, labelText) {
    var b = h("button", { class: "copy" + (iconOnly ? " icon-only" : ""), type: "button", title: "Copy " + what, "aria-label": "Copy " + what },
      icon("copy"), iconOnly ? null : h("span", { class: "copy-label", "data-label": labelText || "Copy" }, labelText || "Copy"));
    b.addEventListener("click", function (e) { e.stopPropagation(); copyText(text, b); });
    return b;
  }

  function localName(c) {
    if (!c.module) return c.address;
    return c.address.slice(c.module.length + 1);
  }

  function isCosmetic(c) { return !!c.cosmetic; }
  function isUnchanged(c) { return c.action === "no-op" && !c.moved && !c.importing; }

  // Cut long labels in the middle, so the distinctive end survives:
  // aws_iam_role_po…attachment rather than aws_iam_role_policy_at….
  function truncateMiddle(str, n) {
    if (str.length <= n) return str;
    var keep = n - 1, head = Math.ceil(keep * 0.55);
    return str.slice(0, head) + "…" + str.slice(str.length - (keep - head));
  }

  function firstSentence(t) {
    var inCode = false;
    for (var i = 0; i < t.length; i++) {
      if (t[i] === "`") inCode = !inCode;
      else if (t[i] === "." && !inCode && (i + 1 === t.length || t[i + 1] === " ")) return t.slice(0, i + 1);
    }
    return t;
  }

  function changedCount() { return changes.filter(function (c) { return !isUnchanged(c); }).length; }

  // ---------------------------------------------------------- entry reveal

  // Threshold 0: the change list can be far taller than the viewport, so a
  // ratio would never be reached.
  var revealer = ("IntersectionObserver" in window) && !reducedMotion() ? new IntersectionObserver(function (entries) {
    entries.forEach(function (e) {
      if (!e.isIntersecting) return;
      e.target.classList.add("is-in");
      revealer.unobserve(e.target);
    });
  }, { rootMargin: "0px 0px -6% 0px", threshold: 0 }) : null;

  function reveal(root) {
    root.querySelectorAll("[data-reveal]").forEach(function (el) {
      if (revealer) revealer.observe(el);
      else el.classList.add("is-in");
    });
  }

  function revealAttrs(i) { return { "data-reveal": "", style: "--i:" + i }; }

  // ------------------------------------------------------------------ theme

  var PAGE_COLOR = { light: "#f7f6f3", dark: "#0f0f0e" };

  function themeSetting() { return document.documentElement.getAttribute("data-theme") || "system"; }

  // The browser chrome follows the page, including a theme chosen here.
  function syncThemeColor() {
    var setting = themeSetting();
    document.querySelectorAll('meta[name="theme-color"]').forEach(function (m) {
      var scheme = (m.getAttribute("media") || "").indexOf("dark") >= 0 ? "dark" : "light";
      m.setAttribute("content", PAGE_COLOR[setting === "system" ? scheme : setting]);
    });
  }

  // A light switch: the change is instant, because nobody wants to wait
  // for their own preference.
  function themeButton() {
    var b = h("button", { class: "icon-btn", type: "button" });
    var names = { system: "System theme", light: "Light theme", dark: "Dark theme" };
    var order = ["system", "light", "dark"];
    function paint() {
      var current = themeSetting();
      var next = order[(order.indexOf(current) + 1) % order.length];
      b.textContent = "";
      b.appendChild(icon(current === "light" ? "sun" : current === "dark" ? "moon" : "contrast"));
      b.title = names[current] + ". Switch to " + names[next].toLowerCase();
      b.setAttribute("aria-label", b.title);
    }
    b.addEventListener("click", function () {
      var next = order[(order.indexOf(themeSetting()) + 1) % order.length];
      try { localStorage.setItem("interragate-theme", next); } catch (e) { /* file:// can block storage */ }
      if (next === "system") document.documentElement.removeAttribute("data-theme");
      else document.documentElement.setAttribute("data-theme", next);
      syncThemeColor();
      paint();
    });
    paint();
    return b;
  }

  // ------------------------------------------------------------------- bar

  var TABS = [
    { id: "changes", label: "Changes", icon: "list", title: "Changes",
      count: function () { return changedCount(); },
      blurb: "Every resource the plan touches. Select one to see why it changes, what it affects and its full diff." },
    { id: "map", label: "Map", icon: "graph", title: "Dependency map",
      count: function () { return 0; },
      blurb: "Resources laid out by dependency depth, in lanes. Select one to light up what it depends on and everything that depends on it." },
    { id: "apply", label: "Apply", icon: "graph", title: "How the apply runs",
      count: function () { return 0; },
      blurb: "The order " + TOOL + " works through the changes in, ten at a time, with typical times for slow resources. A sketch of the apply, not a promise." },
    { id: "findings", label: "Findings", icon: "shield", title: "Findings",
      count: function () { return findings.length; },
      blurb: "What this change introduces that deserves a second look, with fixes you can paste." },
    { id: "drift", label: "Drift", icon: "drift", title: "Drift",
      count: function () { return drift.filter(function (d) { return !d.noise; }).length; },
      blurb: "Changes made outside " + TOOL + ", and whether this plan keeps or reverts them." },
    { id: "outputs", label: "Outputs", icon: "outputs", title: "Outputs",
      count: function () { return outputs.filter(function (o) { return o.action !== "no-op"; }).length; },
      blurb: "Root module outputs whose values change." },
  ];

  // The apply view exists only when the plan has something to apply.
  if (!(data.apply && data.apply.steps && data.apply.steps.length)) TABS = TABS.filter(function (t) { return t.id !== "apply"; });

  function tabById(id) { return TABS.filter(function (t) { return t.id === id; })[0]; }

  var tabButtons = new Map(), tabTrack = null;

  function topBar() {
    var bar = h("header", { class: "bar" });
    var inner = h("div", { class: "bar-inner" });
    inner.appendChild(h("a", {
      class: "brand", href: "#", title: "Back to the top", "aria-label": "interragate: back to the top",
      onclick: function (e) { e.preventDefault(); window.scrollTo({ top: 0, behavior: reducedMotion() ? "auto" : "smooth" }); },
    }, brandMark(), h("span", null, "interragate")));
    tabTrack = h("nav", { class: "views", role: "tablist", "aria-label": "Views" });
    TABS.forEach(function (t, i) {
      var n = t.count();
      var b = h("button", {
        class: "view", role: "tab", type: "button", id: "tab-" + t.id, "aria-controls": "panel",
        title: t.label + " (" + (i + 1) + ")", "aria-label": t.label + (n ? ", " + n : ""),
        onclick: function () { setTab(t.id, true); },
      }, h("span", { class: "label", "data-label": t.label }, t.label), n ? h("span", { class: "count" }, String(n)) : null);
      tabButtons.set(t.id, b);
      tabTrack.appendChild(b);
    });
    tabTrack.addEventListener("keydown", function (e) {
      var keys = { ArrowRight: 1, ArrowLeft: -1, Home: -99, End: 99 };
      if (!(e.key in keys)) return;
      e.preventDefault();
      var i = TABS.indexOf(tabById(state.tab));
      var next = Math.max(0, Math.min(TABS.length - 1, keys[e.key] === 99 ? TABS.length - 1 : keys[e.key] === -99 ? 0 : (i + keys[e.key] + TABS.length) % TABS.length));
      setTab(TABS[next].id, false);
      tabButtons.get(TABS[next].id).focus();
    });
    inner.appendChild(tabTrack);
    inner.appendChild(h("div", { class: "bar-tools" },
      h("button", {
        class: "search-btn", type: "button", title: "Go to a resource, finding or view (" + (MAC ? "⌘K" : "Ctrl K") + ")", "aria-label": "Go to a resource, finding or view",
        onclick: function () { openPalette(); },
      }, icon("search"), h("span", { class: "label" }, "Go to"), h("kbd", null, MAC ? "⌘K" : "Ctrl K")),
      themeButton()));
    bar.appendChild(inner);
    return bar;
  }

  function syncTabs() {
    tabButtons.forEach(function (b, id) {
      b.setAttribute("aria-selected", id === state.tab ? "true" : "false");
      b.setAttribute("tabindex", id === state.tab ? "0" : "-1");
    });
  }

  // The bar only needs an edge while the page is scrolled beneath it.
  function watchBar(bar) {
    if (!("IntersectionObserver" in window)) return;
    var sentinel = h("div", { "aria-hidden": "true", style: "position:absolute;top:0;left:0;width:1px;height:1px" });
    document.body.prepend(sentinel);
    new IntersectionObserver(function (entries) { bar.classList.toggle("scrolled", !entries[0].isIntersecting); }).observe(sentinel);
  }

  // ------------------------------------------------------------------ hero

  function heroView() {
    var risk = data.risk || {};
    var level = risk.level || "none";
    var sec = h("section", { class: "hero", "aria-label": "Verdict" });
    sec.appendChild(h("p", Object.assign({ class: "risk" }, revealAttrs(0)), sevIcon(level), riskLabel(level)));
    sec.appendChild(h("h1", Object.assign({ class: "headline" }, revealAttrs(1)), rich(risk.headline)));
    var line = h("p", Object.assign({ class: "plan-line" }, revealAttrs(2)));
    planFacts().forEach(function (f) { line.appendChild(h("span", f.title ? { title: f.title } : null, f.text)); });
    sec.appendChild(line);
    var since = sinceBlock();
    if (since) sec.appendChild(since);
    var fig = inventory();
    if (fig) {
      fig.setAttribute("data-reveal", "");
      fig.style.setProperty("--i", "3");
      sec.appendChild(fig);
    }
    return sec;
  }

  // Where the plan came from, in one quiet line.
  function planFacts() {
    var facts = [];
    if (plan.source) facts.push({ text: plan.source, title: "Plan file" });
    if (plan.dir && plan.dir !== plan.source) facts.push({ text: plan.dir, title: "Working directory" });
    facts.push({ text: (TOOL + " " + (plan.version || "")).trim() });
    if (plan.workspace && plan.workspace !== "default") facts.push({ text: "workspace " + plan.workspace, title: "The " + TOOL + " workspace this plan was made in" });
    if (plan.timestamp) {
      var t = new Date(plan.timestamp);
      if (!isNaN(t)) facts.push({ text: "planned " + t.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }), title: plan.timestamp });
    }
    if (git && git.branch) facts.push({ text: "branch " + git.branch, title: "The git branch the configuration is on" });
    if (git && git.files.length) {
      facts.push({ text: plural(git.files.length, "file") + " edited " + EDIT_SINCE, title: git.files.join("\n") + "\n\nCompared with " + git.commit + (git.dirty ? ", uncommitted changes included" : "") });
    }
    return facts;
  }

  // The plan as a figure: one square per changed resource, a row per kind
  // of change, worst first, so the rows compare by length. Each square is
  // shaped by what happens to its resource: crossed out when destroyed,
  // half rebuilt when replaced, solid when updated or created, hollow when
  // an update only touches tags or formatting. Every square is a link.
  var INVENTORY = [
    { key: "delete", word: "destroyed" },
    { key: "replace", word: "replaced" },
    { key: "update", word: "updated" },
    { key: "create", word: "created" },
  ];

  function inventory() {
    var groups = { delete: [], replace: [], update: [], create: [], other: [] };
    changes.forEach(function (c) { if (!isUnchanged(c)) groups[actionKind(c.action)].push(c); });
    var total = changedCount();
    if (!total) return null;
    var size = total <= 60 ? 14 : total <= 200 ? 11 : total <= 600 ? 8 : 6;
    var fig = h("figure", { class: "inventory" + (size < 10 ? " small" : ""), style: "--u:" + size + "px" });
    var spoken = [];
    function row(key, labelEl, items, note) {
      items.sort(function (a, b) {
        var ca = isCosmetic(a) ? 1 : 0, cb = isCosmetic(b) ? 1 : 0;
        if (ca !== cb) return ca - cb;
        return a.address < b.address ? -1 : a.address > b.address ? 1 : 0;
      });
      var units = h("div", { class: "units" });
      items.forEach(function (c) {
        units.appendChild(h("button", {
          class: "unit " + key + (isCosmetic(c) ? " cosmetic" : ""), type: "button", tabindex: "-1", "aria-hidden": "true",
          title: label(c) + ": " + c.address,
          onclick: function () { go({ kind: "change", id: c.address }); },
        }));
      });
      if (note) units.appendChild(h("span", { class: "unit-note" }, note));
      fig.appendChild(labelEl);
      fig.appendChild(units);
    }
    INVENTORY.forEach(function (g) {
      var items = groups[g.key];
      if (!items.length) return;
      spoken.push(items.length + " " + g.word);
      var cosmetic = items.filter(isCosmetic).length;
      var note = g.key === "update" && cosmetic ? (cosmetic === items.length ? "all only touch tags or formatting" : cosmetic + " only touch tags or formatting") : null;
      row(g.key, h("button", { class: "inv-label", type: "button", title: "Show only these in the list", onclick: function () { onlyAction(g.key); } },
        h("b", null, String(items.length)), " " + g.word), items, note);
    });
    if (groups.other.length) {
      var kinds = {};
      groups.other.forEach(function (c) { var w = otherWord(c); kinds[w] = (kinds[w] || 0) + 1; });
      spoken.push(groups.other.length + " other");
      row("other", h("span", { class: "inv-label" }, h("b", null, String(groups.other.length)), " other"), groups.other,
        Object.keys(kinds).map(function (w) { return kinds[w] + " " + w; }).join(", "));
    }
    fig.appendChild(h("figcaption", { class: "visually-hidden" }, plural(total, "resource changes", "resources change") + ": " + spoken.join(", ") + "."));
    return fig;
  }

  function otherWord(c) {
    if (c.action === "read") return "read";
    if (c.action === "forget") return "forgotten";
    if (c.importing) return "imported";
    if (c.moved) return "moved";
    return c.action;
  }

  function onlyAction(key) {
    Object.keys(state.actions).forEach(function (k) { state.actions[k] = k === key; });
    if (state.tab !== "changes") setTab("changes", true);
    else { renderMain(); scrollToWorkspace(); }
  }

  // -------------------------------------------------------------- overview

  function textLink(text, onClick) {
    return h("button", { class: "text-link", type: "button", onclick: onClick }, h("span", null, text), icon("arrowRight"));
  }

  // What matters reads down the main column; the blast radius and the
  // ready-made fixes sit beside it, as figures beside a text.
  function overviewView() {
    var sec = h("section", { class: "overview", "aria-label": "Overview" });
    var main = h("div", Object.assign({ class: "ov-main" }, revealAttrs(4)));
    main.appendChild(h("h2", null, "What matters"));
    var items = data.narrative || [];
    if (!items.length) {
      main.appendChild(h("p", null, "Nothing in this plan needs a second look."));
    } else {
      var list = h("ol", { class: "matters" });
      items.slice(0, 8).forEach(function (it) {
        list.appendChild(h("li", { class: sevRank[it.severity] <= 1 ? "info" : null }, sevIcon(it.severity), h("span", null, rich(it.text))));
      });
      main.appendChild(list);
      var more = h("div", { class: "more" });
      if (findings.length) more.appendChild(textLink("Read all " + plural(findings.length, "finding"), function () { setTab("findings", true); }));
      if (prComment) more.appendChild(copyButton(prComment, "a summary for a pull request, in Markdown", false, "Copy for a pull request"));
      if (more.childNodes.length) main.appendChild(more);
    }
    var outside = consumersBlock();
    if (outside) main.appendChild(outside);
    sec.appendChild(main);
    var aside = h("div", Object.assign({ class: "ov-aside" }, revealAttrs(5)));
    var edits = editsBlock();
    if (edits) aside.appendChild(edits);
    var spot = spotlightTarget();
    if (spot) aside.appendChild(spotlightBlock(spot));
    var ap = applyBlock();
    if (ap) aside.appendChild(ap);
    var fixes = fixesBlock();
    if (fixes) aside.appendChild(fixes);
    if (aside.childNodes.length) sec.appendChild(aside);
    else sec.classList.add("single");
    return sec;
  }

  // What the plan does to each service outside Terraform that
  // .interragate.yaml declares, the worst first, then which it leaves
  // alone, which the configuration's settings leave out, and which it has
  // nothing for. interragate cannot see these services; it checks the plan
  // against what the file says they use.
  function consumersBlock() {
    var cs = data.consumers || [];
    if (!cs.length) return null;
    var sec = h("section", { class: "outside", "aria-labelledby": "outside-title" },
      h("h2", { id: "outside-title" }, "Outside Terraform"),
      h("p", { class: "outside-sub" }, "Services you declared, which interragate cannot see."));
    function worst(c) {
      return c.effects.reduce(function (w, e) { return Math.max(w, sevRank[e.severity] || 0); }, 0);
    }
    var affected = cs.filter(function (c) { return c.effects && c.effects.length; })
      .map(function (c, i) { return { c: c, i: i }; })
      .sort(function (a, b) { return worst(b.c) - worst(a.c) || a.i - b.i; })
      .map(function (x) { return x.c; });
    affected.forEach(function (c) {
      // The link comes from .interragate.yaml, which the team checks in,
      // not from the plan, so it is safe to follow.
      var name = /^https?:\/\//i.test(c.link || "")
        ? h("a", { class: "doc-link", href: c.link, target: "_blank", rel: "noopener noreferrer", title: "Where " + c.name + "'s settings are changed" }, c.name)
        : c.name;
      var meta = [c.platform].concat(c.owners || []).filter(Boolean).join(" · ");
      var list = h("ul", { class: "matters" });
      c.effects.forEach(function (e) {
        list.appendChild(h("li", { class: sevRank[e.severity] <= 1 ? "info" : null }, sevIcon(e.severity), h("span", null, rich(e.text))));
      });
      sec.appendChild(h("div", { class: "service" }, h("h3", null, name, meta ? h("span", { class: "meta" }, meta) : null), list));
    });
    function note(text, items, word) {
      var p = h("p", { class: "outside-note" }, text + ": ");
      items.forEach(function (it, i) {
        if (i) p.appendChild(document.createTextNode(word));
        append(p, it);
      });
      p.appendChild(document.createTextNode("."));
      sec.appendChild(p);
    }
    var names = function (list) { return list.map(function (c) { return c.name; }); };
    var unaffected = cs.filter(function (c) { return !(c.effects && c.effects.length) && !c.hidden && c.checked; });
    var hidden = cs.filter(function (c) { return !(c.effects && c.effects.length) && c.hidden; });
    if (unaffected.length) note("Not affected", names(unaffected), ", ");
    if (hidden.length) note("Left out by disable or ignore", names(hidden), ", ");
    var missing = cs.filter(function (c) { return c.missing && c.missing.length; }).map(function (c) {
      var uses = [c.name + " ("];
      c.missing.forEach(function (m, i) { uses.push(i ? ", " : null, h("code", { class: "plain" }, m)); });
      uses.push(")");
      return uses;
    });
    if (missing.length) note("Declared to use what this plan does not have", missing, "; ");
    return sec;
  }

  // How much of the plan your edits account for, when interragate compared
  // the configuration with git. The rest is what nobody asked for in this
  // branch, so it gets its own way in.
  function editsBlock() {
    if (!git) return null;
    var mine = 0, follow = 0, rest = 0, restShown = 0;
    changes.forEach(function (c) {
      if (isUnchanged(c)) return;
      var k = editClass(c);
      if (k === "edit") mine++;
      else if (k === "follows") follow++;
      else {
        rest++;
        if (!isCosmetic(c)) restShown++;
      }
    });
    if (!mine) return null;
    var files = git.files.length === 1 ? "`" + git.files[0] + "`" : plural(git.files.length, "file");
    var whose = git.base === "HEAD" ? "your uncommitted edits to " + files : "your edits to " + files + " since `" + git.base + "`";
    var text = cap(plural(mine, "change comes", "changes come")) + " from " + whose + (follow ? ", and " + plural(follow, "more follows", "more follow") + " from them." : ".");
    var block = h("div", { class: "aside-block" }, h("h2", null, "Your edits"), h("p", null, rich(text)));
    if (!rest) {
      block.appendChild(h("p", null, "Nothing else in the plan changes."));
      return block;
    }
    var why = "Drift, a provider upgrade or a change on the base may explain " + (rest === 1 ? "it" : "them") + ", so check " + (rest === 1 ? "it was" : "they were") + " meant.";
    var moved = (git.providers || []).filter(function (p) { return p.from && p.to; });
    if (moved.length) {
      why = andList(moved.map(function (p) { return "`" + p.source.split("/").pop() + "` " + p.from + " → " + p.to; }), 3) + (moved.length === 1 ? " is a provider upgrade" : " are provider upgrades") + " that may explain some.";
    }
    block.appendChild(h("p", null, rich(plural(rest, "change is", "changes are") + " not tied to an edit. " + cap(why))));
    block.appendChild(h("div", { class: "more" }, textLink("See what is not tied to an edit", function () {
      state.groupBy = "edit";
      state.collapsed = new Set();
      // When they only touch tags, the list hides them until asked.
      if (!restShown) state.cosmetic = true;
      if (state.tab === "changes") renderMain(); else setTab("changes", false);
      scrollToWorkspace();
    })));
    return block;
  }

  // How long the apply takes and what goes missing meanwhile, when the
  // simulation found something worth saying.
  function applyBlock() {
    var ap = data.apply;
    if (!ap || !ap.narrative || !ap.narrative.length) return null;
    var block = h("div", { class: "aside-block" }, h("h2", null, "The apply"));
    ap.narrative.forEach(function (sn) { block.appendChild(h("p", null, rich(sn.text))); });
    block.appendChild(h("div", { class: "more" }, textLink("See how the apply runs", function () { setTab("apply", true); })));
    return block;
  }

  // The resource most worth a look: the riskiest destroy or replace, ties
  // broken by how much depends on it.
  function spotlightTarget() {
    var best = null;
    changes.forEach(function (c) {
      if (c.action !== "delete" && c.action !== "replace") return;
      if (!nodeById.has(c.resource)) return;
      var score = sevRank[c.severity] * 100000 + transitive(c.resource, downs).size;
      if (!best || score > best.score) best = { change: c, score: score };
    });
    return best ? best.change : null;
  }

  function spotlightBlock(c) {
    var down = transitive(c.resource, downs).size;
    // A resource with no neighbours needs no figure; the sentence says it.
    var related = (ups.get(c.resource) || []).length + (downs.get(c.resource) || []).length > 0;
    return h("div", { class: "aside-block" },
      h("h2", null, "Blast radius"),
      h("p", null, c.action === "delete" ? "Destroying " : "Replacing ", refFor(c.address),
        down ? " affects " + plural(down, "resource") + " that " + (down === 1 ? "depends" : "depend") + " on it." : " affects nothing else in this configuration."),
      related ? h("div", { class: "spot-graph" }, spotGraph(c)) : null,
      related ? textLink("Open it in the map", function () { openInMap({ kind: "change", id: c.address }); }) : null);
  }

  // What it reads, the resource, what reads it: a slice of the map, drawn
  // the same way.
  function spotGraph(c) {
    var upIds = (ups.get(c.resource) || []).slice().sort();
    var downIds = (downs.get(c.resource) || []).slice().sort();
    var maxRows = 4;
    var left = upIds.slice(0, maxRows), right = downIds.slice(0, maxRows);
    var W = 420, NW = 126, NH = 38, GAP = 10, PAD = 2, TOP = 28;
    var rows = Math.max(1, left.length, right.length);
    var stackH = rows * (NH + GAP) - GAP;
    var H = TOP + stackH + 26;
    var svg = s("svg", { class: "flow", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": c.address + " reads " + plural(upIds.length, "resource") + "; " + plural(downIds.length, "resource") + " read it directly" });
    function colY(n, i) { return TOP + (stackH - (n * (NH + GAP) - GAP)) / 2 + i * (NH + GAP); }
    var cx = (W - NW) / 2, cy = colY(1, 0);
    function node(id, x, y, subject) {
      var n = nodeById.get(id);
      var kind = n && nodeChanged(n) ? actionKind(n.action) : "other";
      var g = s("g", { transform: "translate(" + x + "," + y + ")" });
      if (subject) g.appendChild(s("path", { d: bracketPath(-5, -5, NW + 10, NH + 10, 8), style: "fill: none; stroke: var(--ink); stroke-width: 1.5" }));
      var stroke = kind === "delete" ? "var(--act-delete)" : subject ? "var(--ink)" : "var(--node-ring)";
      g.appendChild(s("rect", { x: 0.5, y: 0.5, width: NW - 1, height: NH - 1,
        style: "fill: var(--node); stroke: " + stroke + "; stroke-width: " + (subject ? 1.5 : 1) + (kind === "delete" ? "; stroke-dasharray: 5 3" : "") }));
      if (kind !== "other") g.appendChild(s("rect", { x: 0.5, y: 0.5, width: 25, height: NH - 1, style: "fill: var(--act-" + kind + ")" }));
      else g.appendChild(s("path", { d: "M26 0.5V" + (NH - 0.5), style: "stroke: var(--rule)" }));
      g.appendChild(s("text", { x: 13, y: NH / 2 + 4, "text-anchor": "middle", style: "font: 700 10.5px var(--mono); letter-spacing: -0.04em; fill: " + (kind === "other" ? "var(--muted)" : "var(--on-" + kind + ")") }, kind === "other" ? "·" : GLYPH[n.action] || "·"));
      var typ = n ? (n.mode === "data" ? "data." + n.type : n.type) : "";
      g.appendChild(s("text", { x: 34, y: 16, style: "font: 450 9.5px var(--mono); letter-spacing: -0.01em; fill: var(--muted)" }, truncateMiddle(typ, 15)));
      g.appendChild(s("text", { x: 34, y: 29.5, style: "font: 600 11.5px var(--sans); letter-spacing: -0.01em; fill: var(--ink)" }, truncateMiddle(n ? n.name : id, 13)));
      g.appendChild(s("title", null, id));
      return g;
    }
    var inX = (PAD + NW + cx) / 2, outX = (cx + NW + W - PAD - NW) / 2;
    left.forEach(function (id, i) { svg.appendChild(s("path", { class: "edge up", d: stepPath(PAD + NW, colY(left.length, i) + NH / 2, cx - 1, cy + NH / 2, inX) })); });
    right.forEach(function (id, i) { svg.appendChild(s("path", { class: "edge down", d: stepPath(cx + NW, cy + NH / 2, W - PAD - NW - 1, colY(right.length, i) + NH / 2, outX) })); });
    left.forEach(function (id, i) { svg.appendChild(node(id, PAD, colY(left.length, i), false)); });
    right.forEach(function (id, i) { svg.appendChild(node(id, W - PAD - NW, colY(right.length, i), false)); });
    svg.appendChild(node(c.resource, cx, cy, true));
    var note = "font: 400 12px var(--sans); fill: var(--muted)";
    svg.appendChild(s("text", { x: PAD, y: 14, style: note }, "Reads"));
    svg.appendChild(s("text", { x: W - PAD, y: 14, "text-anchor": "end", style: note }, "Read by"));
    if (upIds.length > left.length) svg.appendChild(s("text", { x: PAD, y: H - 6, style: note }, "and " + (upIds.length - left.length) + " more"));
    if (downIds.length > right.length) svg.appendChild(s("text", { x: W - PAD, y: H - 6, "text-anchor": "end", style: note }, "and " + (downIds.length - right.length) + " more"));
    if (!left.length) svg.appendChild(s("text", { x: PAD + NW / 2, y: cy + NH / 2 + 4, "text-anchor": "middle", style: note }, "nothing"));
    if (!right.length) svg.appendChild(s("text", { x: W - PAD - NW / 2, y: cy + NH / 2 + 4, "text-anchor": "middle", style: note }, "nothing"));
    return svg;
  }

  function fixesBlock() {
    var withSnippets = findings.filter(function (f) { return f.snippet; });
    if (!withSnippets.length) return null;
    var all = withSnippets.map(function (f) { return f.snippet; }).join("\n\n");
    var n = withSnippets.length;
    return h("div", { class: "aside-block" },
      h("h2", null, "Fixes, ready to paste"),
      h("p", null, plural(n, "fix", "fixes") + " written from the plan: " + fixKinds(withSnippets) + "."),
      h("pre", { class: "snippet-peek" }, withSnippets[0].snippet),
      h("div", { class: "more" }, copyButton(all, n === 1 ? "the fix" : "all " + n + " fixes", false, n === 1 ? "Copy the fix" : "Copy all " + n)));
  }

  function fixKinds(list) {
    var kinds = [];
    function add(k) { if (kinds.indexOf(k) < 0) kinds.push(k); }
    list.forEach(function (f) {
      var t = f.snippet;
      if (/^\s*moved\s*\{/m.test(t)) add("moved blocks");
      else if (/^\s*removed\s*\{/m.test(t)) add("removed blocks");
      else if (/^\s*import\s*\{/m.test(t)) add("import blocks");
      else if (/lifecycle\s*\{/.test(t)) add("lifecycle settings");
      else add("configuration");
    });
    return kinds.join(", ");
  }

  // ------------------------------------------------------------- workspace

  var wsHead, workspace, mainCol, detailCol;

  function headHint(id) {
    if (id === "changes") return h("div", { class: "head-hint" }, h("kbd", null, "j"), h("kbd", null, "k"), h("span", null, "move"), h("kbd", null, "/"), h("span", null, "filter"), h("kbd", null, "esc"), h("span", null, "clear"));
    if (id === "map") return h("div", { class: "head-hint" }, h("kbd", null, "f"), h("span", null, "fit"), h("kbd", null, "+"), h("kbd", null, "−"), h("span", null, "zoom"), h("kbd", null, "←"), h("kbd", null, "→"), h("span", null, "walk the graph"));
    return null;
  }

  function workspaceHead() {
    var t = tabById(state.tab);
    reviewLineEl = null;
    var review = t.id === "changes" || t.id === "findings" ? reviewLine() : null;
    return h("div", { class: "workspace-head", id: "workspace" },
      h("div", null, h("h2", null, t.title), h("p", null, t.blurb), review),
      headHint(t.id));
  }

  // An element still waiting to reveal sits 16px low; a scroll aimed at it
  // would land short. Programmatic scrolls settle it first.
  function settleReveal() {
    [wsHead, workspace].forEach(function (el) {
      if (el && el.hasAttribute("data-reveal")) { el.removeAttribute("data-reveal"); el.classList.remove("is-in"); }
    });
  }

  function scrollToWorkspace() {
    if (!wsHead) return;
    settleReveal();
    var top = wsHead.getBoundingClientRect().top;
    if (top > 160 || top < 0) wsHead.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "start" });
  }

  function setTab(id, scroll) {
    if (state.tab !== id) {
      state.tab = id;
      renderBody(true);
      syncHash();
    }
    if (scroll) scrollToWorkspace();
  }

  function renderBody(animate) {
    syncTabs();
    var head = workspaceHead();
    wsHead.replaceWith(head);
    wsHead = head;
    var single = state.tab === "outputs";
    var ws = h("div", {
      class: "workspace" + (single ? " single" : "") + (animate && !reducedMotion() ? " enter" : ""),
      id: "panel", role: "tabpanel", "aria-labelledby": "tab-" + state.tab,
    });
    mainCol = h("div", { class: "main" });
    ws.appendChild(mainCol);
    detailCol = single ? null : h("aside", { class: "detail", "aria-label": "Details" });
    if (detailCol) ws.appendChild(detailCol);
    workspace.replaceWith(ws);
    workspace = ws;
    renderMain();
    renderDetail();
  }

  function renderMain() {
    stopPlay();
    hideTip();
    hideApplyTip();
    cancelAnimationFrame(camRaf);
    mainCol.textContent = "";
    mapEls = null;
    listEl = null;
    switch (state.tab) {
      case "changes": mainCol.appendChild(changesView()); break;
      case "map": mainCol.appendChild(mapView()); break;
      case "apply": mainCol.appendChild(applyTimeline()); break;
      case "findings": mainCol.appendChild(findingsView()); break;
      case "drift": mainCol.appendChild(driftView()); break;
      case "outputs": mainCol.appendChild(outputsView()); break;
    }
  }

  // ---------------------------------------------------------- changes view

  function matches(c) {
    if (isUnchanged(c) && !state.unchanged) return false;
    if (isCosmetic(c) && !state.cosmetic) return false;
    if (!state.actions[actionKind(c.action)]) return false;
    if (state.q) {
      var q = state.q.toLowerCase();
      if (c.address.toLowerCase().indexOf(q) < 0 && (c.noun || "").toLowerCase().indexOf(q) < 0 && (c.summary || "").toLowerCase().indexOf(q) < 0) return false;
    }
    return true;
  }

  var listEl = null, rowEls = new Map(), selectedRowEl = null;

  // Grouping: by module (where it is defined), by kind (network, database
  // and so on, the way people think about infrastructure) or by action (all
  // the destroys together).
  var GROUP_ACTIONS = ["delete", "replace", "update", "create", "other"];
  var GROUP_ACTION_TITLE = { "delete": "Destroyed", replace: "Replaced", update: "Updated", create: "Created", other: "Other" };

  function groupKey(c) {
    if (state.groupBy === "edit") return editClass(c);
    // A resource with several owners is listed under the first, as the
    // CODEOWNERS line names them.
    if (state.groupBy === "owner") return (c.owners || [])[0] || "";
    if (state.groupBy === "kind") return c.category || "other";
    if (state.groupBy === "action") return actionKind(c.action);
    return c.module || "";
  }

  function groupOrder(a, b) {
    if (state.groupBy === "edit") return EDIT_GROUPS.indexOf(a) - EDIT_GROUPS.indexOf(b);
    if (state.groupBy === "owner") return a === "" ? 1 : b === "" ? -1 : a < b ? -1 : a > b ? 1 : 0;
    if (state.groupBy === "action") return GROUP_ACTIONS.indexOf(a) - GROUP_ACTIONS.indexOf(b);
    if (state.groupBy === "kind") return CATEGORY_ORDER.indexOf(a) - CATEGORY_ORDER.indexOf(b);
    return a === "" ? -1 : b === "" ? 1 : a < b ? -1 : a > b ? 1 : 0;
  }

  function groupTitle(key) {
    if (state.groupBy === "edit") return EDIT_GROUP_TITLE[key];
    if (state.groupBy === "owner") return key || "No code owner";
    if (state.groupBy === "action") return GROUP_ACTION_TITLE[key] || key;
    if (state.groupBy === "kind") return key === "dns" ? "DNS" : cap(key);
    return key || "Root module";
  }

  function changesView() {
    var wrap = h("div");
    wrap.appendChild(filterRow());
    listEl = h("div", { class: "change-list" });
    wrap.appendChild(listEl);
    fillList();
    return wrap;
  }

  function filterRow() {
    var row = h("div", { class: "filters", role: "group", "aria-label": "Filters" });
    var search = h("input", { class: "search", type: "search", placeholder: "Filter resources", "aria-label": "Filter resources", value: state.q, autocomplete: "off", spellcheck: "false" });
    var timer = 0;
    search.addEventListener("input", function () {
      state.q = search.value;
      clearTimeout(timer);
      // Large plans rebuild a long list; let the typing settle first.
      if (changes.length > 400) timer = setTimeout(fillList, 90); else fillList();
    });
    row.appendChild(search);
    var counts = { create: 0, update: 0, replace: 0, delete: 0, other: 0 };
    changes.forEach(function (c) { if (!isUnchanged(c) && !isCosmetic(c)) counts[actionKind(c.action)]++; });
    var names = { create: "Create", update: "Update", replace: "Replace", delete: "Destroy", other: "Other" };
    Object.keys(names).forEach(function (k) {
      if (!counts[k]) return;
      var chip = h("button", { class: "chip", type: "button", "aria-pressed": state.actions[k] ? "true" : "false", title: state.actions[k] ? "Hide these" : "Show these" },
        h("span", { class: "sym " + k }, KIND_GLYPH[k]), h("span", { class: "label" }, names[k]), h("span", { class: "n" }, String(counts[k])));
      chip.addEventListener("click", function () {
        state.actions[k] = !state.actions[k];
        chip.setAttribute("aria-pressed", state.actions[k] ? "true" : "false");
        chip.title = state.actions[k] ? "Hide these" : "Show these";
        fillList();
      });
      row.appendChild(chip);
    });
    var cosmeticCount = changes.filter(isCosmetic).length;
    if (cosmeticCount) row.appendChild(toggle("Show cosmetic", cosmeticCount, state.cosmetic, function (v) { state.cosmetic = v; fillList(); }));
    var unchangedCount = changes.filter(isUnchanged).length;
    if (unchangedCount) row.appendChild(toggle("Show unchanged", unchangedCount, state.unchanged, function (v) { state.unchanged = v; fillList(); }));
    var groups = h("div", { class: "options group-by", role: "group", "aria-label": "Group by" }, h("span", { class: "opt-label", "aria-hidden": "true" }, "Group by"));
    var by = [["module", "Module"], ["kind", "Kind"], ["action", "Action"]];
    if (changes.some(function (c) { return c.edit; })) by.push(["edit", "Your edits"]);
    if (changes.some(function (c) { return c.owners && c.owners.length; })) by.push(["owner", "Owner"]);
    by.forEach(function (g) {
      var b = h("button", { type: "button", "aria-pressed": state.groupBy === g[0] ? "true" : "false" }, g[1]);
      b.addEventListener("click", function () {
        state.groupBy = g[0];
        state.collapsed = new Set();
        groups.querySelectorAll("button").forEach(function (x) { x.setAttribute("aria-pressed", x === b ? "true" : "false"); });
        fillList();
      });
      groups.appendChild(b);
    });
    row.appendChild(groups);
    return row;
  }

  var KIND_GLYPH = { create: "+", update: "~", replace: "-/+", delete: "−", other: "·" };

  function toggle(text, n, checked, onChange) {
    var input = h("input", { type: "checkbox" });
    input.checked = checked;
    input.addEventListener("change", function () { onChange(input.checked); });
    return h("label", { class: "check" }, input, text, n ? h("span", { class: "n" }, String(n)) : null);
  }

  function fillList() {
    if (!listEl) return;
    listEl.textContent = "";
    rowEls = new Map();
    selectedRowEl = null;
    var groups = new Map();
    var shown = 0;
    changes.forEach(function (c) {
      if (!matches(c)) return;
      shown++;
      var key = groupKey(c);
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(c);
    });
    var keys = Array.from(groups.keys()).sort(groupOrder);
    keys.forEach(function (key) {
      var items = groups.get(key);
      var collapsed = state.collapsed.has(key);
      var tally = h("span", { class: "tally" });
      var t = { create: 0, update: 0, replace: 0, delete: 0, other: 0 };
      items.forEach(function (c) { t[actionKind(c.action)]++; });
      Object.keys(t).forEach(function (k) {
        if (t[k]) tally.appendChild(h("span", { title: t[k] + " " + (k === "delete" ? "to destroy" : k === "other" ? "other" : "to " + k) }, h("span", { class: "sym " + k }, KIND_GLYPH[k]), " " + t[k]));
      });
      var title = groupTitle(key);
      var head = h("button", { class: "module-head", type: "button", "aria-expanded": collapsed ? "false" : "true" },
        icon("caretDown", "caret"),
        h("span", { class: "module-name" + (state.groupBy === "module" && key ? " path" : "") }, title),
        tally);
      var rows = h("ul", { class: "rows", role: "listbox", "aria-label": title });
      items.forEach(function (c) { rows.appendChild(h("li", { role: "none" }, rowFor(c))); });
      var section = h("section", { class: "module", "data-collapsed": collapsed ? "true" : "false" }, head, rows);
      head.addEventListener("click", function () {
        if (state.collapsed.has(key)) state.collapsed.delete(key); else state.collapsed.add(key);
        var now = state.collapsed.has(key);
        section.setAttribute("data-collapsed", now ? "true" : "false");
        head.setAttribute("aria-expanded", now ? "false" : "true");
      });
      listEl.appendChild(section);
    });
    if (!shown) {
      listEl.appendChild(h("p", { class: "empty" }, changedCount() ? "Nothing matches these filters." : "This plan changes no resources."));
    }
    var hiddenCosmetic = !state.cosmetic ? changes.filter(isCosmetic).length : 0;
    if (hiddenCosmetic) {
      listEl.appendChild(h("p", { class: "hidden-note" },
        plural(hiddenCosmetic, "cosmetic update") + " hidden (tags or formatting only). ",
        h("button", { type: "button", onclick: function () { state.cosmetic = true; renderMain(); } }, "Show them")));
    }
  }

  function rowFor(c) {
    var selected = !!(state.sel && state.sel.kind === "change" && state.sel.id === c.address);
    var name = localName(c);
    var dot = name.indexOf(".");
    var addr = h("span", { class: "addr", title: c.address });
    if (c.mode === "data" || dot <= 0) {
      addr.appendChild(document.createTextNode(name));
    } else {
      addr.appendChild(h("span", { class: "type" }, name.slice(0, dot + 1)));
      addr.appendChild(document.createTextNode(name.slice(dot + 1)));
    }
    var marks = h("span", { class: "marks" });
    if (isReviewed(c)) marks.appendChild(reviewedTag());
    var since = sinceMark(c);
    if (since) marks.appendChild(since);
    if (c.edit) marks.appendChild(h("span", { class: "tag", title: editTitle(c.edit) }, "edited"));
    if (c.drifted) marks.appendChild(h("span", { class: "tag", title: "Changed outside " + TOOL }, "drifted"));
    if (c.indirect) marks.appendChild(h("span", { class: "tag", title: "Only changes because something it depends on changes" }, "follows"));
    if (c.cosmetic) marks.appendChild(h("span", { class: "tag" }, c.cosmetic));
    if (sevRank[c.severity] >= 3) marks.appendChild(sevMark(c.severity));
    var row = h("button", {
      class: "row", type: "button", role: "option", "aria-selected": selected ? "true" : "false", "data-address": c.address,
      onclick: function () { select({ kind: "change", id: c.address }); },
    }, changeMark(c), addr, marks, h("span", { class: "desc" }, c.summary ? label(c) + " · " + c.summary : label(c)));
    rowEls.set(c.address, row);
    if (selected) selectedRowEl = row;
    return row;
  }

  function syncRows() {
    syncApplyRows();
    if (selectedRowEl) selectedRowEl.setAttribute("aria-selected", "false");
    selectedRowEl = null;
    if (state.sel && state.sel.kind === "change" && rowEls.has(state.sel.id)) {
      selectedRowEl = rowEls.get(state.sel.id);
      selectedRowEl.setAttribute("aria-selected", "true");
    }
  }

  // ------------------------------------------------------------ detail pane

  // The pane updates instantly: selection is the most frequent thing anyone
  // does here, so it never waits on an animation.
  function renderDetail() {
    if (!detailCol) return;
    detailCol.textContent = "";
    var inner = h("div", { class: "detail-inner" });
    detailCol.appendChild(h("div", { class: "core" }, inner));
    var sel = state.sel;
    if (sel && sel.kind === "change" && byAddr.has(sel.id)) changeDetail(inner, byAddr.get(sel.id));
    else if (sel && sel.kind === "resource" && (nodeById.has(sel.id) || byResource.has(sel.id))) resourceDetail(inner, sel.id);
    else overviewDetail(inner);
  }

  function overviewDetail(inner) {
    inner.appendChild(h("div", { class: "section-title", style: "margin-top:0" }, "Details"));
    inner.appendChild(h("p", { class: "why" }, "Select a resource to see why it changes, what it follows from, its findings and the full diff."));
    var top = findings.slice(0, 3);
    if (top.length) {
      inner.appendChild(h("div", { class: "section-title" }, "Start here"));
      top.forEach(function (f) { inner.appendChild(findingCard(f, true)); });
    }
    inner.appendChild(h("div", { class: "section-title" }, "Keys"));
    var keys = h("dl", { class: "keys" });
    var keyList = [[["/"], "Filter resources"], [["j", "k"], "Next and previous resource"], [["1", String(TABS.length)], "Switch view"], [["esc"], "Clear the selection"]];
    if (toReview.length) keyList.splice(2, 0, [["n"], "Next change to review"], [["r"], "Mark the selected change reviewed"]);
    keyList.forEach(function (k) {
      keys.appendChild(h("dt", null, k[0].map(function (key, i) { return [i ? h("span", { class: "to" }, k[0][0] === "1" ? "–" : "") : null, h("kbd", null, key)]; })));
      keys.appendChild(h("dd", null, k[1]));
    });
    inner.appendChild(keys);
  }

  // docsURL is where a resource type is documented: the provider's page on
  // the registry the plan's providers came from. Links only leave the page
  // when followed; the report itself still fetches nothing.
  function docsURL(provider, type, mode) {
    if (type === "terraform_data") return plan.tool === "opentofu" ? "https://opentofu.org/docs/language/resources/tf-data/" : "https://developer.hashicorp.com/terraform/language/resources/terraform-data";
    var source = (plan.providers || {})[provider];
    var parts = source ? source.split("/") : [];
    if (parts.length !== 3) return null;
    // Resource types start with the provider's local name, which need not
    // be the source's name: google-beta's types begin google_.
    var name = type.slice(type.indexOf("_") + 1);
    if (parts[0] === "registry.terraform.io") return "https://registry.terraform.io/providers/" + parts[1] + "/" + parts[2] + "/latest/docs/" + (mode === "data" ? "data-sources" : "resources") + "/" + name;
    if (parts[0] === "registry.opentofu.org") return "https://search.opentofu.org/provider/" + parts[1] + "/" + parts[2] + "/latest/docs/" + (mode === "data" ? "datasources" : "resources") + "/" + name;
    return null;
  }

  function nounEl(noun, provider, type, mode) {
    var url = docsURL(provider, type, mode);
    if (!url) return h("span", null, noun);
    var host = url.split("/")[2].replace(/^www\./, "");
    return h("span", null, h("a", { class: "doc-link", href: url, target: "_blank", rel: "noopener noreferrer", title: (mode === "data" ? "data." : "") + type + " documentation on " + host }, noun));
  }

  function changeDetail(inner, c) {
    inner.appendChild(h("div", { class: "detail-title" }, changeMark(c), h("h3", null, c.address), copyButton(c.address, "address", true)));
    var chips = h("p", { class: "meta-line" });
    chips.appendChild(h("span", null, cap(label(c))));
    if (c.noun) chips.appendChild(nounEl(c.noun, c.provider, c.type, c.mode));
    if (c.location) chips.appendChild(h("span", { class: "mono" }, c.location.file + ":" + c.location.line));
    if (c.stateful) chips.appendChild(h("span", null, "holds data"));
    if (c.owners && c.owners.length) chips.appendChild(h("span", { title: "Its code owners, from " + ((data.git || {}).codeowners || "CODEOWNERS") }, "owned by " + andList(c.owners)));
    inner.appendChild(chips);
    if (needsLook(c)) inner.appendChild(reviewControl(c));
    if (c.why) inner.appendChild(h("p", { class: "why" }, rich(c.why)));
    if (c.edit) {
      inner.appendChild(h("div", { class: "section-title" }, "Your edit"));
      inner.appendChild(h("p", { class: "why small" }, rich(editLead(c.edit))));
      inner.appendChild(editLinesEl(c.edit));
    }
    if (c.deploys && c.deploys.length) {
      inner.appendChild(h("div", { class: "section-title" }, "Deploys"));
      var dl = h("ul", { class: "access deploys" });
      c.deploys.forEach(function (d) {
        var to = d.unknown ? h("span", { class: "muted" }, "known after apply") : d.to ? h("code", null, d.to) : null;
        dl.appendChild(h("li", null, h("span", { class: "op add", "aria-hidden": "true" }, "↑"),
          h("span", null, d.what === "code" ? "New code" : h("code", null, d.what), d.from ? [" ", h("code", { class: "was" }, d.from), " → "] : " ", to)));
      });
      inner.appendChild(dl);
    }
    if (c.access && c.access.length) {
      inner.appendChild(h("div", { class: "section-title" }, accessTitle(c.access)));
      inner.appendChild(accessList(c.access));
    }
    var when = applyLines(c);
    if (when) {
      inner.appendChild(h("div", { class: "section-title" }, "In the apply"));
      inner.appendChild(when);
    }

    if (c.causes && c.causes.length) {
      inner.appendChild(h("div", { class: "section-title" }, "Because of"));
      var ul = h("ul", { class: "causes" });
      c.causes.forEach(function (k) {
        ul.appendChild(h("li", null, h("code", { class: "plain" }, k.attribute), h("span", { class: "arrow" }, "←"), refFor(k.resource), k.via ? h("code", { class: "plain" }, "." + k.via) : null, h("span", { class: "tag" }, verbFor(k.action))));
      });
      inner.appendChild(ul);
      var start = startsWith(c);
      if (start) inner.appendChild(h("p", { class: "why small starts" }, rich(start)));
    }

    var fs = (c.findings || []).map(function (id) { return findingById.get(id); }).filter(Boolean);
    if (fs.length) {
      inner.appendChild(h("div", { class: "section-title" }, findingsTitle(fs.length)));
      fs.forEach(function (f) { inner.appendChild(findingCard(f, true)); });
    }

    var d = drift.filter(function (x) { return x.address === c.address && !x.noise; })[0];
    if (d) {
      inner.appendChild(h("div", { class: "section-title" }, "Changed outside " + TOOL));
      inner.appendChild(h("p", { class: "why small" }, rich(d.summary)));
    }

    if (c.diff && c.action !== "no-op") {
      inner.appendChild(h("div", { class: "section-title" }, diffTitle(c)));
      inner.appendChild(topDiff(c));
    } else if (c.diff && c.importing) {
      inner.appendChild(h("div", { class: "section-title" }, "Imported object"));
      inner.appendChild(diffTree(c.diff));
    }

    relations(inner, c.resource);

    var siblings = byResource.get(c.resource) || [];
    if (siblings.length > 1) {
      inner.appendChild(h("div", { class: "section-title" }, "Other instances"));
      inner.appendChild(instanceLinks(siblings, c.address));
    }
  }

  // Access: what an IAM policy or firewall change lets in that it did not,
  // and what it stops letting in, in words rather than JSON or rule blocks.
  var ACCESS_SHOWN = 12;
  function accessTitle(lines) {
    var iam = lines.some(function (l) { return l.kind === "iam"; });
    var net = lines.some(function (l) { return l.kind === "network"; });
    return iam && net ? "Access" : iam ? "Permissions" : "Network access";
  }

  function accessList(lines) {
    var ul = h("ul", { class: "access" });
    function row(l) {
      return h("li", { class: l.wide ? "wide" : null },
        h("span", { class: "op " + (l.op === "+" ? "add" : "del"), "aria-label": l.op === "+" ? "Now allows" : "No longer allows" }, l.op === "+" ? "+" : "−"),
        h("span", null, rich(l.text)));
    }
    lines.slice(0, ACCESS_SHOWN).forEach(function (l) { ul.appendChild(row(l)); });
    if (lines.length > ACCESS_SHOWN) {
      var more = h("li", { class: "more" }, h("button", { class: "text-link", type: "button", onclick: function () {
        more.remove();
        lines.slice(ACCESS_SHOWN).forEach(function (l) { ul.appendChild(row(l)); });
      } }, "Show " + plural(lines.length - ACCESS_SHOWN, "more line")));
      ul.appendChild(more);
    }
    return ul;
  }

  function verbFor(action) {
    return { create: "new", replace: "replaced", update: "updated", read: "read during apply", delete: "destroyed" }[action] || action;
  }

  function diffTitle(c) {
    switch (c.action) {
      case "create": return "Planned attributes";
      case "delete": return "Current attributes";
      case "read": return "Data source";
      default:
        var parts = [];
        if (c.changed) parts.push(plural(c.changed, "attribute") + " changing");
        if (c.unknown) parts.push(c.unknown + " known after apply");
        return parts.length ? parts.join(" · ") : "Changes";
    }
  }

  function resourceDetail(inner, id) {
    var node = nodeById.get(id);
    var inst = byResource.get(id) || [];
    var action = node ? node.action : (inst[0] && inst[0].action) || "no-op";
    inner.appendChild(h("div", { class: "detail-title" }, actMark(action, GLYPH[action] || "·", action), h("h3", null, id), copyButton(id, "address", true)));
    var chips = h("p", { class: "meta-line" });
    if (node && node.noun) chips.appendChild(nounEl(cap(node.noun), node.type.split("_")[0], node.type, node.mode));
    chips.appendChild(h("span", null, plural(inst.length, "instance")));
    if (node && node.location) chips.appendChild(h("span", { class: "mono" }, node.location.file + ":" + node.location.line));
    inner.appendChild(chips);
    if (inst.length) {
      inner.appendChild(h("div", { class: "section-title" }, "Instances"));
      inner.appendChild(instanceLinks(inst, null));
    } else {
      inner.appendChild(h("p", { class: "why" }, node && node.mode === "data" ? "A data source read while planning." : "No instances in this plan."));
    }
    var ids = new Set();
    inst.forEach(function (c) { (c.findings || []).forEach(function (f) { ids.add(f); }); });
    if (ids.size) {
      inner.appendChild(h("div", { class: "section-title" }, findingsTitle(ids.size)));
      ids.forEach(function (fid) { var f = findingById.get(fid); if (f) inner.appendChild(findingCard(f, true)); });
    }
    relations(inner, id);
  }

  function instanceLinks(list, current) {
    var box = h("div", { class: "links" });
    list.forEach(function (c) {
      if (c.address === current) return;
      box.appendChild(h("button", { class: "link-row", type: "button", onclick: function () { select({ kind: "change", id: c.address }); } }, changeMark(c), h("span", null, c.address)));
    });
    return box;
  }

  function relations(inner, id) {
    var up = ups.get(id) || [], down = downs.get(id) || [];
    if (up.length) {
      inner.appendChild(h("div", { class: "section-title" }, "Depends on"));
      inner.appendChild(nodeLinks(up));
    }
    if (down.length) {
      inner.appendChild(h("div", { class: "section-title" }, "Used by"));
      inner.appendChild(nodeLinks(down));
    }
    var blast = transitive(id, downs);
    if (blast.size > down.length) {
      inner.appendChild(h("p", { class: "blast-note" }, plural(blast.size, "resource depends", "resources depend") + " on it directly or indirectly. The map traces them."));
    }
  }

  function nodeLinks(ids) {
    var box = h("div", { class: "links" });
    ids.slice().sort().forEach(function (nid) {
      var node = nodeById.get(nid);
      var action = node ? node.action : "no-op";
      var inst = byResource.get(nid) || [];
      var target = inst.length === 1 ? { kind: "change", id: inst[0].address } : { kind: "resource", id: nid };
      box.appendChild(h("button", { class: "link-row", type: "button", onclick: function () { select(target); } }, actMark(action, GLYPH[action] || "·", action), h("span", null, nid)));
    });
    return box;
  }

  function transitive(id, edges) {
    var seen = new Set(), queue = [id];
    while (queue.length) {
      var cur = queue.shift();
      (edges.get(cur) || []).forEach(function (n) { if (!seen.has(n) && n !== id) { seen.add(n); queue.push(n); } });
    }
    return seen;
  }

  // ------------------------------------------------------------------ diffs

  function fmt(v) {
    if (v === null || v === undefined) return "null";
    if (typeof v === "string" || typeof v === "object") return JSON.stringify(v);
    return String(v);
  }

  // redact() puts this stand-in wherever a secret was. On its own it is
  // not data, so it is not quoted or struck through like a value.
  var MASKED = "(sensitive value)";
  function oldValue(v) { return v === MASKED ? valueEl(v) : h("span", { class: "before" }, valueEl(v)); }

  function valueEl(v) {
    if (v === MASKED) return h("span", { class: "pending" }, "sensitive");
    var text = fmt(v);
    if (text.length <= 180) return h("span", null, text);
    var span = h("span", null, text.slice(0, 160) + "…");
    var more = h("button", { class: "expand", type: "button" }, "show all");
    more.addEventListener("click", function () { span.textContent = text; more.remove(); });
    return h("span", null, span, more);
  }

  // Word-level highlights: the tokens that differ between two versions of a
  // line or value. Returns null when the two share too little for a
  // highlight to help, or are too long to compare cheaply.
  function wordDiff(a, b) {
    if (a.length > 1000 || b.length > 1000) return null;
    var ta = a.match(/\w+|\s+|[^\w\s]/g) || [], tb = b.match(/\w+|\s+|[^\w\s]/g) || [];
    var n = ta.length, m = tb.length;
    if (!n || !m) return null;
    var pre = 0;
    while (pre < n && pre < m && ta[pre] === tb[pre]) pre++;
    var suf = 0;
    while (suf < n - pre && suf < m - pre && ta[n - 1 - suf] === tb[m - 1 - suf]) suf++;
    var A = ta.slice(pre, n - suf), B = tb.slice(pre, m - suf);
    if (A.length * B.length > 40000) return null;
    // Longest common subsequence over the differing middle.
    var dp = [];
    for (var i = 0; i <= A.length; i++) dp.push(new Uint16Array(B.length + 1));
    for (i = A.length - 1; i >= 0; i--) {
      for (var j = B.length - 1; j >= 0; j--) dp[i][j] = A[i] === B[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
    var keepA = new Array(A.length).fill(false), keepB = new Array(B.length).fill(false);
    i = 0; j = 0;
    while (i < A.length && j < B.length) {
      if (A[i] === B[j]) { keepA[i] = keepB[j] = true; i++; j++; }
      else if (dp[i + 1][j] >= dp[i][j + 1]) i++;
      else j++;
    }
    var shared = 0;
    for (i = 0; i < ta.length; i++) if (i < pre || i >= n - suf || keepA[i - pre]) shared += ta[i].length;
    if (shared / Math.max(a.length, b.length) < 0.2) return null;
    function segments(all, keep) {
      var changed = all.map(function (t, k) { return k >= pre && k < all.length - suf && !keep[k - pre]; });
      // A space between two changed words belongs to the change.
      for (var k = 1; k < all.length - 1; k++) if (!changed[k] && /^\s+$/.test(all[k]) && changed[k - 1] && changed[k + 1]) changed[k] = true;
      var out = [];
      all.forEach(function (t, k) {
        var last = out[out.length - 1];
        if (last && last.changed === changed[k]) last.text += t;
        else out.push({ text: t, changed: changed[k] });
      });
      return out;
    }
    return { a: segments(ta, keepA), b: segments(tb, keepB) };
  }

  function marked(segs, cls) {
    var span = h("span");
    segs.forEach(function (sg) { span.appendChild(sg.changed ? h("mark", { class: cls }, sg.text) : document.createTextNode(sg.text)); });
    return span;
  }

  // before → after for one value, with the changed words marked when both
  // are strings that mostly agree.
  function changePair(before, after, pending) {
    var out = [];
    var wd = !pending && typeof before === "string" && typeof after === "string" && before.length >= 8 && after.length >= 8 && before !== MASKED && after !== MASKED ? wordDiff(fmt(before), fmt(after)) : null;
    if (wd && fmt(before).length <= 180 && fmt(after).length <= 180) {
      out.push(h("span", { class: "before" }, marked(wd.a, "del")));
      out.push(h("span", { class: "to" }, "→"));
      out.push(marked(wd.b, "add"));
      return out;
    }
    out.push(oldValue(before));
    out.push(h("span", { class: "to" }, "→"));
    out.push(pending ? h("span", { class: "pending" }, "known after apply") : valueEl(after));
    return out;
  }

  function lineEl(op, text, segs) {
    var cls = op === "+" ? "add" : op === "-" ? "del" : "";
    var el = h("span", { class: "line " + cls }, (op === " " ? " " : op) + " ");
    if (segs) el.appendChild(marked(segs, op === "+" ? "add" : "del"));
    else el.appendChild(document.createTextNode(text));
    return el;
  }

  // Multi-line strings: a run of removed lines followed by a run of added
  // ones is a rewrite, so pair them up and mark the words that changed.
  function linesEl(lines) {
    var pre = h("div", { class: "lines" });
    var i = 0;
    while (i < lines.length) {
      var l = lines[i];
      if (l.op === "-") {
        var j = i;
        while (j < lines.length && lines[j].op === "-") j++;
        var k = j;
        while (k < lines.length && lines[k].op === "+") k++;
        var dels = lines.slice(i, j), adds = lines.slice(j, k);
        var pairs = [];
        for (var q = 0; q < Math.min(dels.length, adds.length); q++) pairs.push(wordDiff(dels[q].text, adds[q].text));
        dels.forEach(function (d, idx) { pre.appendChild(lineEl("-", d.text, pairs[idx] ? pairs[idx].a : null)); });
        adds.forEach(function (a, idx) { pre.appendChild(lineEl("+", a.text, pairs[idx] ? pairs[idx].b : null)); });
        i = k;
        continue;
      }
      if (l.op === "skip") pre.appendChild(h("span", { class: "line skip" }, plural(l.skip, "unchanged line") + " hidden"));
      else if (l.op === "note") pre.appendChild(h("span", { class: "line skip" }, l.text));
      else pre.appendChild(lineEl(l.op, l.text, null));
      i++;
    }
    return pre;
  }

  function forcesTag() {
    return h("span", { class: "forces", title: "Changing this forces replacement" }, sevIcon("high"), "forces replacement");
  }

  // topDiff puts what the plan changes first. Attributes the provider only
  // recomputes (known after apply), and attributes a replacement simply does
  // not set, are folded away.
  function topDiff(c) {
    var root = c.diff;
    // What makes each top-level attribute unknown, said where it shows.
    var causes = new Map((c.causes || []).map(function (k) { return [k.attribute, k]; }));
    var children = root.children || [];
    var primary = [], recomputed = [], cleared = [];
    children.forEach(function (n) {
      if (n.forces) primary.push(n);
      else if (n.unknown && !n.sensitive) recomputed.push(n);
      else if (c.action === "replace" && n.kind === "removed") cleared.push(n);
      else primary.push(n);
    });
    primary.sort(function (a, b) { return (b.forces ? 1 : 0) - (a.forces ? 1 : 0); });
    var ul = h("ul", { class: "diff" });
    var limit = c.action === "delete" ? 14 : primary.length;
    primary.slice(0, limit).forEach(function (n) { ul.appendChild(diffRow(n, false, causes)); });
    if (primary.length > limit) ul.appendChild(fold(plural(primary.length - limit, "more attribute"), primary.slice(limit), causes));
    if (!primary.length && !recomputed.length && !cleared.length) ul.appendChild(h("li", { class: "dhidden" }, "No attribute changes."));
    if (recomputed.length) ul.appendChild(fold(plural(recomputed.length, "attribute") + " known after apply", recomputed, causes));
    if (cleared.length) ul.appendChild(fold(plural(cleared.length, "attribute") + " not set on the new object", cleared));
    if (root.hidden) ul.appendChild(h("li", { class: "dhidden" }, plural(root.hidden, "unchanged attribute") + " hidden"));
    return ul;
  }

  function fold(text, nodes, causes) {
    var li = h("li", { class: "fold" });
    var btn = h("button", { class: "fold-toggle", type: "button", "aria-expanded": "false" }, icon("caretRight", "caret"), text);
    var body = null;
    btn.addEventListener("click", function () {
      var open = btn.getAttribute("aria-expanded") === "true";
      btn.setAttribute("aria-expanded", open ? "false" : "true");
      if (open) { if (body) body.remove(); body = null; return; }
      body = h("ul", { class: "diff" });
      nodes.forEach(function (n) { body.appendChild(diffRow(n, false, causes)); });
      li.appendChild(body);
    });
    li.appendChild(btn);
    return li;
  }

  function diffTree(root) {
    var ul = h("ul", { class: "diff" });
    var children = root.children || [];
    var inList = root.type === "list";
    children.forEach(function (n) { ul.appendChild(diffRow(n, inList)); });
    if (!children.length && !root.hidden) {
      if (root.unknown) ul.appendChild(h("li", { class: "dhidden" }, "Known after apply"));
      else if (root.kind === "unchanged") ul.appendChild(h("li", { class: "dhidden" }, "No attribute changes."));
    }
    if (root.hidden) ul.appendChild(h("li", { class: "dhidden" }, plural(root.hidden, inList ? "unchanged element" : "unchanged attribute") + " hidden"));
    return ul;
  }

  // causeHint says which change makes a value unknown: from
  // random_id.suffix.hex (replaced).
  function causeHint(causes, n) {
    var k = causes && n.unknown && causes.get(n.key);
    if (!k) return null;
    return h("span", { class: "cause-hint" }, "from ", h("code", null, k.resource + (k.via ? "." + k.via : "")), " (" + verbFor(k.action) + ")");
  }

  function diffRow(n, inList, causes) {
    var sig = { added: "+", removed: "−", changed: "~", unchanged: " " }[n.kind] || " ";
    var li = h("li", { class: "drow " + n.kind });
    li.appendChild(h("span", { class: "sigil", "aria-hidden": "true" }, sig));
    var body = h("div", { class: "dval" });
    li.appendChild(body);
    if (!inList) body.appendChild(h("span", { class: "dkey" }, n.key + " = "));
    var forces = n.forces ? forcesTag() : null;
    var hasKids = n.children && n.children.length;

    if (n.sensitive) {
      body.appendChild(h("span", { class: "pending" }, n.kind === "unchanged" ? "sensitive" : "sensitive, " + (n.kind === "added" ? "set" : n.kind === "removed" ? "removed" : "changed")));
      append(body, forces);
      return li;
    }
    if (n.lines) {
      append(body, forces);
      li.appendChild(h("span"));
      li.appendChild(linesEl(n.lines));
      return li;
    }
    if (hasKids) {
      var open = n.type === "list" ? "[" : "{";
      body.appendChild(h("span", { class: "dkey" }, n.encoding === "json" ? "JSON " + open : open));
      if (n.unknown) body.appendChild(h("span", null, h("span", { class: "to" }, "→"), h("span", { class: "pending" }, "known after apply"), causeHint(causes, n)));
      append(body, forces);
      li.appendChild(h("span"));
      li.appendChild(diffTree(n));
      return li;
    }
    if (n.kind === "added" && n.unknown) {
      body.appendChild(h("span", { class: "pending" }, "known after apply"));
      append(body, causeHint(causes, n));
    } else if (n.kind === "added") {
      body.appendChild(valueEl(n.after));
    } else if (n.kind === "removed") {
      body.appendChild(oldValue(n.before));
    } else if (n.unknown) {
      append(body, changePair(n.before, null, true));
      append(body, causeHint(causes, n));
    } else if (n.kind === "changed") {
      append(body, changePair(n.before, n.after, false));
    } else {
      body.appendChild(valueEl(n.after !== undefined ? n.after : n.before));
    }
    append(body, forces);
    return li;
  }

  // --------------------------------------------------------------- findings

  function findingCard(f, compact) {
    var card = h("article", { class: "finding", style: "--sev-color:" + sevColor(f.severity) });
    // A team's own policy says so, so it is not mistaken for one of
    // interragate's rules.
    var about = null;
    var rule = f.category === "policy"
      ? h("span", { class: "rule", title: "A policy from this repository's .interragate.yaml" }, "policy " + f.rule)
      : h("span", { class: "rule" }, f.rule);
    // The rule's own rationale, a click away for a reader new to it.
    var doc = (data.rule_docs || {})[f.rule];
    if (doc) {
      rule = h("button", { class: "rule", type: "button", "aria-expanded": "false", title: "What this rule looks for" }, f.category === "policy" ? "policy " + f.rule : f.rule);
      rule.addEventListener("click", function (e) {
        e.stopPropagation();
        var open = rule.getAttribute("aria-expanded") !== "true";
        rule.setAttribute("aria-expanded", open ? "true" : "false");
        about.hidden = !open;
      });
      about = h("p", { class: "about", hidden: "" }, rich(doc));
    }
    card.appendChild(h("div", { class: "finding-head" }, sevMark(f.severity, true), h("span", { class: "finding-title" }, f.title), rule));
    if (about) card.appendChild(about);
    card.appendChild(h("p", null, rich(f.message)));
    if (f.remediation) card.appendChild(h("p", { class: "fix" }, h("strong", null, "Fix: "), rich(f.remediation)));
    if (f.snippet) {
      var pre = h("pre", { class: "snippet" }, f.snippet);
      pre.appendChild(copyButton(f.snippet, "snippet"));
      card.appendChild(pre);
    }
    if (!compact && f.addresses && f.addresses.length) {
      var targets = h("div", { class: "targets" });
      f.addresses.slice(0, 12).forEach(function (a) { targets.appendChild(refFor(a)); });
      if (f.addresses.length > 12) targets.appendChild(h("span", { class: "tag" }, "+" + (f.addresses.length - 12) + " more"));
      card.appendChild(targets);
    }
    return card;
  }

  // Printing from any other view adds every finding after it: on paper
  // there is no tab to switch to.
  var printFindings = null;
  window.addEventListener("beforeprint", function () {
    if (state.tab === "findings" || !findings.length || !mainCol) return;
    printFindings = h("section", { class: "print-findings" }, h("h2", null, findingsTitle(findings.length)), findingsView());
    mainCol.appendChild(printFindings);
  });
  window.addEventListener("afterprint", function () {
    if (printFindings) printFindings.remove();
    printFindings = null;
  });

  function findingsView() {
    var wrap = h("div");
    if (!findings.length) {
      wrap.appendChild(h("p", { class: "empty" }, "No findings. Nothing in this plan matched a rule."));
    }
    SEVERITIES.forEach(function (sev) {
      var group = findings.filter(function (f) { return f.severity === sev; });
      if (!group.length) return;
      var sec = h("section", { class: "findings-group" }, h("h3", null, sevIcon(sev), SEV_LABEL[sev], h("span", { class: "count" }, String(group.length))));
      group.forEach(function (f) { sec.appendChild(findingCard(f, false)); });
      wrap.appendChild(sec);
    });
    var sup = data.suppressed || [];
    if (sup.length) {
      var p = h("p", { class: "suppressed" }, plural(sup.length, "finding") + " suppressed by .interragate.yaml: ");
      sup.forEach(function (f, i) {
        if (i) p.appendChild(document.createTextNode(", "));
        p.appendChild(h("code", { class: "plain" }, f.rule));
        if (f.reason) p.appendChild(document.createTextNode(" (" + f.reason + ")"));
      });
      wrap.appendChild(p);
    }
    return wrap;
  }

  // ------------------------------------------------------------------ drift

  function driftView() {
    var wrap = h("div", { class: "card-list" });
    var real = drift.filter(function (d) { return !d.noise; });
    var noise = drift.filter(function (d) { return d.noise; });
    if (!real.length) wrap.appendChild(h("p", { class: "empty" }, "Nothing was changed outside " + TOOL + (noise.length ? ", apart from provider noise." : ".")));
    real.forEach(function (d) { wrap.appendChild(driftCard(d)); });
    if (noise.length) {
      var note = h("p", { class: "hidden-note" }, plural(noise.length, "resource") + " showed only provider-side differences (default tags, timestamps, empty values). ");
      wrap.appendChild(note);
      if (!state.noiseDrift) note.appendChild(h("button", { type: "button", onclick: function () { state.noiseDrift = true; renderMain(); } }, "Show them"));
      else noise.forEach(function (d) { wrap.appendChild(driftCard(d)); });
    }
    return wrap;
  }

  function driftCard(d) {
    var c = byAddr.get(d.address);
    var card = h("article", { class: "plain-card" });
    card.appendChild(h("h3", null, c ? changeMark(c) : null, refFor(d.address), d.action === "delete" ? h("span", { class: "note" }, "deleted outside " + TOOL) : null));
    card.appendChild(h("p", null, rich(d.summary)));
    if (d.reverted && d.reverted.length) {
      card.appendChild(h("p", null, h("strong", null, "Reverted by this plan: "), d.reverted.map(function (r, i) { return [i ? ", " : "", h("code", { class: "plain" }, r)]; })));
    }
    if (d.kept && d.kept.length) {
      card.appendChild(h("p", null, h("strong", null, "Kept: "), d.kept.map(function (r, i) { return [i ? ", " : "", h("code", { class: "plain" }, r)]; })));
    }
    if (d.diff && d.diff.children && d.diff.children.length) {
      card.appendChild(h("div", { class: "section-title" }, "State → real object"));
      card.appendChild(diffTree(d.diff));
    }
    return card;
  }

  // ---------------------------------------------------------------- outputs

  function outputsView() {
    var changed = outputs.filter(function (o) { return o.action !== "no-op"; });
    if (!changed.length) return h("p", { class: "empty" }, "No output values change.");
    var table = h("table", { class: "outputs" });
    table.appendChild(h("thead", null, h("tr", null, h("th", { class: "act-col" }, h("span", { class: "visually-hidden" }, "Action")), h("th", null, "Output"), h("th", null, "Change"))));
    var body = h("tbody");
    changed.forEach(function (o) {
      var change = h("td");
      if (o.sensitive) change.appendChild(h("span", { class: "pending" }, "sensitive"));
      else if (o.diff) change.appendChild(inlineDiff(o.diff));
      body.appendChild(h("tr", { "data-output": o.name }, h("td", null, actMark(o.action, GLYPH[o.action] || "·", o.action)), h("td", { class: "mono" }, o.name), change));
    });
    table.appendChild(body);
    return h("div", { class: "outputs-wrap" }, table);
  }

  function inlineDiff(n) {
    var wrap = h("span", { class: "dval mono" });
    if (n.children && n.children.length) {
      wrap.appendChild(diffTree(n));
      return wrap;
    }
    if (n.kind === "added") wrap.appendChild(n.unknown ? h("span", { class: "pending" }, "known after apply") : valueEl(n.after));
    else if (n.kind === "removed") wrap.appendChild(oldValue(n.before));
    else append(wrap, changePair(n.before, n.after, !!n.unknown));
    return wrap;
  }

  // -------------------------------------------------------------------- map

  var NODE_W = 228, NODE_H = 52, GAP_X = 72, GAP_Y = 14, LANE_HEAD = 34, LANE_PAD = 16, LANE_GAP = 14, MARGIN = 24;
  var CATEGORY_ORDER = ["network", "firewall", "identity", "secrets", "dns", "edge", "compute", "containers", "serverless", "database", "storage", "messaging", "monitoring", "utility", "other"];
  var mapEls = null;
  var suppressClickUntil = 0;
  var camRaf = 0;
  // Beyond this many highlighted edges the blast radius stays still: a
  // marching dash on hundreds of paths costs more than it tells.
  var FLOW_LIMIT = 80;
  // Below this zoom, labels are too small to read: drop them and draw
  // the map as coloured blocks, which is also far cheaper to repaint.
  var FAR_ZOOM = 0.5;

  // The same curve as --ease-out, for camera moves driven from script.
  var easeOut = (function (x1, y1, x2, y2) {
    function bez(t, a, b) { var u = 1 - t; return 3 * u * u * t * a + 3 * u * t * t * b + t * t * t; }
    function dbez(t, a, b) { var u = 1 - t; return 3 * u * u * a + 6 * u * t * (b - a) + 3 * t * t * (1 - b); }
    return function (x) {
      var t = x;
      for (var i = 0; i < 8; i++) {
        var d = dbez(t, x1, x2);
        if (Math.abs(d) < 1e-6) break;
        t = Math.min(1, Math.max(0, t - (bez(t, x1, x2) - x) / d));
      }
      return bez(t, y1, y2);
    };
  })(0.32, 0.72, 0, 1);

  function nodeChanged(n) {
    var inst = byResource.get(n.id) || [];
    return inst.some(function (c) { return !isUnchanged(c) && (!isCosmetic(c) || state.cosmetic); });
  }

  function visibleNodes() {
    if (state.mapScope === "all") return graphNodes.map(function (n) { return n.id; });
    var focus = selectedResource();
    if (state.mapScope === "focus" && focus && nodeById.has(focus)) {
      var around = new Set([focus]);
      transitive(focus, ups).forEach(function (n) { around.add(n); });
      transitive(focus, downs).forEach(function (n) { around.add(n); });
      return graphNodes.map(function (n) { return n.id; }).filter(function (id) { return around.has(id); });
    }
    var keep = new Set();
    graphNodes.forEach(function (n) { if (nodeChanged(n)) keep.add(n.id); });
    Array.from(keep).forEach(function (id) {
      (ups.get(id) || []).forEach(function (n) { keep.add(n); });
      (downs.get(id) || []).forEach(function (n) { keep.add(n); });
    });
    return graphNodes.map(function (n) { return n.id; }).filter(function (id) { return keep.has(id); });
  }

  function laneKey(n) {
    if (state.mapGroup === "module") return n.module || "";
    if (state.mapGroup === "category") return n.category || "other";
    return "";
  }

  function laneLabel(key) {
    if (state.mapGroup === "module") return key || "root module";
    if (state.mapGroup === "category") return key;
    return "";
  }

  function layout(ids) {
    var set = new Set(ids);
    var preds = new Map(), succs = new Map();
    ids.forEach(function (id) { preds.set(id, []); succs.set(id, []); });
    graphEdges.forEach(function (e) {
      if (!set.has(e[0]) || !set.has(e[1]) || e[0] === e[1]) return;
      preds.get(e[1]).push(e[0]);
      succs.get(e[0]).push(e[1]);
    });
    // Longest-path layering (Kahn), tolerant of cycles.
    var indeg = new Map(ids.map(function (id) { return [id, preds.get(id).length]; }));
    var queue = ids.filter(function (id) { return indeg.get(id) === 0; });
    var layer = new Map();
    while (queue.length) {
      var id = queue.shift();
      var l = 0;
      preds.get(id).forEach(function (p) { if (layer.has(p)) l = Math.max(l, layer.get(p) + 1); });
      layer.set(id, l);
      succs.get(id).forEach(function (n) { indeg.set(n, indeg.get(n) - 1); if (indeg.get(n) === 0) queue.push(n); });
    }
    ids.forEach(function (id) { if (!layer.has(id)) layer.set(id, 0); });
    // Pull sources right, next to what they feed, so edges stay short.
    for (var pass = 0; pass < 2; pass++) {
      ids.slice().reverse().forEach(function (id) {
        if (preds.get(id).length) return;
        var next = succs.get(id).map(function (n) { return layer.get(n); });
        if (next.length) layer.set(id, Math.max(0, Math.min.apply(null, next) - 1));
      });
    }
    var laneKeys = [];
    var laneOf = new Map();
    ids.forEach(function (nid) {
      var k = laneKey(nodeById.get(nid));
      laneOf.set(nid, k);
      if (laneKeys.indexOf(k) < 0) laneKeys.push(k);
    });
    laneKeys.sort(function (a, b) {
      if (state.mapGroup === "category") return CATEGORY_ORDER.indexOf(a) - CATEGORY_ORDER.indexOf(b);
      if (a === "") return -1;
      if (b === "") return 1;
      return a < b ? -1 : a > b ? 1 : 0;
    });
    var maxLayer = 0;
    layer.forEach(function (lv) { maxLayer = Math.max(maxLayer, lv); });
    // Cells: lane by layer, ordered by barycentre sweeps.
    var cells = new Map();
    function cellKey(k, lv) { return k + "\u0000" + lv; }
    ids.slice().sort(function (a, b) { return a < b ? -1 : a > b ? 1 : 0; }).forEach(function (nid) {
      var key = cellKey(laneOf.get(nid), layer.get(nid));
      if (!cells.has(key)) cells.set(key, []);
      cells.get(key).push(nid);
    });
    var laneIndex = new Map(laneKeys.map(function (k, i) { return [k, i]; }));
    function rank(nid) {
      var cell = cells.get(cellKey(laneOf.get(nid), layer.get(nid)));
      return laneIndex.get(laneOf.get(nid)) * 10000 + cell.indexOf(nid);
    }
    function sweep(neighbours, layers) {
      layers.forEach(function (lv) {
        laneKeys.forEach(function (k) {
          var cell = cells.get(cellKey(k, lv));
          if (!cell || cell.length < 2) return;
          var bary = new Map();
          cell.forEach(function (nid, i) {
            var ns = neighbours.get(nid);
            bary.set(nid, ns.length ? ns.reduce(function (sum, n) { return sum + rank(n); }, 0) / ns.length : rank(nid) - 0.5 + i * 0.001);
          });
          cell.sort(function (a, b) { return bary.get(a) - bary.get(b); });
        });
      });
    }
    var forward = [], backward = [];
    for (var i = 1; i <= maxLayer; i++) forward.push(i);
    for (var j = maxLayer - 1; j >= 0; j--) backward.push(j);
    for (var it = 0; it < 4; it++) { sweep(preds, forward); sweep(succs, backward); }
    var pos = new Map(), lanes = [];
    var y = MARGIN;
    laneKeys.forEach(function (k) {
      var rows = 1;
      for (var lv = 0; lv <= maxLayer; lv++) { var c = cells.get(cellKey(k, lv)); if (c) rows = Math.max(rows, c.length); }
      var head = state.mapGroup === "none" ? LANE_PAD : LANE_HEAD;
      var height = head + rows * (NODE_H + GAP_Y) - GAP_Y + LANE_PAD;
      for (var l2 = 0; l2 <= maxLayer; l2++) {
        var cell = cells.get(cellKey(k, l2));
        if (!cell) continue;
        var offset = (rows - cell.length) * (NODE_H + GAP_Y) / 2;
        cell.forEach(function (nid, idx) {
          pos.set(nid, { x: MARGIN + 16 + l2 * (NODE_W + GAP_X), y: y + head + offset + idx * (NODE_H + GAP_Y), idx: idx });
        });
      }
      lanes.push({ key: k, label: laneLabel(k), y: y, h: height });
      y += height + LANE_GAP;
    });
    var width = MARGIN * 2 + 32 + (maxLayer + 1) * NODE_W + maxLayer * GAP_X;
    var edges = [];
    graphEdges.forEach(function (e) { if (set.has(e[0]) && set.has(e[1]) && e[0] !== e[1]) edges.push(e); });
    return { pos: pos, lanes: lanes, laneOf: laneOf, width: width, height: y - LANE_GAP + MARGIN, edges: edges };
  }

  // Orthogonal step edges, as on a schematic: out of the source, along a
  // channel just before the target, and in. Neighbouring target rows get
  // different channels, so edges into different resources do not share a
  // vertical run; edges into the same resource merge into one trunk.
  function stepPath(x1, y1, x2, y2, xm) {
    var dy = y2 - y1;
    if (Math.abs(dy) < 0.5) return "M" + x1 + " " + y1 + " H" + x2;
    var sy = dy > 0 ? 1 : -1;
    var r = Math.max(0, Math.min(8, Math.abs(dy) / 2, xm - x1, x2 - xm));
    return "M" + x1 + " " + y1 + " H" + (xm - r) +
      " Q" + xm + " " + y1 + " " + xm + " " + (y1 + sy * r) +
      " V" + (y2 - sy * r) +
      " Q" + xm + " " + y2 + " " + (xm + r) + " " + y2 +
      " H" + x2;
  }

  function edgePath(a, b) {
    var x1 = a.x + NODE_W, y1 = a.y + NODE_H / 2, x2 = b.x - 1, y2 = b.y + NODE_H / 2;
    if (x2 > x1 + 16) {
      var channel = (((b.idx || 0) % 5) - 2) * 7;
      var xm = Math.max(x1 + 10, Math.min(x2 - 14, b.x - GAP_X / 2 + channel));
      return stepPath(x1, y1, x2, y2, xm);
    }
    // Same column or backwards (cycles, pulled-in sources): out to the
    // right, under both, and back in from the left.
    var below = Math.max(a.y, b.y) + NODE_H + 10;
    return "M" + x1 + " " + y1 + " H" + (x1 + 18) + " V" + below + " H" + (b.x - 18) + " V" + y2 + " H" + x2;
  }

  // Corner brackets around a box, like the crop marks of a viewfinder.
  function bracketPath(x, y, w, hgt, arm) {
    return "M" + x + " " + (y + arm) + " V" + y + " H" + (x + arm) +
      " M" + (x + w - arm) + " " + y + " H" + (x + w) + " V" + (y + arm) +
      " M" + (x + w) + " " + (y + hgt - arm) + " V" + (y + hgt) + " H" + (x + w - arm) +
      " M" + (x + arm) + " " + (y + hgt) + " H" + x + " V" + (y + hgt - arm);
  }

  function mapView() {
    var wrap = h("div");
    var row = h("div", { class: "filters", role: "group", "aria-label": "Map options" });
    var scopes = h("div", { class: "options", role: "group", "aria-label": "Show" }, h("span", { class: "opt-label", "aria-hidden": "true" }, "Show"));
    [["changed", "Changes and neighbours", "Changed resources and their direct neighbours"], ["focus", "Around the selection", "Only what the selected resource depends on and what depends on it"], ["all", "Everything", "Every resource in the configuration"]].forEach(function (sc) {
      scopes.appendChild(h("button", { type: "button", title: sc[2], "aria-pressed": state.mapScope === sc[0] ? "true" : "false", onclick: function () { state.mapScope = sc[0]; state.view = null; renderMain(); } }, sc[1]));
    });
    row.appendChild(scopes);
    var groups = h("div", { class: "options", role: "group", "aria-label": "Lanes" }, h("span", { class: "opt-label", "aria-hidden": "true" }, "Lanes"));
    [["module", "By module", "A lane per module"], ["category", "By kind", "A lane per kind of resource: network, compute, data and so on"], ["none", "None", "One canvas, no lanes"]].forEach(function (g) {
      groups.appendChild(h("button", { type: "button", title: g[2], "aria-pressed": state.mapGroup === g[0] ? "true" : "false", onclick: function () { state.mapGroup = g[0]; state.view = null; renderMain(); } }, g[1]));
    });
    row.appendChild(groups);
    if (changes.filter(isCosmetic).length) {
      var cos = toggle("Count cosmetic updates", 0, state.cosmetic, function (v) { state.cosmetic = v; state.view = null; renderMain(); });
      cos.title = "Treat updates that only touch tags or formatting as changes";
      row.appendChild(cos);
    }
    wrap.appendChild(row);

    var ids = visibleNodes();
    var core = h("div", { class: "core" });
    wrap.appendChild(h("div", { class: "map-frame" }, core));
    if (!ids.length) {
      core.appendChild(h("div", { class: "empty map-empty" }, "Nothing to draw: this plan changes no resources."));
      return wrap;
    }
    var L = layout(ids);
    var svgEl = s("svg", { class: "map", role: "group", "aria-label": "Dependency map of " + plural(ids.length, "resource") });
    var defs = s("defs");
    // Graph paper: a fine 16-unit grid under a heavier 80-unit one.
    defs.appendChild(s("pattern", { id: "interragate-grid", width: "16", height: "16", patternUnits: "userSpaceOnUse" }, s("path", { class: "grid-minor", d: "M16 0.5H0.5V16" })));
    defs.appendChild(s("pattern", { id: "interragate-grid-major", width: "80", height: "80", patternUnits: "userSpaceOnUse" }, s("path", { class: "grid-major", d: "M80 0.5H0.5V80" })));
    [["arrow", "var(--edge)"], ["arrow-up", "var(--ink)"], ["arrow-down", "var(--act-delete)"]].forEach(function (m) {
      defs.appendChild(s("marker", { id: m[0], viewBox: "0 0 8 8", refX: "7.5", refY: "4", markerWidth: "6", markerHeight: "6", orient: "auto-start-reverse" }, s("path", { d: "M0 0.6 8 4 0 7.4z", style: "fill:" + m[1] })));
    });
    svgEl.appendChild(defs);
    var root = s("g", { class: "viewport" });
    svgEl.appendChild(root);
    root.appendChild(s("rect", { class: "grid-fine", x: -6000, y: -6000, width: L.width + 12000, height: L.height + 12000, fill: "url(#interragate-grid)" }));
    root.appendChild(s("rect", { x: -6000, y: -6000, width: L.width + 12000, height: L.height + 12000, fill: "url(#interragate-grid-major)" }));
    var tallies = [];
    if (state.mapGroup !== "none") {
      L.lanes.forEach(function (lane) {
        root.appendChild(s("rect", { class: "lane-band", x: MARGIN, y: lane.y, width: L.width - MARGIN * 2, height: lane.h }));
        var labelEl = s("text", { class: "lane-label", x: MARGIN + 18, y: lane.y + 22 }, lane.label);
        root.appendChild(labelEl);
        var tally = laneTally(lane.key, L);
        if (tally) { root.appendChild(tally); tallies.push({ label: labelEl, tally: tally, y: lane.y + 22 }); }
      });
    }
    var edgeLayer = s("g", { class: "edges" });
    root.appendChild(edgeLayer);
    var edgeEls = [];
    L.edges.forEach(function (e) {
      var a = L.pos.get(e[0]), b = L.pos.get(e[1]);
      var pa = presence(nodeById.get(e[0])), pb = presence(nodeById.get(e[1]));
      var cls = "edge" + (pa.before && pb.before ? "" : " absent-before") + (pa.after && pb.after ? "" : " absent-after") + (nodeOrigin(e[0]) && nodeOrigin(e[1]) ? " o-edit" : "");
      var path = s("path", { class: cls, d: edgePath(a, b), "marker-end": "url(#arrow)" });
      path.__edge = e;
      path.__box = [Math.min(a.x, b.x), Math.min(a.y, b.y), Math.max(a.x, b.x) + NODE_W + 60, Math.max(a.y, b.y) + NODE_H];
      edgeLayer.appendChild(path);
      edgeEls.push(path);
    });
    // Nodes are grouped by lane, so a lane scrolled out of view can be
    // dropped from painting in one write.
    var nodeLayer = s("g", { class: "nodes" });
    root.appendChild(nodeLayer);
    var nodeEls = new Map(), laneGroups = [];
    var groupOf = new Map();
    L.lanes.forEach(function (lane) {
      var g = s("g");
      g.__box = [lane.y, lane.y + lane.h];
      nodeLayer.appendChild(g);
      laneGroups.push(g);
      groupOf.set(lane.key, g);
    });
    ids.forEach(function (id) { var g = nodeCard(id, L.pos.get(id)); groupOf.get(L.laneOf.get(id)).appendChild(g); nodeEls.set(id, g); });
    core.appendChild(svgEl);

    core.appendChild(h("div", { class: "float tools" },
      toolButton("Zoom in", "plus", function () { zoomBy(1.25, undefined, undefined, true); }),
      toolButton("Zoom out", "minus", function () { zoomBy(0.8, undefined, undefined, true); }),
      toolButton("Fit everything", "fit", function () { fit(true); }),
      toolButton("Save the map as an image", "download", function () { saveMapImage("png"); })));
    var when = h("div", { class: "float when", role: "group", "aria-label": "Draw" });
    var whenButtons = new Map();
    var playBtn = null, clockEl = null;
    var modes = [["plan", "Plan", "The plan's changes"], ["before", "Before", "The infrastructure as it is now, before the apply (b)"], ["after", "After", "The infrastructure after the apply (b)"]];
    if (changes.some(function (c) { return c.edit; })) modes.splice(1, 0, ["edits", "Edits", "Your edits and what changes because of them (e)"]);
    modes.forEach(function (m) {
      if (m[0] === "after" && applyData) {
        // Between before and after, the apply itself.
        clockEl = h("span", { class: "play-clock", "aria-live": "off" });
        playBtn = h("button", { type: "button", class: "play", title: "Play the apply, from before to after (p)", "aria-label": "Play the apply", onclick: function () { togglePlay(); } }, icon("play"), clockEl);
        when.appendChild(playBtn);
      }
      var b = h("button", { type: "button", title: m[2], "aria-pressed": "false", onclick: function () { stopPlay(); setMapTime(m[0]); } }, m[1]);
      whenButtons.set(m[0], b);
      when.appendChild(b);
    });
    core.appendChild(when);
    var progress = h("div", { class: "play-progress", "aria-hidden": "true" });
    core.appendChild(progress);
    var legend = h("div", { class: "float legend", role: "list", "aria-label": "Legend" });
    core.appendChild(legend);
    var mini = minimap(L);
    core.appendChild(mini.el);

    var note = h("p", { class: "map-note" });
    wrap.appendChild(note);

    mapEls = { svg: svgEl, root: root, nodes: nodeEls, edges: edgeEls, lanes: laneGroups, layout: L, mini: mini, tallies: tallies, far: null, size: null, when: whenButtons, legend: legend, note: note, play: playBtn, clock: clockEl, progress: progress };
    applyMapTime();
    svgEl.addEventListener("click", function () {
      if (Date.now() < suppressClickUntil) return;
      if (state.sel) { state.sel = null; renderDetail(); syncRows(); highlight(); syncHash(); }
    });
    enablePanZoom(svgEl);
    highlight();
    return wrap;
  }

  // Before and after: the same drawing, with what does not exist yet (or
  // any more) left as a dashed outline, so flipping between the two shows
  // what the apply does. Positions never move, so the eye can compare.
  function setMapTime(t) {
    state.mapTime = t;
    applyMapTime();
  }

  var LEGENDS = {
    plan: function (legend) {
      [["create", "+", "create"], ["update", "~", "update"], ["replace", "-/+", "replace"], ["delete", "−", "destroy"]].forEach(function (k) {
        legend.appendChild(h("span", { role: "listitem" }, tileMark(k[0], k[1], k[2]), k[2]));
      });
      legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile noop", "aria-hidden": "true" }, "·"), "context"));
    },
    before: function (legend) {
      legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile plain", "aria-hidden": "true" }), "exists now"));
      legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile ghost", "aria-hidden": "true" }), "created by this plan"));
    },
    after: function (legend) {
      legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile plain", "aria-hidden": "true" }), "after the apply"));
      legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile ghost", "aria-hidden": "true" }), "destroyed by this plan"));
    },
  };

  LEGENDS.edits = function (legend) {
    legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile plain edited", "aria-hidden": "true" }), "your edit"));
    legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile plain", "aria-hidden": "true" }), "because of your edit"));
    legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile plain faint", "aria-hidden": "true" }), "not tied to an edit"));
  };

  LEGENDS.apply = function (legend) {
    legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile plain running", "aria-hidden": "true" }), "running now"));
    legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile plain", "aria-hidden": "true" }), "exists"));
    legend.appendChild(h("span", { role: "listitem" }, h("span", { class: "tile ghost", "aria-hidden": "true" }), "does not exist yet, or any more"));
  };

  var NOTES = {
    apply: "Playing the apply in interragate's simulated order: each resource lights up while its step runs. Long waits are shortened; the Apply view has the real proportions. Space pauses.",
    plan: "Arrows run from a resource to what depends on it. Select one: what it reads turns solid, and its blast radius flows in pink.",
    before: "Before the apply: what exists now. Dashed outlines are what this plan creates. Press b to flip to after.",
    edits: "Your edits are outlined, and the changes that follow from them keep their colour. What is faded changes for another reason: nothing you edited explains it. Press e to go back to the plan.",
    after: "After the apply. Dashed outlines are what this plan destroys, and counts show the instances left. Press b to flip back.",
  };

  function applyMapTime() {
    if (!mapEls) return;
    var t = state.mapTime;
    if (t === "apply" && !player) t = state.mapTime = "after";
    mapEls.svg.classList.toggle("t-before", t === "before");
    mapEls.svg.classList.toggle("t-after", t === "after");
    mapEls.svg.classList.toggle("t-apply", t === "apply");
    mapEls.svg.classList.toggle("t-edits", t === "edits");
    mapEls.when.forEach(function (b, k) { b.setAttribute("aria-pressed", k === t ? "true" : "false"); });
    mapEls.nodes.forEach(function (g) {
      var c = g.__counts;
      var at = t === "edits" ? "plan" : t;
      if (c && t !== "apply") c.el.textContent = c[at] > 1 ? "×" + c[at] : "";
    });
    mapEls.legend.textContent = "";
    LEGENDS[t](mapEls.legend);
    mapEls.note.textContent = NOTES[t];
    highlight();
  }

  // Playing the apply: the map from before to after, in the order
  // interragate's simulation runs the steps. Each resource shows its step's
  // colour and symbol while it runs, what does not exist at that moment is
  // an outline, and the clock reads simulated time. Playback moves from
  // event to event rather than in real proportion, which would spend most of
  // its time waiting on the slowest step; the Apply view keeps the
  // proportions.
  var player = null;
  var OP_KIND = { create: "create", update: "update", destroy: "delete", read: "other" };
  var OP_GLYPH = { create: "+", update: "~", destroy: "−", read: "<=" };

  function togglePlay() {
    if (!player) return startPlay();
    if (player.paused) resumePlay();
    else pausePlay();
  }

  function startPlay() {
    if (!mapEls || !applyData) return;
    var byNode = new Map();
    applyData.steps.forEach(function (st) {
      var c = byAddr.get(st.address);
      var id = c && c.resource;
      if (!id || !mapEls.nodes.has(id)) return;
      if (!byNode.has(id)) byNode.set(id, []);
      byNode.get(id).push({ start: st.start, end: st.start + st.duration, op: st.op });
    });
    var times = new Set([0, applyData.duration || 0]);
    applyData.steps.forEach(function (st) { times.add(st.start); times.add(st.start + st.duration); });
    var events = Array.from(times).sort(function (a, b) { return a - b; });
    player = {
      byNode: byNode, events: events, k: 0, paused: false, raf: 0, last: new Map(),
      per: Math.max(120, Math.min(400, 14000 / Math.max(1, events.length))),
      origin: performance.now(),
    };
    state.mapTime = "apply";
    applyMapTime();
    mapEls.play.classList.add("on");
    mapEls.play.setAttribute("aria-label", "Pause the apply");
    swapIcon("pause");
    player.raf = requestAnimationFrame(playFrame);
  }

  function swapIcon(name) {
    if (!mapEls || !mapEls.play) return;
    var old = mapEls.play.querySelector("svg");
    if (old) mapEls.play.replaceChild(icon(name), old);
  }

  function pausePlay() {
    if (!player) return;
    player.paused = true;
    cancelAnimationFrame(player.raf);
    swapIcon("play");
    mapEls.play.setAttribute("aria-label", "Resume the apply");
  }

  function resumePlay() {
    if (!player) return;
    player.paused = false;
    player.origin = performance.now() - player.k * player.per;
    swapIcon("pause");
    mapEls.play.setAttribute("aria-label", "Pause the apply");
    player.raf = requestAnimationFrame(playFrame);
  }

  // stopPlay ends playback and puts every node back as the map drew it.
  function stopPlay() {
    if (!player) return;
    cancelAnimationFrame(player.raf);
    if (mapEls) {
      player.byNode.forEach(function (_, id) { resetNode(mapEls.nodes.get(id)); });
      mapEls.play.classList.remove("on");
      mapEls.play.setAttribute("aria-label", "Play the apply");
      mapEls.clock.textContent = "";
      mapEls.progress.style.transform = "scaleX(0)";
      swapIcon("play");
    }
    player = null;
  }

  function playFrame(now) {
    if (!player || !mapEls) return;
    var ev = player.events;
    player.k = (now - player.origin) / player.per;
    var i = Math.floor(player.k), f = player.k - i;
    var done = i >= ev.length - 1;
    var t = done ? ev[ev.length - 1] : ev[i] + (ev[i + 1] - ev[i]) * f;
    showTime(t);
    if (done) {
      // The apply is over: leave the map drawing what it left behind.
      stopPlay();
      setMapTime("after");
      return;
    }
    player.raf = requestAnimationFrame(playFrame);
  }

  function showTime(t) {
    var total = Math.max(1, applyData.duration || 1);
    mapEls.clock.textContent = clock(t);
    mapEls.progress.style.transform = "scaleX(" + Math.min(1, t / total).toFixed(4) + ")";
    player.byNode.forEach(function (steps, id) {
      var g = mapEls.nodes.get(id);
      if (!g) return;
      var n = nodeById.get(id);
      var count = presence(n).before, running = null;
      steps.forEach(function (st) {
        if (st.end <= t) count += st.op === "create" ? 1 : st.op === "destroy" ? -1 : 0;
        else if (st.start <= t && !running) running = st.op;
      });
      var key = (running || "") + "|" + (count > 0 ? 1 : 0);
      if (player.last.get(id) === key) return;
      player.last.set(id, key);
      g.classList.toggle("p-ghost", count <= 0 && !running);
      g.classList.toggle("p-run", !!running);
      var cell = g.querySelector(".cell");
      var glyph = g.querySelector(".glyph");
      if (running && cell) cell.style.fill = "var(--act-" + OP_KIND[running] + ")";
      if (running && glyph) { glyph.textContent = OP_GLYPH[running] || "·"; glyph.style.fill = "var(--on-" + OP_KIND[running] + ")"; }
    });
  }

  function resetNode(g) {
    if (!g) return;
    g.classList.remove("p-ghost", "p-run");
    var cell = g.querySelector(".cell");
    if (cell) cell.style.fill = cell.getAttribute("data-fill") || cell.style.fill;
    var glyph = g.querySelector(".glyph");
    if (glyph && glyph.getAttribute("data-text") != null) { glyph.textContent = glyph.getAttribute("data-text"); glyph.style.fill = glyph.getAttribute("data-fill"); }
  }

  // Saving the map: the whole drawing, as it looks now (before or after,
  // with the selection), with a header that says what it is, so the image
  // explains itself in a pull request or a chat. Computed styles are copied
  // onto a clone, which resolves the CSS variables the page uses, and the
  // fonts travel inside the SVG so the PNG renders them.
  var STYLE_PROPS = ["fill", "fill-opacity", "stroke", "stroke-width", "stroke-dasharray", "stroke-opacity", "opacity", "font-family", "font-size", "font-weight", "letter-spacing", "text-anchor", "visibility", "display"];

  function inlineStyles(a, b) {
    var cs = getComputedStyle(a);
    var parts = [];
    STYLE_PROPS.forEach(function (prop) {
      var v = cs.getPropertyValue(prop);
      if (v) parts.push(prop + ":" + v);
    });
    b.setAttribute("style", parts.join(";"));
    b.removeAttribute("class");
    if (a.tagName === "text" && cs.textTransform === "uppercase") b.textContent = a.textContent.toUpperCase();
    for (var i = 0; i < a.children.length; i++) inlineStyles(a.children[i], b.children[i]);
  }

  function fontFaces() {
    var out = [];
    Array.prototype.forEach.call(document.styleSheets, function (sheet) {
      var rules;
      try { rules = sheet.cssRules; } catch (e) { return; }
      Array.prototype.forEach.call(rules || [], function (r) {
        if (r.type === 5 && !/Geist Pixel/.test(r.cssText)) out.push(r.cssText);
      });
    });
    return out.join("\n");
  }

  function saveMapImage(kind) {
    if (!mapEls) return;
    var svg = mapEls.svg, L = mapEls.layout, HEAD = 60;
    // Everything is drawn for the copy: labels even when zoomed out, and
    // what culling left off screen. Nothing paints before it is undone.
    var far = svg.classList.contains("far");
    svg.classList.remove("far");
    mapEls.lanes.forEach(function (g) { g.style.display = ""; g.__hidden = false; });
    mapEls.edges.forEach(function (p) { p.style.display = ""; p.__hidden = false; });
    var clone = svg.cloneNode(true);
    inlineStyles(svg, clone);
    if (far) svg.classList.add("far");
    cull();

    var cs = getComputedStyle(document.documentElement);
    var color = function (name) { return cs.getPropertyValue(name).trim(); };
    var W = Math.ceil(L.width), H = Math.ceil(L.height) + HEAD;
    var out = s("svg", { xmlns: "http://www.w3.org/2000/svg", width: W, height: H, viewBox: "0 0 " + W + " " + H });
    out.appendChild(s("style", null, fontFaces()));
    out.appendChild(s("rect", { width: W, height: H, style: "fill:" + color("--page") }));
    Array.prototype.slice.call(clone.childNodes).forEach(function (n) { out.appendChild(n); });
    var view = out.querySelector("g");
    out.querySelectorAll("g").forEach(function (g) { if (g.getAttribute("transform") && /scale/.test(g.getAttribute("transform"))) view = g; });
    view.setAttribute("transform", "translate(0," + HEAD + ")");
    // The header sits on top of the grid that runs under it.
    var head = s("g");
    head.appendChild(s("rect", { width: W, height: HEAD, style: "fill:" + color("--page") }));
    head.appendChild(s("text", { x: 24, y: 37, style: "font:600 17px Geist, sans-serif;fill:" + color("--ink") }, "interragate"));
    var when = { plan: "planned changes", edits: "your edits", before: "before the apply", after: "after the apply", apply: "during the apply" }[state.mapTime] || "planned changes";
    var facts = [plan.dir, plan.workspace && plan.workspace !== "default" ? "workspace " + plan.workspace : "", when, plan.timestamp ? new Date(plan.timestamp).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : ""].filter(Boolean).join("  ·  ");
    head.appendChild(s("text", { x: 124, y: 37, style: "font:400 14px Geist, sans-serif;fill:" + color("--muted") }, facts));
    if (state.mapTime === "plan") {
      var x = W - 24;
      [["delete", "−", "destroy"], ["replace", "-/+", "replace"], ["update", "~", "update"], ["create", "+", "create"]].forEach(function (k) {
        var label = s("text", { x: x, y: 36, "text-anchor": "end", style: "font:500 12px Geist, sans-serif;fill:" + color("--ink-2") }, k[2]);
        head.appendChild(label);
        x -= k[2].length * 7 + 10;
        head.appendChild(s("rect", { x: x - 14, y: 25, width: 14, height: 14, style: "fill:" + color("--act-" + k[0]) }));
        x -= 34;
      });
    }
    out.appendChild(head);

    var name = (plan.dir || "plan").replace(/[^\w.-]+/g, "-") + "-map";
    var xml = new XMLSerializer().serializeToString(out);
    var blob = new Blob([xml], { type: "image/svg+xml;charset=utf-8" });
    if (kind === "svg") { download(blob, name + ".svg"); return; }
    var url = URL.createObjectURL(blob);
    var img = new Image();
    img.onload = function () {
      // Twice the size for sharp text, within what browsers can draw.
      var scale = Math.max(0.5, Math.min(2, 8000 / W, 8000 / H));
      var canvas = document.createElement("canvas");
      canvas.width = Math.round(W * scale);
      canvas.height = Math.round(H * scale);
      var ctx = canvas.getContext("2d");
      ctx.scale(scale, scale);
      ctx.drawImage(img, 0, 0);
      URL.revokeObjectURL(url);
      canvas.toBlob(function (png) { if (png) download(png, name + ".png"); }, "image/png");
    };
    img.onerror = function () { URL.revokeObjectURL(url); download(blob, name + ".svg"); };
    img.src = url;
  }

  // A summary card to drop into a chat or a ticket: the verdict, the
  // headline, every changed resource as a square, and what matters most, in
  // the page's own type and colours.
  function saveSummaryImage() {
    var cs = getComputedStyle(document.documentElement);
    var color = function (name) { return cs.getPropertyValue(name).trim(); };
    var W = 1200, PAD = 64, inner = W - PAD * 2;
    var measure = document.createElement("canvas").getContext("2d");
    function wrap(text, font, width, max) {
      measure.font = font;
      var out = [], line = "";
      var fits = function (t) { return measure.measureText(t).width <= width; };
      text.split(/\s+/).forEach(function (w) {
        var next = line ? line + " " + w : w;
        if (fits(next)) { line = next; return; }
        if (line) { out.push(line); line = ""; }
        // An address wider than the card breaks after a dot, an underscore
        // or a bracket, or anywhere if it must, rather than running off.
        while (!fits(w)) {
          var cut = 1;
          while (cut < w.length && fits(w.slice(0, cut + 1))) cut++;
          var soft = Math.max(w.lastIndexOf(".", cut - 1), w.lastIndexOf("_", cut - 1), w.lastIndexOf("[", cut - 1));
          if (soft > cut / 2) cut = soft + 1;
          out.push(w.slice(0, cut));
          w = w.slice(cut);
        }
        line = w;
      });
      if (line) out.push(line);
      if (out.length > max) { out = out.slice(0, max); out[max - 1] = out[max - 1].replace(/\s*\S*$/, "") + "…"; }
      return out;
    }
    var flat = function (t) { return t.replace(/`/g, ""); };
    var g = s("g");
    var y = PAD;

    var facts = [plan.dir, (TOOL + " " + (plan.version || "")).trim(), git && git.branch ? "branch " + git.branch : "", plan.timestamp ? "planned " + new Date(plan.timestamp).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : ""].filter(Boolean).join("  ·  ");
    g.appendChild(s("text", { x: PAD, y: y, style: "font:600 18px Geist, sans-serif;fill:" + color("--ink") }, "interragate"));
    g.appendChild(s("text", { x: W - PAD, y: y, "text-anchor": "end", style: "font:400 14px Geist, sans-serif;fill:" + color("--muted") }, facts));
    y += 72;

    var level = (data.risk || {}).level || "none";
    g.appendChild(sevShape(level, PAD + 8, y - 6, color(level === "info" || level === "none" ? "--sev-low" : "--sev-" + level)));
    g.appendChild(s("text", { x: PAD + 26, y: y, style: "font:600 18px Geist, sans-serif;fill:" + color("--ink") }, riskLabel(level)));
    y += 58;
    wrap(flat((data.risk || {}).headline || ""), "600 44px Geist", inner, 3).forEach(function (line) {
      g.appendChild(s("text", { x: PAD, y: y, style: "font:600 44px Geist, sans-serif;letter-spacing:-0.02em;fill:" + color("--ink") }, line));
      y += 52;
    });
    y += 20;

    // The inventory, row by row, squares shaped as on the page.
    var rows = { "delete": [], replace: [], update: [], create: [] };
    changes.forEach(function (c) { var k = actionKind(c.action); if (!isUnchanged(c) && rows[k]) rows[k].push(c); });
    var WORD = { "delete": "destroyed", replace: "replaced", update: "updated", create: "created" };
    var U = 14, GAP = 4, LABEL = 120, perRow = Math.floor((inner - LABEL) / (U + GAP));
    ["delete", "replace", "update", "create"].forEach(function (k) {
      // Updates that only touch tags or formatting come last, as on the page.
      var items = rows[k].filter(function (c) { return !isCosmetic(c); }).concat(rows[k].filter(isCosmetic));
      if (!items.length) return;
      g.appendChild(s("text", { x: PAD + LABEL - 16, y: y + 12, "text-anchor": "end", style: "font:500 14px Geist, sans-serif;fill:" + color("--ink-2") }, items.length + " " + WORD[k]));
      items.slice(0, perRow).forEach(function (c, i) {
        g.appendChild(squareShape(k, isCosmetic(c), PAD + LABEL + i * (U + GAP), y, U, color("--act-" + k)));
      });
      if (items.length > perRow) g.appendChild(s("text", { x: PAD + LABEL + perRow * (U + GAP) + 4, y: y + 12, style: "font:500 13px Geist, sans-serif;fill:" + color("--muted") }, "+" + (items.length - perRow)));
      y += U + 10;
    });
    y += 44;

    // What matters most: the worst few sentences.
    (data.narrative || []).filter(function (it) { return sevRank[it.severity] >= 2; }).slice(0, 3).forEach(function (it) {
      var lines = wrap(flat(it.text), "400 17px Geist", inner - 28, 2);
      g.appendChild(sevShape(it.severity, PAD + 7, y - 6, color("--sev-" + it.severity)));
      lines.forEach(function (line) {
        g.appendChild(s("text", { x: PAD + 28, y: y, style: "font:400 17px Geist, sans-serif;fill:" + color("--ink-2") }, line));
        y += 26;
      });
      y += 10;
    });
    var H = y + PAD - 26;

    var out = s("svg", { xmlns: "http://www.w3.org/2000/svg", width: W, height: H, viewBox: "0 0 " + W + " " + H });
    out.appendChild(s("style", null, fontFaces()));
    out.appendChild(s("rect", { width: W, height: H, style: "fill:" + color("--page") }));
    out.appendChild(g);
    var name = (plan.dir || "plan").replace(/[^\w.-]+/g, "-") + "-summary";
    var blob = new Blob([new XMLSerializer().serializeToString(out)], { type: "image/svg+xml;charset=utf-8" });
    var url = URL.createObjectURL(blob);
    var img = new Image();
    img.onload = function () {
      var canvas = document.createElement("canvas");
      canvas.width = W * 2;
      canvas.height = H * 2;
      var ctx = canvas.getContext("2d");
      ctx.scale(2, 2);
      ctx.drawImage(img, 0, 0);
      URL.revokeObjectURL(url);
      canvas.toBlob(function (png) { if (png) download(png, name + ".png"); }, "image/png");
    };
    img.onerror = function () { URL.revokeObjectURL(url); download(blob, name + ".svg"); };
    img.src = url;
  }

  // The severity shapes the page uses, centred on x, y: octagon, triangle,
  // diamond and dot.
  function sevShape(sev, x, y, fill) {
    var r = 7;
    if (sev === "critical") {
      var pts = [];
      for (var i = 0; i < 8; i++) { var a = Math.PI / 8 + i * Math.PI / 4; pts.push((x + r * Math.cos(a)).toFixed(2) + "," + (y + r * Math.sin(a)).toFixed(2)); }
      return s("polygon", { points: pts.join(" "), style: "fill:" + fill });
    }
    if (sev === "high") return s("polygon", { points: x + "," + (y - r) + " " + (x + r) + "," + (y + r * 0.8) + " " + (x - r) + "," + (y + r * 0.8), style: "fill:" + fill });
    if (sev === "medium") return s("polygon", { points: x + "," + (y - r) + " " + (x + r) + "," + y + " " + x + "," + (y + r) + " " + (x - r) + "," + y, style: "fill:" + fill });
    return s("circle", { cx: x, cy: y, r: 5, style: "fill:" + fill });
  }

  // squareShape draws one inventory square: crossed out when destroyed,
  // half filled when replaced, hollow for a tags-only update, solid
  // otherwise.
  function squareShape(kind, cosmetic, x, y, u, fill) {
    var g = s("g");
    var stroke = "fill:none;stroke:" + fill + ";stroke-width:1.5";
    if (kind === "delete") {
      g.appendChild(s("rect", { x: x + 0.75, y: y + 0.75, width: u - 1.5, height: u - 1.5, style: stroke }));
      g.appendChild(s("path", { d: "M" + x + " " + y + "L" + (x + u) + " " + (y + u) + "M" + (x + u) + " " + y + "L" + x + " " + (y + u), style: stroke }));
    } else if (kind === "replace") {
      g.appendChild(s("rect", { x: x + 0.75, y: y + 0.75, width: u - 1.5, height: u - 1.5, style: stroke }));
      g.appendChild(s("path", { d: "M" + (x + u) + " " + y + "L" + (x + u) + " " + (y + u) + "L" + x + " " + (y + u) + "Z", style: "fill:" + fill }));
    } else if (cosmetic) {
      g.appendChild(s("rect", { x: x + 0.75, y: y + 0.75, width: u - 1.5, height: u - 1.5, style: stroke }));
    } else {
      g.appendChild(s("rect", { x: x, y: y, width: u, height: u, style: "fill:" + fill }));
    }
    return g;
  }

  function download(blob, filename) {
    var a = h("a", { href: URL.createObjectURL(blob), download: filename, style: "display:none" });
    document.body.appendChild(a);
    a.click();
    setTimeout(function () { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
  }

  // Each lane's changed resources by action, as swatches and counts placed
  // after its label once the label can be measured.
  function laneTally(key, L) {
    var t = { create: 0, update: 0, replace: 0, delete: 0 };
    L.laneOf.forEach(function (k, id) {
      if (k !== key) return;
      var n = nodeById.get(id);
      if (!nodeChanged(n)) return;
      var kind = actionKind(n.action);
      if (kind in t) t[kind]++;
    });
    var g = s("g", { class: "lane-tally" });
    var x = 0;
    ["create", "update", "replace", "delete"].forEach(function (k) {
      if (!t[k]) return;
      g.appendChild(s("rect", { x: x, y: -8, width: 8, height: 8, rx: 2.5, style: "fill: var(--act-" + k + ")" }));
      g.appendChild(s("text", { x: x + 12, y: 0 }, String(t[k])));
      x += 12 + String(t[k]).length * 6.4 + 12;
    });
    return x ? g : null;
  }

  function placeTallies() {
    if (!mapEls) return;
    mapEls.tallies.forEach(function (t) {
      var w = 0;
      try { w = t.label.getComputedTextLength(); } catch (e) { w = t.label.textContent.length * 7.4; }
      t.tally.setAttribute("transform", "translate(" + (MARGIN + 18 + w + 14) + "," + t.y + ")");
    });
  }

  // nodeOrigin says where a resource's changes come from: "edit" when any
  // instance's block was edited, "follows" when one only changes because
  // of an edit, and "" otherwise.
  function nodeOrigin(id) {
    var origin = "";
    (byResource.get(id) || []).forEach(function (c) {
      // An instance the plan leaves alone says nothing about where the
      // change comes from, edited block or not.
      if (isUnchanged(c)) return;
      var k = editClass(c);
      if (k === "edit") origin = "edit";
      else if (k === "follows" && !origin) origin = "follows";
    });
    return origin;
  }

  // presence says how many of a resource's instances exist before and after
  // the apply. Forgotten objects still exist; they are only unmanaged.
  function presence(n) {
    var acts = (n && n.actions) || {};
    var total = Math.max(1, (n && n.instances || []).length);
    if (n && n.mode === "data") return { total: total, before: total, after: total };
    return { total: total, before: total - (acts.create || 0), after: total - (acts["delete"] || 0) };
  }

  function nodeCard(id, p) {
    var n = nodeById.get(id);
    var changed = nodeChanged(n);
    var kind = changed ? actionKind(n.action) : "other";
    var pres = presence(n);
    var origin = nodeOrigin(id);
    var g = s("g", {
      class: "node" + (changed ? "" : " context") + (kind === "delete" ? " gone" : "") + (pres.before ? "" : " absent-before") + (pres.after ? "" : " absent-after") + (origin ? " o-" + origin : ""),
      transform: "translate(" + p.x + "," + p.y + ")", tabindex: "0", role: "button", "aria-label": nodeAria(n),
    });
    g.appendChild(s("path", { class: "brackets", d: bracketPath(-6, -6, NODE_W + 12, NODE_H + 12, 10) }));
    g.appendChild(s("rect", { class: "card-bg", x: 0.5, y: 0.5, width: NODE_W - 1, height: NODE_H - 1 }));
    // The action cell: the action's colour and symbol in a square column.
    // Under it, a plain rule, for when the map draws before or after the
    // apply and actions are not the point.
    g.appendChild(s("path", { class: "cellrule", d: "M32 0.5V" + (NODE_H - 0.5) }));
    if (kind !== "other") g.appendChild(s("rect", { class: "cell", x: 0.5, y: 0.5, width: 31, height: NODE_H - 1, style: "fill: var(--act-" + kind + ")", "data-fill": "var(--act-" + kind + ")" }));
    var glyph = changed ? (GLYPH[n.action] || "·") : "·";
    if (n.action === "no-op" && n.actions && n.actions.move) glyph = "→";
    var glyphFill = kind === "other" ? "var(--muted)" : "var(--on-" + kind + ")";
    g.appendChild(s("text", { class: "glyph", x: 16, y: NODE_H / 2 + 4.5, "text-anchor": "middle", style: "fill: " + glyphFill, "data-fill": glyphFill, "data-text": glyph }, glyph));
    var typeLabel = n.mode === "data" ? "data." + n.type : n.type;
    g.appendChild(s("text", { class: "type", x: 44, y: 21 }, truncateMiddle(typeLabel, 27)));
    var count = (n.instances || []).length;
    g.appendChild(s("text", { class: "name", x: 44, y: 38 }, truncateMiddle(n.name, Math.max(count, pres.before, pres.after) > 1 ? 18 : 22)));
    var countEl = s("text", { class: "count", x: NODE_W - 12, y: 38, "text-anchor": "end" }, count > 1 ? "×" + count : "");
    g.appendChild(countEl);
    g.__counts = { plan: count, before: pres.before, after: pres.after, el: countEl };
    if (sevRank[n.severity] >= 3) {
      var ic = sevIcon(n.severity);
      ic.setAttribute("x", NODE_W - 25);
      ic.setAttribute("y", 8);
      ic.setAttribute("width", 14);
      ic.setAttribute("height", 14);
      g.appendChild(ic);
    }
    // Mixed instances: a thin bar of their actions along the bottom edge.
    var kinds = Object.keys(n.actions || {}).filter(function (a) { return n.actions[a] > 0; });
    if (changed && kinds.length > 1) {
      var total = kinds.reduce(function (sum, a) { return sum + n.actions[a]; }, 0);
      var x = 44, w = NODE_W - 44 - 12;
      kinds.sort(function (a, b) { return rankAction(b) - rankAction(a); }).forEach(function (a) {
        var segment = Math.max(4, w * n.actions[a] / total - 2);
        var k2 = actionKind(a);
        g.appendChild(s("rect", { class: "mix", x: x, y: NODE_H - 8, width: segment, height: 3, style: "fill: " + (k2 === "other" ? "var(--faint)" : "var(--act-" + k2 + ")") }));
        x += segment + 2;
      });
    }
    g.addEventListener("click", function (e) { e.stopPropagation(); selectNode(id); });
    // The node takes Enter and Space for itself, so Space does not also
    // pause the apply playing on the map.
    g.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); e.stopPropagation(); selectNode(id); } });
    g.addEventListener("pointerenter", function (e) { scheduleTip(n, e); });
    g.addEventListener("pointermove", function (e) { lastPointer = e; moveTip(e); });
    g.addEventListener("pointerleave", leaveTip);
    g.addEventListener("focus", function () { var r = g.getBoundingClientRect(); showTip(n, { clientX: r.right, clientY: r.top }); });
    g.addEventListener("blur", leaveTip);
    return g;
  }

  function rankAction(a) { return { delete: 7, replace: 6, forget: 5, create: 4, update: 3, read: 2, "no-op": 1, move: 1 }[a] || 0; }

  function nodeAria(n) {
    var parts = [n.id, n.noun || n.type];
    Object.keys(n.actions || {}).forEach(function (a) { parts.push(n.actions[a] + " " + a); });
    if (sevRank[n.severity] >= 3) parts.push(n.severity + " severity");
    return parts.join(", ");
  }

  function toolButton(title, iconName, onClick) {
    return h("button", { type: "button", title: title, "aria-label": title, onclick: onClick }, icon(iconName));
  }

  function minimap(L) {
    var el = h("div", { class: "minimap", title: "Overview. Click to move there", "aria-hidden": "true" });
    var svgEl = s("svg", { viewBox: "0 0 " + L.width + " " + L.height, preserveAspectRatio: "xMidYMid meet" });
    if (state.mapGroup !== "none") {
      L.lanes.forEach(function (lane) { svgEl.appendChild(s("rect", { x: MARGIN, y: lane.y, width: L.width - MARGIN * 2, height: lane.h, class: "lane-band" })); });
    }
    L.pos.forEach(function (p, id) {
      var n = nodeById.get(id);
      var kind = nodeChanged(n) ? actionKind(n.action) : "other";
      svgEl.appendChild(s("rect", { x: p.x, y: p.y, width: NODE_W, height: NODE_H, style: "fill: " + (kind === "other" ? "var(--faint)" : "var(--act-" + kind + ")"), opacity: kind === "other" ? 0.55 : 0.95 }));
    });
    // The viewport box moves on every pan frame; on its own SVG it repaints
    // alone, instead of re-rasterising every node of the overview with it.
    var view = s("rect", { class: "view", x: 0, y: 0, width: 0, height: 0 });
    var overlay = s("svg", { class: "mini-view", viewBox: "0 0 " + L.width + " " + L.height, preserveAspectRatio: "xMidYMid meet" }, view);
    el.appendChild(svgEl);
    el.appendChild(overlay);
    // Press to glide there; keep holding and drag to scrub 1:1.
    var scrubbing = false;
    function centreOn(e, animate) {
      if (!mapEls || !state.view) return;
      var m = svgEl.getScreenCTM();
      if (!m) return;
      var pt = svgEl.createSVGPoint();
      pt.x = e.clientX; pt.y = e.clientY;
      var at = pt.matrixTransform(m.inverse());
      var size = mapSize();
      var k = state.view.k;
      var target = { k: k, x: size.w / 2 - at.x * k, y: size.h / 2 - at.y * k };
      if (animate) animateView(target, 300);
      else { cancelAnimationFrame(camRaf); state.view = target; applyView(); }
    }
    el.addEventListener("pointerdown", function (e) {
      if (e.button !== 0) return;
      scrubbing = true;
      el.setPointerCapture(e.pointerId);
      el.classList.add("scrubbing");
      centreOn(e, true);
    });
    el.addEventListener("pointermove", function (e) { if (scrubbing) centreOn(e, false); });
    function stop() { scrubbing = false; el.classList.remove("scrubbing"); }
    el.addEventListener("pointerup", stop);
    el.addEventListener("pointercancel", stop);
    return { el: el, view: view };
  }

  // The frame's size, read once and refreshed on resize, so per-frame camera
  // work never forces a layout.
  function mapSize() {
    if (!mapEls.size) {
      var r = mapEls.svg.getBoundingClientRect();
      mapEls.size = { w: r.width || 900, h: r.height || 520 };
    }
    return mapEls.size;
  }

  function updateMinimap() {
    if (!mapEls || !state.view) return;
    var size = mapSize();
    var v = state.view;
    mapEls.mini.view.setAttribute("x", -v.x / v.k);
    mapEls.mini.view.setAttribute("y", -v.y / v.k);
    mapEls.mini.view.setAttribute("width", size.w / v.k);
    mapEls.mini.view.setAttribute("height", size.h / v.k);
  }

  // Off-screen culling: lanes and edges outside the view (plus a margin)
  // are not painted. Only elements whose state flips are written.
  function cull() {
    var v = state.view, size = mapSize(), m = 240;
    var x0 = -v.x / v.k - m, y0 = -v.y / v.k - m, x1 = (size.w - v.x) / v.k + m, y1 = (size.h - v.y) / v.k + m;
    mapEls.lanes.forEach(function (g) {
      var hide = g.__box[1] < y0 || g.__box[0] > y1;
      if (g.__hidden !== hide) { g.__hidden = hide; g.style.display = hide ? "none" : ""; }
    });
    mapEls.edges.forEach(function (p) {
      var b = p.__box;
      var hide = b[2] < x0 || b[0] > x1 || b[3] < y0 || b[1] > y1;
      if (p.__hidden !== hide) { p.__hidden = hide; p.style.display = hide ? "none" : ""; }
    });
  }

  // mapNeighbour walks the drawing from the keyboard: left to what a
  // resource depends on, right to what depends on it, up and down within its
  // column; the nearest one on screen wins.
  function mapNeighbour(id, key) {
    var pos = mapEls.layout.pos, p = pos.get(id);
    if (!p) return null;
    var pool;
    if (key === "ArrowLeft") pool = ups.get(id) || [];
    else if (key === "ArrowRight") pool = downs.get(id) || [];
    else pool = Array.from(pos.keys()).filter(function (o) { return Math.abs(pos.get(o).x - p.x) < 1; });
    pool = pool.filter(function (o) {
      var q = pos.get(o);
      if (!q || o === id) return false;
      if (key === "ArrowUp") return q.y < p.y;
      if (key === "ArrowDown") return q.y > p.y;
      return true;
    });
    pool.sort(function (a, b) {
      var qa = pos.get(a), qb = pos.get(b);
      return (Math.abs(qa.y - p.y) + Math.abs(qa.x - p.x) * 0.25) - (Math.abs(qb.y - p.y) + Math.abs(qb.x - p.x) * 0.25);
    });
    return pool[0] || null;
  }

  function selectNode(id) {
    var inst = byResource.get(id) || [];
    if (inst.length === 1) select({ kind: "change", id: inst[0].address });
    else select({ kind: "resource", id: id });
  }

  function selectedResource() {
    if (!state.sel) return null;
    if (state.sel.kind === "resource") return state.sel.id;
    var c = byAddr.get(state.sel.id);
    return c ? c.resource : null;
  }

  // Breadth-first distances from id along edges: 1 for direct neighbours.
  function distances(id, edges) {
    var dist = new Map([[id, 0]]), queue = [id];
    while (queue.length) {
      var cur = queue.shift();
      (edges.get(cur) || []).forEach(function (n) {
        if (!dist.has(n)) { dist.set(n, dist.get(cur) + 1); queue.push(n); }
      });
    }
    dist.delete(id);
    return dist;
  }

  // Selection traces what a resource reads and everything that reads it.
  // Nearer resources stay brighter. A selection with nothing related in view
  // dims nothing: there is no trace to show, only the resource itself.
  function highlight() {
    if (!mapEls || state.tab !== "map") return;
    var id = selectedResource();
    var upD = new Map(), downD = new Map();
    var active = !!(id && mapEls.nodes.has(id));
    if (active) {
      upD = distances(id, ups);
      downD = distances(id, downs);
    }
    var related = false;
    if (active) {
      mapEls.nodes.forEach(function (g, nid) { if (upD.has(nid) || downD.has(nid)) related = true; });
    }
    // Edits mode already fades what your edits do not explain; dimming
    // around the selection as well would fade the edits themselves.
    mapEls.root.classList.toggle("dimmed", related && state.mapTime !== "edits");
    mapEls.nodes.forEach(function (g, nid) {
      var d = Math.min(upD.has(nid) ? upD.get(nid) : 99, downD.has(nid) ? downD.get(nid) : 99);
      g.classList.toggle("selected", nid === id);
      g.classList.toggle("up", upD.has(nid));
      g.classList.toggle("down", downD.has(nid));
      g.classList.toggle("d2", d === 2);
      g.classList.toggle("d3", d >= 3 && d < 99);
    });
    var flowing = 0;
    mapEls.edges.forEach(function (p) {
      var e = p.__edge;
      var isDown = active && (e[0] === id || downD.has(e[0])) && downD.has(e[1]);
      var isUp = active && (e[1] === id || upD.has(e[1])) && upD.has(e[0]);
      if (isDown) flowing++;
      p.classList.toggle("down", isDown);
      p.classList.toggle("up", isUp && !isDown);
      p.setAttribute("marker-end", isDown ? "url(#arrow-down)" : isUp ? "url(#arrow-up)" : "url(#arrow)");
    });
    mapEls.root.classList.toggle("flow", flowing > 0 && flowing <= FLOW_LIMIT);
  }

  function applyView() {
    if (!mapEls || !state.view) return;
    mapEls.root.setAttribute("transform", "translate(" + state.view.x + "," + state.view.y + ") scale(" + state.view.k + ")");
    var far = state.view.k < FAR_ZOOM;
    if (far !== mapEls.far) { mapEls.far = far; mapEls.svg.classList.toggle("far", far); }
    cull();
    updateMinimap();
  }

  // Camera moves glide on the same curve as everything else. They are rare
  // and spatial, so they earn the motion; dragging and wheel zoom stay 1:1.
  function animateView(target, ms) {
    cancelAnimationFrame(camRaf);
    if (!state.view || reducedMotion()) { state.view = target; applyView(); return; }
    var from = { k: state.view.k, x: state.view.x, y: state.view.y };
    var start = performance.now();
    var duration = ms || 320;
    function frame(now) {
      var p = Math.min(1, (now - start) / duration);
      var e = easeOut(p);
      state.view = { k: from.k + (target.k - from.k) * e, x: from.x + (target.x - from.x) * e, y: from.y + (target.y - from.y) * e };
      applyView();
      if (p < 1) camRaf = requestAnimationFrame(frame);
    }
    camRaf = requestAnimationFrame(frame);
  }

  function fitView() {
    var L = mapEls.layout;
    var size = mapSize();
    var w = size.w, hgt = size.h;
    var k = Math.max(0.12, Math.min(1.1, Math.min((w - 24) / L.width, (hgt - 80) / L.height)));
    return { k: k, x: (w - L.width * k) / 2, y: Math.max(10, (hgt - 56 - L.height * k) / 2) };
  }

  function fit(animate) {
    if (!mapEls) return;
    var target = fitView();
    if (animate) animateView(target); else { state.view = target; applyView(); }
  }

  // The first view keeps text legible: it only shrinks so far, and centres
  // on the selection when the map is bigger than the frame.
  function initialView() {
    if (!mapEls) return;
    var L = mapEls.layout;
    var size = mapSize();
    var w = size.w, hgt = size.h;
    var fitK = Math.min((w - 24) / L.width, (hgt - 80) / L.height);
    if (fitK >= 0.8) { fit(false); return; }
    var k = 0.86, x = 10, y = 10;
    var id = selectedResource();
    var p = id && L.pos.get(id);
    if (p) {
      x = w / 2 - (p.x + NODE_W / 2) * k;
      y = (hgt - 56) / 2 - (p.y + NODE_H / 2) * k;
    }
    x = Math.min(10, Math.max(w - L.width * k - 10, x));
    y = Math.min(10, Math.max(hgt - 56 - L.height * k - 10, y));
    state.view = { k: k, x: x, y: y };
    applyView();
  }

  function zoomBy(f, cx, cy, animate) {
    if (!mapEls || !state.view) return;
    var size = mapSize();
    if (cx === undefined) { cx = size.w / 2; cy = size.h / 2; }
    var v = state.view;
    var k = Math.min(3, Math.max(0.08, v.k * f));
    var ratio = k / v.k;
    var target = { k: k, x: cx - (cx - v.x) * ratio, y: cy - (cy - v.y) * ratio };
    if (animate) animateView(target, 240); else { state.view = target; applyView(); }
  }

  function enablePanZoom(svgEl) {
    requestAnimationFrame(function () {
      placeTallies();
      if (!state.view) initialView(); else applyView();
    });
    svgEl.addEventListener("wheel", function (e) {
      e.preventDefault();
      cancelAnimationFrame(camRaf);
      var rect = svgEl.getBoundingClientRect();
      zoomBy(Math.exp(-e.deltaY * (e.deltaMode === 1 ? 0.05 : 0.0015)), e.clientX - rect.left, e.clientY - rect.top, false);
    }, { passive: false });
    var drag = null;
    svgEl.addEventListener("pointerdown", function (e) {
      if (e.button !== 0 || e.target.closest(".node")) return;
      cancelAnimationFrame(camRaf);
      drag = { x: e.clientX, y: e.clientY, vx: state.view ? state.view.x : 0, vy: state.view ? state.view.y : 0, moved: false };
      svgEl.setPointerCapture(e.pointerId);
    });
    svgEl.addEventListener("pointermove", function (e) {
      if (!drag || !state.view) return;
      var dx = e.clientX - drag.x, dy = e.clientY - drag.y;
      if (!drag.moved && Math.abs(dx) + Math.abs(dy) > 3) { drag.moved = true; svgEl.classList.add("panning"); hideTip(); }
      if (!drag.moved) return;
      state.view.x = drag.vx + dx;
      state.view.y = drag.vy + dy;
      applyView();
    });
    function end() {
      if (!drag) return;
      // The click that ends a drag should not clear the selection.
      if (drag.moved) suppressClickUntil = Date.now() + 80;
      drag = null;
      svgEl.classList.remove("panning");
    }
    svgEl.addEventListener("pointerup", end);
    svgEl.addEventListener("pointercancel", end);
  }

  // Tooltips: the first waits a moment, so sweeping the pointer across the
  // map does not flash labels; once one is open, neighbours appear at once.
  var tip = null, tipTimer = 0, tipWarmUntil = 0, lastPointer = null;

  function scheduleTip(n, e) {
    lastPointer = e;
    clearTimeout(tipTimer);
    if (Date.now() < tipWarmUntil) { showTip(n, e); return; }
    tipTimer = setTimeout(function () { showTip(n, lastPointer || e); }, 450);
  }

  function leaveTip() {
    clearTimeout(tipTimer);
    if (tip) tipWarmUntil = Date.now() + 600;
    hideTip();
  }

  function showTip(n, e) {
    hideTip();
    tip = h("div", { class: "tooltip", role: "tooltip" });
    tip.appendChild(h("div", { class: "t-addr" }, n.id));
    tip.appendChild(h("div", { class: "t-line" }, n.noun || n.type));
    var parts = [];
    Object.keys(n.actions || {}).sort(function (a, b) { return rankAction(b) - rankAction(a); }).forEach(function (a) {
      parts.push(n.actions[a] + " " + (a === "delete" ? "destroy" : a === "no-op" ? "unchanged" : a));
    });
    if (parts.length) tip.appendChild(h("div", { class: "t-line" }, parts.join(" · ")));
    else if (n.mode === "data") tip.appendChild(h("div", { class: "t-line" }, "read while planning"));
    var ids = new Set();
    (byResource.get(n.id) || []).forEach(function (c) { (c.findings || []).forEach(function (f) { ids.add(f); }); });
    if (ids.size) tip.appendChild(h("div", { class: "t-line" }, sevMark(n.severity, true), " · " + plural(ids.size, "finding")));
    var down = transitive(n.id, downs).size;
    if (down) tip.appendChild(h("div", { class: "t-line" }, plural(down, "resource depends", "resources depend") + " on it"));
    document.body.appendChild(tip);
    moveTip(e);
  }

  function moveTip(e) {
    if (!tip || !e) return;
    var x = e.clientX + 16, y = e.clientY + 16;
    var r = tip.getBoundingClientRect();
    if (x + r.width > window.innerWidth - 8) x = e.clientX - r.width - 16;
    if (y + r.height > window.innerHeight - 8) y = e.clientY - r.height - 16;
    tip.style.left = Math.max(8, x) + "px";
    tip.style.top = Math.max(8, y) + "px";
  }

  function hideTip() { if (tip) { tip.remove(); tip = null; } }

  // -------------------------------------------------------------- selection

  function select(target) {
    if (target && target.kind === "output") {
      setTab("outputs", true);
      var row = document.querySelector('tr[data-output="' + cssEscape(target.id) + '"]');
      if (row) {
        row.classList.add("flash");
        row.scrollIntoView({ block: "center" });
        setTimeout(function () { row.classList.remove("flash"); }, 1600);
      }
      return;
    }
    state.sel = target;
    state.autoSel = false;
    if (state.tab === "outputs") { state.tab = "changes"; renderBody(false); }
    else if (state.tab === "map" && state.mapScope === "focus") { state.view = null; renderMain(); }
    renderDetail();
    syncRows();
    if (state.tab === "changes" && selectedRowEl) selectedRowEl.scrollIntoView({ block: "nearest" });
    if (state.tab === "map" && mapEls && target) {
      var id = selectedResource();
      var p = id && mapEls.layout.pos.get(id);
      if (p && state.view) {
        var rect = mapEls.svg.getBoundingClientRect();
        var k = state.view.k;
        var sx = p.x * k + state.view.x, sy = p.y * k + state.view.y;
        // Glide to the selection only when it is off screen.
        if (sx < 0 || sy < 0 || sx + NODE_W * k > rect.width || sy + NODE_H * k > rect.height - 56) {
          animateView({ k: k, x: rect.width / 2 - (p.x + NODE_W / 2) * k, y: (rect.height - 56) / 2 - (p.y + NODE_H / 2) * k });
        }
      }
    }
    highlight();
    syncHash();
  }

  function go(target) {
    if (target.kind === "output") { select(target); return; }
    if (target.kind === "change") {
      var c = byAddr.get(target.id);
      var refill = false;
      if (c && isCosmetic(c) && !state.cosmetic) { state.cosmetic = true; refill = true; }
      if (c && isUnchanged(c) && !state.unchanged) { state.unchanged = true; refill = true; }
      if (c && !state.actions[actionKind(c.action)]) { state.actions[actionKind(c.action)] = true; refill = true; }
      // A filter or a folded group that hides it gives way too.
      if (c && state.q && !matches(c)) { state.q = ""; refill = true; }
      if (c && state.collapsed.has(groupKey(c))) { state.collapsed.delete(groupKey(c)); refill = true; }
      if (refill && state.tab === "changes") renderMain();
    }
    select(target);
    if (wsHead && wsHead.getBoundingClientRect().top > window.innerHeight * 0.6) scrollToWorkspace();
  }

  function openInMap(target) {
    state.sel = target;
    state.autoSel = false;
    state.mapScope = "focus";
    state.view = null;
    if (state.tab === "map") { renderMain(); renderDetail(); syncHash(); }
    else setTab("map", false);
    scrollToWorkspace();
  }

  function cssEscape(v) {
    if (window.CSS && CSS.escape) return CSS.escape(v);
    return String(v).replace(/["\\]/g, "\\$&");
  }

  function syncHash() {
    var parts = [];
    if (state.tab !== "changes") parts.push("view=" + state.tab);
    // A selection interragate made is not worth a link: reloading should choose
    // afresh.
    if (state.sel && !state.autoSel) parts.push((state.sel.kind === "resource" ? "resource=" : "r=") + encodeURIComponent(state.sel.id));
    var hash = parts.length ? "#" + parts.join("&") : "";
    if (location.hash !== hash) history.replaceState(null, "", location.pathname + location.search + hash);
  }

  function readHash() {
    var hash = location.hash.slice(1);
    if (!hash) return false;
    var any = false;
    hash.split("&").forEach(function (kv) {
      var i = kv.indexOf("=");
      if (i < 0) return;
      var k = kv.slice(0, i), v;
      try { v = decodeURIComponent(kv.slice(i + 1)); } catch (e) { return; }
      if (k === "view" && tabById(v)) { state.tab = v; any = true; }
      if (k === "r" && byAddr.has(v)) { state.sel = { kind: "change", id: v }; any = true; }
      if (k === "resource" && (nodeById.has(v) || byResource.has(v))) { state.sel = { kind: "resource", id: v }; any = true; }
    });
    if (state.sel && state.sel.kind === "change") {
      var c = byAddr.get(state.sel.id);
      if (c && isCosmetic(c)) state.cosmetic = true;
      if (c && isUnchanged(c)) state.unchanged = true;
    }
    return any;
  }

  function focusSearch() {
    setTab("changes", false);
    var input = mainCol && mainCol.querySelector(".search");
    if (!input) return;
    input.focus({ preventScroll: true });
    scrollToWorkspace();
  }

  // --------------------------------------------------------------- keyboard

  // Only fields that take text swallow the shortcuts; a checkbox someone
  // just clicked must not.
  var TEXT_INPUT = /^(text|search|email|url|tel|password|number|date|time|datetime-local|month|week)$/;

  function isTextEntry(t) {
    if (!t || !t.tagName) return false;
    if (t.isContentEditable || t.tagName === "TEXTAREA" || t.tagName === "SELECT") return true;
    return t.tagName === "INPUT" && TEXT_INPUT.test((t.getAttribute("type") || "text").toLowerCase());
  }

  document.addEventListener("keydown", function (e) {
    var t = e.target;
    var typing = isTextEntry(t);
    if (e.key === "Escape") {
      if (typing) { t.blur(); return; }
      if (state.sel) {
        state.sel = null;
        renderDetail();
        syncRows();
        highlight();
        syncHash();
      }
      return;
    }
    // ⌘K on a Mac, Ctrl K elsewhere. In a Mac text field Ctrl K deletes to
    // the end of the line, so it is left alone there.
    if (!e.altKey && (e.key === "k" || e.key === "K") && (MAC ? e.metaKey || (e.ctrlKey && !typing) : e.ctrlKey)) { e.preventDefault(); openPalette(); return; }
    if (paletteEl) return;
    if (typing || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === "/") { e.preventDefault(); focusSearch(); return; }
    if (/^[1-9]$/.test(e.key) && Number(e.key) <= TABS.length) { setTab(TABS[Number(e.key) - 1].id, true); return; }
    if (state.tab === "map" && mapEls) {
      if (e.key === "f") { fit(true); return; }
      if (e.key === "b") { stopPlay(); setMapTime(state.mapTime === "before" ? "after" : "before"); return; }
      if (e.key === "e" && mapEls.when.has("edits")) { stopPlay(); setMapTime(state.mapTime === "edits" ? "plan" : "edits"); return; }
      if (/^Arrow(Left|Right|Up|Down)$/.test(e.key) && !(t.closest && t.closest('[role="tablist"]'))) {
        var from = selectedResource();
        var next = from && mapNeighbour(from, e.key);
        if (next) {
          e.preventDefault();
          selectNode(next);
          var ng = mapEls.nodes.get(next);
          if (ng) ng.focus({ preventScroll: true });
        }
        return;
      }
      if (e.key === "p" && applyData) { togglePlay(); return; }
      if (e.key === " " && player) { e.preventDefault(); togglePlay(); return; }
      if (e.key === "+" || e.key === "=") { zoomBy(1.25, undefined, undefined, true); return; }
      if (e.key === "-" || e.key === "_") { zoomBy(0.8, undefined, undefined, true); return; }
    }
    if (e.key === "n" && toReview.length) { goNextReview(); return; }
    if (e.key === "r" && state.sel && state.sel.kind === "change") {
      var rc = byAddr.get(state.sel.id);
      if (rc && needsLook(rc)) {
        setReviewed(rc, !isReviewed(rc));
        renderDetail();
      }
      return;
    }
    if ((e.key === "j" || e.key === "k") && state.tab === "changes" && listEl) {
      var rows = Array.from(listEl.querySelectorAll('.module:not([data-collapsed="true"]) .row'));
      if (!rows.length) return;
      var idx = selectedRowEl ? rows.indexOf(selectedRowEl) : -1;
      var next = e.key === "j" ? Math.min(rows.length - 1, idx + 1) : Math.max(0, idx < 0 ? 0 : idx - 1);
      select({ kind: "change", id: rows[next].getAttribute("data-address") });
      rows[next].focus({ preventScroll: true });
    }
  });

  window.addEventListener("resize", function () {
    if (!mapEls) return;
    mapEls.size = null;
    applyView();
  });

  // ----------------------------------------------------------------- apply

  // How the apply runs, as a timeline with a row per resource. A replacement
  // that destroys first reads left to right on its row: the destroy, the
  // window in which the resource does not exist, the create. Steps off the
  // critical path are faded: they can finish later without making the apply
  // any longer, so the eye goes to what decides its length. A step expected
  // to fail is not, since the apply stops there.
  var applyData = data.apply || null;
  var applyRowEls = new Map();
  var APPLY_KIND = { create: "create", update: "update", destroy: "delete", read: "other" };
  var APPLY_VERB = { create: "Create", update: "Update", destroy: "Destroy", read: "Read" };
  var APPLY_LIMIT = 80;

  function clock(sec) {
    sec = Math.round(sec);
    var h = Math.floor(sec / 3600), m = Math.floor(sec % 3600 / 60), s = sec % 60;
    var mm = h ? String(m).padStart(2, "0") : String(m);
    return (h ? h + ":" : "") + mm + ":" + String(s).padStart(2, "0");
  }

  // roughly words a duration as interragate does in Go: minutes to the
  // nearest one, and past an hour, to the nearest five.
  function roughly(sec) {
    if (sec < 60) return "under a minute";
    var min = Math.floor((sec + 30) / 60);
    if (min < 60) return "about " + plural(min, "minute");
    min = Math.floor((min + 2) / 5) * 5;
    var hrs = Math.floor(min / 60), rest = min % 60;
    return "about " + plural(hrs, "hour") + (rest ? " " + plural(rest, "minute") : "");
  }

  // Tick spacing: the smallest round step that gives at most six ticks.
  function applyTicks(total) {
    var steps = [10, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200];
    var step = steps[steps.length - 1];
    for (var i = 0; i < steps.length; i++) { if (total / steps[i] <= 6) { step = steps[i]; break; } }
    var out = [];
    for (var t = 0; t <= total; t += step) out.push(t);
    return { step: step, ticks: out };
  }

  function tickLabel(t, step) {
    if (t === 0) return "0";
    if (step < 60) return t + " s";
    if (t < 3600 || t % 3600) return Math.round(t / 60) + " min";
    return t / 3600 + " h";
  }

  function applyRows() {
    var rows = new Map();
    applyData.steps.forEach(function (st) {
      var r = rows.get(st.address);
      if (!r) { r = { address: st.address, steps: [], start: st.start, end: 0, critical: false, known: false }; rows.set(st.address, r); }
      r.steps.push(st);
      r.start = Math.min(r.start, st.start);
      r.end = Math.max(r.end, st.start + st.duration);
      if (applyCritical.has(st.id)) r.critical = true;
      if (st.known) r.known = true;
    });
    return Array.from(rows.values()).sort(function (a, b) { return a.start - b.start || (a.address < b.address ? -1 : 1); });
  }

  var applyCritical = new Set((applyData && applyData.critical) || []);
  var applyGaps = new Map(((applyData && applyData.gaps) || []).map(function (g) { return [g.address, g]; }));
  // Steps a finding expects to fail, such as a destroy that deletion
  // protection refuses.
  var applyPredicted = ((applyData && applyData.steps) || []).filter(function (st) { return st.fails; });
  var applySteps = new Map();
  ((applyData && applyData.steps) || []).forEach(function (st) {
    if (!applySteps.has(st.address)) applySteps.set(st.address, []);
    applySteps.get(st.address).push(st);
  });

  // andList joins items as prose: "a, b and c", or "a, b, c and 2 more"
  // past limit.
  function andList(items, limit) {
    if (items.length > limit) return items.slice(0, limit).join(", ") + " and " + (items.length - limit) + " more";
    if (items.length < 2) return items.join("");
    return items.slice(0, -1).join(", ") + " and " + items[items.length - 1];
  }

  // Why a step is expected to fail, from its finding's title.
  function failReason(st) {
    var f = st && st.fails ? findingById.get(st.fails) : null;
    if (!f) return "";
    var m = /^Apply will fail: (.+)$/.exec(f.title);
    return m ? m[1] : f.title;
  }

  function applyTimeline() {
    var wrap = h("div", { class: "apply" });
    applyRowEls = new Map();
    applySegEls = new Map();
    var total = Math.max(applyData.duration || 0, 1);
    var lead = h("div", { class: "apply-lead" });
    if (applyData.narrative && applyData.narrative.length) {
      applyData.narrative.forEach(function (sn) { lead.appendChild(h("p", null, rich(sn.text))); });
    } else if (!applyData.steps.some(function (st) { return st.known; })) {
      // Without a slow type, every step is a guess of a few seconds: say
      // what the drawing can still tell.
      lead.appendChild(h("p", null, "Every step here should be quick, so read the bars for the order " + TOOL + " takes them in and what waits for what, not for how long each takes."));
    }
    if (lead.childNodes.length) wrap.appendChild(lead);
    var all = applyRows();
    // Long plans keep the rows that shape the apply: the critical path,
    // slow resources, missing windows, and where the apply is expected to
    // fail and what that leaves half done. The rest are a click away.
    var rows = all;
    if (all.length > APPLY_LIMIT && !state.applyAll) {
      var keep = new Set(applyPredicted.map(function (st) { return st.address; }));
      if (applyPredicted.length) {
        var expected = failureFrom(applyPredicted);
        expected.missing.concat(expected.both).forEach(function (a) { keep.add(a); });
      }
      rows = all.filter(function (r) { return r.critical || r.known || applyGaps.has(r.address) || keep.has(r.address); });
    }

    var legend = h("div", { class: "apply-legend", "aria-hidden": "true" });
    [["create", "create"], ["update", "update"], ["delete", "destroy"], ["other", "read"]].forEach(function (k) {
      if (!all.some(function (r) { return r.steps.some(function (st) { return APPLY_KIND[st.op] === k[0]; }); })) return;
      legend.appendChild(h("span", null, h("i", { class: "key " + k[0] }), k[1]));
    });
    if (applyGaps.size) legend.appendChild(h("span", null, h("i", { class: "key gap" }), "does not exist"));
    if (applyPredicted.length) legend.appendChild(h("span", null, h("i", { class: "key fails" }), "expected to fail"));
    legend.appendChild(h("span", null, h("i", { class: "key faded" }), "off the critical path"));
    wrap.appendChild(legend);
    applyFailEl = h("div", { class: "apply-fail", "aria-live": "polite" });
    wrap.appendChild(applyFailEl);

    var t = applyTicks(total);
    var pct = function (sec) { return (sec / total * 100).toFixed(3) + "%"; };
    var chart = h("div", { class: "apply-chart" });
    var axis = h("div", { class: "apply-axis", "aria-hidden": "true" }, h("span", { class: "apply-label" }));
    var scale = h("div", { class: "apply-scale" });
    t.ticks.forEach(function (tick) { scale.appendChild(h("span", { style: "left:" + pct(tick) }, tickLabel(tick, t.step))); });
    axis.appendChild(scale);
    chart.appendChild(axis);

    var body = h("div", { class: "apply-body", role: "group", "aria-label": "Steps of the apply, in the order they start" });
    var grid = h("div", { class: "apply-grid", "aria-hidden": "true" });
    t.ticks.forEach(function (tick) { if (tick) grid.appendChild(h("i", { style: "left:" + pct(tick) })); });
    body.appendChild(grid);
    rows.forEach(function (r) {
      var c = byAddr.get(r.address);
      var track = h("span", { class: "apply-track" });
      var gap = applyGaps.get(r.address);
      if (gap) {
        track.appendChild(applySeg({ gap: gap }, "seg gap", pct(gap.from), pct(Math.max(gap.to - gap.from, total * 0.004))));
      }
      r.steps.forEach(function (st) {
        // A step expected to fail is not faded, on the critical path or not,
        // so its stop line shows.
        var cls = "seg " + (APPLY_KIND[st.op] || "other") + (applyCritical.has(st.id) || st.fails ? "" : " faded") + (st.fails ? " fails" : "");
        var seg = applySeg({ step: st }, cls, pct(st.start), pct(Math.max(st.duration, total * 0.004)));
        applySegEls.set(st.id, seg);
        track.appendChild(seg);
      });
      var name = c ? localName(c) : r.address;
      var spoken = r.steps.map(function (st) { return APPLY_VERB[st.op] + " from " + clock(st.start) + " to " + clock(st.start + st.duration); }).join(", ");
      var row = h("button", {
        class: "apply-row" + (r.critical ? " critical" : ""), type: "button", "data-address": r.address,
        "aria-label": r.address + ": " + spoken + (gap ? ". Does not exist from " + clock(gap.from) + " to " + clock(gap.to) : ""),
        onclick: function () { if (c) select({ kind: "change", id: r.address }); },
      }, applyLabel(name, r.address), track);
      applyRowEls.set(r.address, row);
      body.appendChild(row);
    });
    chart.appendChild(body);
    wrap.appendChild(chart);
    syncApplyRows();

    var notes = h("p", { class: "apply-foot" });
    notes.appendChild(document.createTextNode("Times are typical figures for slow resource types, and a few seconds for everything else. When a step fails, " + TOOL + " skips the steps that wait for it and finishes the rest, so a failure can leave a replacement half done. "));
    if (!plan.source_read) notes.appendChild(document.createTextNode("The configuration's files were not read, so waits through locals and replace_triggered_by are missing. "));
    if (rows.length < all.length) {
      notes.appendChild(document.createTextNode(plural(all.length - rows.length, "quick step") + " off the critical path " + (all.length - rows.length === 1 ? "is" : "are") + " hidden. "));
      notes.appendChild(h("button", { class: "inline-btn", type: "button", onclick: function () { state.applyAll = true; renderMain(); } }, "Show every step"));
    }
    wrap.appendChild(notes);
    return wrap;
  }

  // The type gives way before the name does, so a narrow column still
  // shows which resource a row is: aws_db_inst….orders.
  function applyLabel(name, address) {
    var m = /^((?:data\.)?[^.]+)\.(.+)$/.exec(name);
    if (!m) return h("span", { class: "apply-label", title: address }, h("span", { class: "apply-name" }, name));
    return h("span", { class: "apply-label", title: address }, h("span", { class: "apply-type" }, m[1] + "."), h("span", { class: "apply-name" }, m[2]));
  }

  function applySeg(info, cls, left, width) {
    var seg = h("span", { class: cls, style: "left:" + left + ";width:" + width });
    seg.addEventListener("pointerenter", function (e) { showApplyTip(info, e); });
    seg.addEventListener("pointermove", function (e) { placeApplyTip(e); });
    seg.addEventListener("pointerleave", hideApplyTip);
    return seg;
  }

  var applyTip = null;

  function showApplyTip(info, e) {
    if (!applyTip) { applyTip = h("div", { class: "tooltip apply-tip", role: "tooltip" }); document.body.appendChild(applyTip); }
    applyTip.textContent = "";
    if (info.gap) {
      var g = info.gap;
      applyTip.appendChild(h("div", { class: "t-value" }, clock(g.from) + " – " + clock(g.to)));
      applyTip.appendChild(h("div", { class: "t-addr" }, g.address));
      applyTip.appendChild(h("div", { class: "t-line" }, "Does not exist for " + roughly(g.to - g.from) + ": destroyed before its replacement is created."));
      if (g.dependents && g.dependents.length) applyTip.appendChild(h("div", { class: "t-line" }, "Read meanwhile by " + andList(g.dependents, 3) + "."));
    } else {
      var st = info.step;
      var c = byAddr.get(st.address);
      applyTip.appendChild(h("div", { class: "t-value" }, clock(st.start) + " – " + clock(st.start + st.duration)));
      applyTip.appendChild(h("div", { class: "t-addr" }, (APPLY_VERB[st.op] || st.op) + " " + st.address + (st.deposed ? " (the old object)" : "")));
      if (st.fails) applyTip.appendChild(h("div", { class: "t-line" }, "Expected to fail: " + failReason(st) + "."));
      applyTip.appendChild(h("div", { class: "t-line" }, st.known ? "Typically " + roughly(st.duration) + " for " + (c && c.noun ? article(c.noun) : "this type") + "." : "No typical time is known for this type; interragate allows a few seconds."));
      if (st.after && st.after.length) applyTip.appendChild(h("div", { class: "t-line" }, "Waits for " + andList(st.after.map(function (id) { return id.replace(/:(create|update|destroy|read)$/, " ($1)"); }), 3) + "."));
      if (!applyCritical.has(st.id)) applyTip.appendChild(h("div", { class: "t-line muted" }, "Off the critical path: finishing later would not make the apply longer."));
    }
    applyTip.classList.add("show");
    placeApplyTip(e);
  }

  function placeApplyTip(e) {
    if (!applyTip) return;
    var w = applyTip.offsetWidth, hgt = applyTip.offsetHeight;
    var x = Math.min(window.innerWidth - w - 12, e.clientX + 14), y = e.clientY + 16;
    if (y + hgt > window.innerHeight - 12) y = e.clientY - hgt - 12;
    applyTip.style.transform = "translate(" + Math.max(12, x) + "px," + y + "px)";
  }

  function hideApplyTip() { if (applyTip) applyTip.classList.remove("show"); }

  // article picks "a" or "an" by sound: an acronym is read letter by letter,
  // so "an RDS database instance" but "a VPC".
  function article(noun) {
    var first = noun.split(" ")[0];
    if (/^[A-Z0-9]{2,}$/.test(first)) return (/^[AEFHILMNORSX8]/.test(first) ? "an " : "a ") + noun;
    return (/^[aeiou]/i.test(noun) ? "an " : "a ") + noun;
  }

  function syncApplyRows() {
    var id = state.sel && state.sel.kind === "change" ? state.sel.id : null;
    applyRowEls.forEach(function (row, addr) { row.setAttribute("aria-current", addr === id ? "true" : "false"); });
    // What a failure of the selected resource's step would stop. Until the
    // reader picks a resource, what the failures interragate expects would
    // stop.
    if (state.autoSel && applyPredicted.length) id = null;
    var f = id ? applyFailure(id) : applyPredicted.length ? failureFrom(applyPredicted) : null;
    // Steps that would not run go quiet; a row goes quiet when none of its
    // steps would run, and a replacement left destroyed stands out.
    var skipped = new Set(f ? f.skipped.map(function (st) { return st.id; }) : []);
    var missing = new Set(f ? f.missing : []);
    applySegEls.forEach(function (seg, sid) { seg.classList.toggle("skipped", skipped.has(sid)); });
    applyRowEls.forEach(function (row, addr) {
      var steps = applySteps.get(addr) || [];
      row.classList.toggle("blocked", steps.length > 0 && steps.every(function (st) { return skipped.has(st.id); }));
      row.classList.toggle("stranded", missing.has(addr));
    });
    if (applyFailEl) fillApplyFailure(applyFailEl, id, f);
  }

  // applyFailure works out what would not happen if the selected resource's
  // last step failed. Terraform carries on with work that does not depend
  // on a failed step and skips everything that does, so a failure can leave
  // a replacement half done: destroyed, and never created.
  var applySucc = null;
  function applyFailure(address) {
    if (!applyData) return null;
    var mine = applySteps.get(address) || [];
    if (!mine.length) return null;
    // A step interragate expects to fail is the one to show. Otherwise the
    // step whose failure costs most is the last one: a replacement's create,
    // or the destroy of the object it replaces.
    return failureFrom([mine.filter(function (st) { return st.fails; })[0] || mine[mine.length - 1]]);
  }

  // failureFrom follows what waits for the failing steps: those are
  // skipped, and so is a step expected to fail that waits for another one,
  // which fails only once that one is fixed. As in the narrative, it counts
  // resources: those with a step skipped, besides the ones that fail. A
  // replacement whose destroy runs but whose create does not is left
  // destroyed; one whose create runs but whose old object's destroy is
  // skipped is left with both.
  function failureFrom(predicted) {
    if (!applySucc) {
      applySucc = new Map();
      var link = function (from, to) {
        if (!applySucc.has(from)) applySucc.set(from, []);
        applySucc.get(from).push(to);
      };
      applyData.steps.forEach(function (st) { (st.after || []).forEach(function (id) { link(id, st); }); });
      // A join stands for several steps of one resource; whatever waits
      // for it waits for each of them.
      (applyData.joins || []).forEach(function (j) { (j.after || []).forEach(function (id) { link(id, j); }); });
    }
    var waiting = function (from) {
      var seen = new Set(), queue = from.map(function (st) { return st.id; }), steps = [];
      while (queue.length) {
        (applySucc.get(queue.shift()) || []).forEach(function (st) {
          if (seen.has(st.id)) return;
          seen.add(st.id);
          if (st.address) steps.push(st); // joins take no part of their own
          queue.push(st.id);
        });
      }
      return { ids: seen, steps: steps };
    };
    var reached = waiting(predicted).ids;
    var failing = [], later = [], at = new Set();
    predicted.forEach(function (st) {
      if (reached.has(st.id) || at.has(st.address)) return;
      failing.push(st);
      at.add(st.address);
    });
    predicted.forEach(function (st) {
      if (!at.has(st.address) && !later.some(function (l) { return l.address === st.address; })) later.push(st);
    });
    var skipped = waiting(failing).steps, resources = [];
    skipped.forEach(function (st) {
      if (!at.has(st.address)) { at.add(st.address); resources.push(st.address); }
    });
    var failed = new Set(failing.map(function (st) { return st.id; }));
    var runs = function (st) { return !reached.has(st.id) && !failed.has(st.id); };
    var missing = [], both = [];
    failing.map(function (st) { return st.address; }).concat(resources).forEach(function (addr) {
      var steps = applySteps.get(addr) || [];
      var destroy = steps.filter(function (st) { return st.op === "destroy"; })[0];
      var create = steps.filter(function (st) { return st.op === "create"; })[0];
      if (!destroy || !create) return;
      if (!destroy.deposed && runs(destroy) && !runs(create)) missing.push(addr);
      else if (destroy.deposed && runs(create) && reached.has(destroy.id)) both.push(addr);
    });
    return { failing: failing, later: later, skipped: skipped, resources: resources, missing: missing, both: both };
  }

  var applyFailEl = null;
  var applySegEls = new Map();

  var FAIL_VERB = { create: "creating", update: "updating", destroy: "destroying", read: "reading" };

  function fillApplyFailure(el, id, f) {
    el.textContent = "";
    if (!f) {
      el.appendChild(h("p", { class: "muted" }, "Select a resource to see what would not happen if its step failed."));
      return;
    }
    var first = f.failing[0];
    var them = f.failing.length > 1 ? "them" : "it";
    var codes = function (as) { return andList(as.map(function (a) { return "`" + a + "`"; }), 4); };
    // Resources, as the narrative counts them, besides those that fail: a
    // failed create-first replacement keeps its old object, and that
    // skipped destroy is good news, not work held up.
    var n = f.resources.length;
    var held = n ? (n === 1 ? "the one resource that waits for " + them + " is" : "the " + n + " resources that wait for " + them + " are") + " skipped"
      : "nothing else is held up: no other step waits for " + them;
    var lead;
    if (!id && f.failing.length > 1) {
      lead = "interragate expects the apply to fail at " + andList(f.failing.map(function (st) { return "`" + st.address + "`"; }), 3) + ". " + cap(held) + ".";
    } else if (first.fails) {
      lead = "interragate expects " + (FAIL_VERB[first.op] || first.op) + " `" + first.address + "` to fail: " + failReason(first) + ". " + cap(held) + ".";
    } else {
      lead = "If " + (FAIL_VERB[first.op] || first.op) + " `" + first.address + "` fails, " + held + ".";
    }
    el.appendChild(h("p", null, rich(lead)));
    if (f.missing.length) el.appendChild(h("p", { class: "bad" }, rich("Left destroyed and not replaced: " + codes(f.missing) + ".")));
    if (f.both.length) el.appendChild(h("p", null, rich("Left with both the old and the new " + (f.both.length > 1 ? "objects: " : "object: ") + codes(f.both) + ".")));
    // A step expected to fail that waits for a failure is skipped with the
    // rest, and fails once that one is fixed.
    if (f.later.length) {
      var why = f.later.length === 1 ? failReason(f.later[0]) : "";
      el.appendChild(h("p", null, rich("Fixing " + (f.failing.length > 1 ? "those" : "that") + " is not enough: " +
        codes(f.later.map(function (st) { return st.address; })) + " should fail as well" + (why ? ", where " + why : "") + ".")));
    }
    if (!id) el.appendChild(h("p", { class: "muted" }, "Select a resource to see what would not happen if its step failed instead."));
  }



  // In the detail pane: when the change happens during the apply.
  function applyLines(c) {
    if (!applyData) return null;
    var steps = applyData.steps.filter(function (st) { return st.address === c.address; });
    if (!steps.length) return null;
    var ul = h("ul", { class: "apply-when" });
    steps.forEach(function (st) {
      ul.appendChild(h("li", null, h("span", { class: "mono" }, clock(st.start) + "–" + clock(st.start + st.duration)), " ", (APPLY_VERB[st.op] || st.op).toLowerCase() + (st.deposed ? " the old object" : "") + (st.known ? ", typically " + roughly(st.duration) : "")));
    });
    var gap = applyGaps.get(c.address);
    if (gap) ul.appendChild(h("li", null, "Does not exist for " + roughly(gap.to - gap.from) + (gap.dependents && gap.dependents.length ? ", while " + plural(gap.dependents.length, "resource reads", "resources read") + " it" : "") + "."));
    return ul;
  }

  // ------------------------------------------------------------------ edits

  // Your edits: the change to the configuration that each planned change
  // comes from, when interragate could compare the configuration with git.
  // The rest of the plan is what changes anyway, which is what a reviewer
  // most needs to see.
  var git = data.git || null;
  var EDIT_SINCE = git ? (git.base === "HEAD" ? "since the last commit" : "since " + git.base) : "";
  // What nobody edited comes first: it is what a reviewer did not ask for.
  var EDIT_GROUPS = ["other", "edit", "follows"];
  var EDIT_GROUP_TITLE = { edit: "Your edits", follows: "Because of your edits", other: "Not tied to an edit" };

  function editLead(e) {
    switch (e.kind) {
      case "added": return "Added in `" + e.file + "` " + EDIT_SINCE + ".";
      case "removed": return "Removed from `" + e.file + "` " + EDIT_SINCE + ".";
      case "module": return "Comes from your edit to the call to `" + e.module + "`, in `" + e.file + "`.";
      case "input": return "It reads `" + e.input + "`, which changed in `" + e.file + "` " + EDIT_SINCE + "; its own block did not.";
      case "provider": return "Its provider's block changed in `" + e.file + "` " + EDIT_SINCE + ", and only its default tags change; its own block did not.";
      default: return "Changed in `" + e.file + "` " + EDIT_SINCE + ".";
    }
  }

  // startsWith follows a change's causes back to the edit they begin with,
  // and says so: "It starts with your edit to `var.azs`, in `main.tf`,
  // through `module.network.aws_subnet.private[1]`."
  function startsWith(c) {
    var seen = new Set([c.address]);
    var through = [];
    var cur = c;
    while (cur.causes && cur.causes.length) {
      var k = cur.causes[0];
      var next = byAddr.get(k.address) || (byResource.get(k.resource) || [])[0];
      if (!next || seen.has(next.address)) return "";
      seen.add(next.address);
      through.push(next.address);
      cur = next;
      if (cur.edit) break;
    }
    var e = cur.edit;
    if (!e || cur === c) return "";
    var what = e.kind === "module" ? "the call to `" + e.module + "`"
      : e.kind === "input" ? "`" + e.input + "`"
      : e.kind === "provider" ? "the provider block of `" + cur.address + "`"
      : "`" + cur.address + "`";
    var text = "It starts with your edit to " + what + ", in `" + e.file + "`";
    // The last step is the edited change itself when its own block changed.
    var steps = e.kind === "edited" || e.kind === "added" || e.kind === "removed" ? through.slice(0, -1) : through;
    if (steps.length) text += ", through " + andList(steps.map(function (a) { return "`" + a + "`"; }), 3);
    return text + ".";
  }

  function editTitle(e) {
    if (e.kind === "module") return "Your edit to the call to " + e.module;
    if (e.kind === "input") return "Your edit to " + e.input + ", which it reads";
    if (e.kind === "provider") return "Your edit to its provider's block";
    return "Your edit to " + e.file;
  }

  // The lines as git shows them, numbered as in the file: now for added
  // lines, before the edit for removed ones.
  function editLinesEl(e) {
    var box = h("div", { class: "lines edit-lines" });
    (e.lines || []).forEach(function (l) {
      box.appendChild(h("span", { class: "line " + (l.op === "+" ? "add" : "del") }, h("span", { class: "ln", "aria-hidden": "true" }, l.line ? String(l.line) : ""), l.op + " " + l.text));
    });
    if (e.omitted) box.appendChild(h("span", { class: "line skip" }, plural(e.omitted, "more changed line")));
    return box;
  }

  // editClass sorts a change by where it comes from: your edit, something
  // that changes because of your edit, or neither.
  function editClass(c) { return c.origin || (c.edit ? "edit" : "other"); }

  // ---------------------------------------------------------------- go to

  // Go to: one box that finds any resource, finding, view or command, from
  // any view. The map has no list to filter, so this is how to find a
  // resource in it; Enter selects it where you are.
  var MAC = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");
  var paletteEl = null;

  function paletteItems() {
    var items = [];
    changes.forEach(function (c) {
      if (isUnchanged(c)) return;
      items.push({ kind: "change", text: c.address, detail: cap(label(c)) + (c.noun ? " · " + c.noun : ""), mark: function () { return changeMark(c); }, rank: 3 + sevRank[c.severity] * 0.1,
        go: function () { if (state.tab !== "changes" && state.tab !== "map" && state.tab !== "apply") setTab("changes", false); go({ kind: "change", id: c.address }); scrollToWorkspace(); } });
    });
    graphNodes.forEach(function (n) {
      if (byResource.has(n.id) && byResource.get(n.id).some(function (c) { return !isUnchanged(c); })) return;
      items.push({ kind: "resource", text: n.id, detail: "No change · " + (n.noun || n.type), mark: function () { return h("span", { class: "sym other", "aria-hidden": "true" }, "·"); }, rank: 1,
        go: function () {
          if (state.tab !== "map") { state.tab = "map"; renderBody(true); syncHash(); }
          if (visibleNodes().indexOf(n.id) < 0) { state.mapScope = "all"; state.view = null; renderMain(); }
          selectNode(n.id);
          scrollToWorkspace();
        } });
    });
    findings.forEach(function (f) {
      var first = (f.addresses || [])[0];
      items.push({ kind: "finding", text: f.title, detail: f.rule + (first ? " · " + first : ""), mark: function () { return sevIcon(f.severity); }, rank: 2 + sevRank[f.severity] * 0.1,
        go: function () {
          var target = first && resolve(first);
          if (target && target.kind !== "output") { if (state.tab !== "changes" && state.tab !== "map") setTab("changes", false); select(target); scrollToWorkspace(); }
          else setTab("findings", true);
        } });
    });
    TABS.forEach(function (t, i) {
      items.push({ kind: "view", text: t.title, detail: "View · " + (i + 1), mark: function () { return icon("arrowRight"); }, rank: 2.5, go: function () { setTab(t.id, true); } });
    });
    var cmds = [
      ["Draw the map before the apply", function () { setTab("map", true); stopPlay(); setMapTime("before"); }],
      ["Draw the map after the apply", function () { setTab("map", true); stopPlay(); setMapTime("after"); }],
      ["Draw the plan's changes on the map", function () { setTab("map", true); stopPlay(); setMapTime("plan"); }],
      ["Save the map as a PNG image", function () { setTab("map", false); saveMapImage("png"); }],
      ["Save the map as an SVG drawing", function () { setTab("map", false); saveMapImage("svg"); }],
    ];
    if (prComment) cmds.push(["Copy a summary for a pull request", function () { copyQuietly(prComment, "Copied a summary for a pull request, in Markdown."); }]);
    if (changedCount()) cmds.push(["Save a summary card as an image", saveSummaryImage]);
    if (toReview.length) cmds.push(["Next change to review", goNextReview]);
    cmds.forEach(function (cmd) { items.push({ kind: "command", text: cmd[0], detail: "Command", mark: function () { return icon("arrowRight"); }, rank: 2, go: cmd[1] }); });
    return items;
  }

  // score rates how well q matches text as a subsequence: matches at the
  // start of words (after . _ - / [ " or a space) and runs of consecutive
  // letters count most, and shorter texts win ties. Returns the matched
  // positions, or null when q is not in text at all.
  function score(text, q) {
    var t = text.toLowerCase(), pos = [], total = 0, last = -2;
    var from = 0;
    var sub = t.indexOf(q);
    if (sub >= 0) {
      for (var k = 0; k < q.length; k++) pos.push(sub + k);
      total = 40 + q.length * 6 - sub * 0.2 + (sub === 0 || /[._\-\/\[" ]/.test(t[sub - 1]) ? 20 : 0);
      return { score: total - t.length * 0.05, pos: pos };
    }
    for (var i = 0; i < q.length; i++) {
      var j = t.indexOf(q[i], from);
      if (j < 0) return null;
      var boundary = j === 0 || /[._\-\/\[" ]/.test(t[j - 1]);
      total += 1 + (boundary ? 6 : 0) + (j === last + 1 ? 4 : 0);
      pos.push(j);
      last = j;
      from = j + 1;
    }
    return { score: total - t.length * 0.05, pos: pos };
  }

  function highlighted(text, pos) {
    var frag = document.createDocumentFragment(), set = new Set(pos), run = "", inRun = false;
    function flush() { if (!run) return; frag.appendChild(inRun ? h("b", null, run) : document.createTextNode(run)); run = ""; }
    for (var i = 0; i < text.length; i++) {
      var m = set.has(i);
      if (m !== inRun) { flush(); inRun = m; }
      run += text[i];
    }
    flush();
    return frag;
  }

  function openPalette() {
    if (paletteEl) return;
    var items = paletteItems();
    var input = h("input", { class: "palette-input", type: "text", placeholder: "Go to a resource, finding or view", spellcheck: "false", autocomplete: "off", role: "combobox", "aria-expanded": "true", "aria-controls": "palette-list", "aria-autocomplete": "list" });
    var list = h("ul", { class: "palette-list", id: "palette-list", role: "listbox", "aria-label": "Results" });
    var foot = h("p", { class: "palette-foot" }, h("kbd", null, "↑"), h("kbd", null, "↓"), " move ", h("kbd", null, "enter"), " go ", h("kbd", null, "esc"), " close ", h("span", { class: "palette-aside" }, h("kbd", null, "/"), " filters the change list"));
    var panel = h("div", { class: "palette", role: "dialog", "aria-modal": "true", "aria-label": "Go to" }, h("div", { class: "palette-field" }, icon("search"), input), list, foot);
    var backdrop = h("div", { class: "palette-backdrop" }, panel);
    var results = [], active = 0;
    var returnFocus = document.activeElement;

    function render() {
      var q = input.value.trim().toLowerCase();
      if (!q) {
        // With nothing typed: the findings that matter most, then the views.
        results = items.filter(function (it) { return it.kind === "finding"; }).slice(0, 5).map(function (it) { return { it: it, pos: [] }; })
          .concat(items.filter(function (it) { return it.kind === "view"; }).map(function (it) { return { it: it, pos: [] }; }));
      } else {
        results = [];
        items.forEach(function (it) {
          var m = score(it.text, q);
          var d = !m && score(it.detail, q);
          if (m) results.push({ it: it, pos: m.pos, s: m.score + it.rank });
          else if (d) results.push({ it: it, pos: [], s: d.score * 0.5 + it.rank });
        });
        results.sort(function (a, b) { return b.s - a.s; });
        results = results.slice(0, 12);
      }
      active = Math.min(active, Math.max(0, results.length - 1));
      list.textContent = "";
      if (!results.length) list.appendChild(h("li", { class: "palette-empty" }, "Nothing in this plan matches."));
      results.forEach(function (r, i) {
        var li = h("li", { id: "palette-" + i, role: "option", "aria-selected": i === active ? "true" : "false", class: "palette-item kind-" + r.it.kind },
          h("span", { class: "palette-mark" }, r.it.mark()),
          h("span", { class: "palette-text" + (r.it.kind === "change" || r.it.kind === "resource" ? " mono" : "") }, highlighted(r.it.text, r.pos)),
          h("span", { class: "palette-detail" }, r.it.detail));
        li.addEventListener("pointermove", function () { if (active !== i) { active = i; sync(); } });
        li.addEventListener("click", function () { choose(i); });
        list.appendChild(li);
      });
      sync();
    }
    function sync() {
      Array.prototype.forEach.call(list.children, function (li, i) { li.setAttribute("aria-selected", i === active ? "true" : "false"); });
      input.setAttribute("aria-activedescendant", results.length ? "palette-" + active : "");
      var el = list.children[active];
      if (el && el.scrollIntoView) el.scrollIntoView({ block: "nearest" });
    }
    function choose(i) {
      var r = results[i];
      close(false);
      if (r) r.it.go();
    }
    function close(restore) {
      if (!paletteEl) return;
      backdrop.remove();
      paletteEl = null;
      if (restore && returnFocus && returnFocus.focus) returnFocus.focus({ preventScroll: true });
    }
    input.addEventListener("input", function () { active = 0; render(); });
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown") { e.preventDefault(); active = Math.min(results.length - 1, active + 1); sync(); }
      else if (e.key === "ArrowUp") { e.preventDefault(); active = Math.max(0, active - 1); sync(); }
      else if (e.key === "Enter") { e.preventDefault(); choose(active); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(true); }
      else if (e.key === "Tab") { e.preventDefault(); }
    });
    backdrop.addEventListener("pointerdown", function (e) { if (e.target === backdrop) close(true); });
    paletteEl = backdrop;
    document.body.appendChild(backdrop);
    render();
    input.focus();
  }

  // ---------------------------------------------------------------- review

  // A review is a pass over the changes that need a look: destroys,
  // replacements, and anything with a finding of medium or worse. Each can
  // be marked reviewed, with a note, and the notes copied as a Markdown
  // checklist for a pull request or a change ticket. Reviews are kept in
  // this browser, per directory, and a review sticks to a change only while
  // it is the same change: if a later plan replaces what this one updates,
  // it needs a look again.
  var REVIEW_KEY = "interragate-review:" + (plan.dir || plan.source || "plan");
  var reviews = (function () {
    try { return JSON.parse(localStorage.getItem(REVIEW_KEY) || "{}") || {}; } catch (e) { return {}; }
  })();
  var reviewLineEl = null;

  function needsLook(c) {
    return !isUnchanged(c) && (c.action === "delete" || c.action === "replace" || sevRank[c.severity] >= 3);
  }

  // A mark holds for the change that was reviewed, not for the address: the
  // key covers what the change does, so a different plan for the resource,
  // or another report that happens to share the directory name, starts
  // unreviewed.
  function reviewKey(c) { return [c.action, c.order || "", hash(JSON.stringify(c.diff || null))].join("|"); }

  // hash is FNV-1a, enough to tell two changes apart.
  function hash(s) {
    var x = 0x811c9dc5;
    for (var i = 0; i < s.length; i++) { x ^= s.charCodeAt(i); x = Math.imul(x, 0x01000193); }
    return (x >>> 0).toString(36);
  }

  function isReviewed(c) { var r = reviews[c.address]; return !!r && r.key === reviewKey(c); }

  // The changes to review, worst first, then in address order.
  var toReview = changes.filter(needsLook).sort(function (a, b) {
    return (sevRank[b.severity] - sevRank[a.severity]) || (rankAction(b.action) - rankAction(a.action)) || (a.address < b.address ? -1 : 1);
  });

  function saveReviews() {
    try { localStorage.setItem(REVIEW_KEY, JSON.stringify(reviews)); } catch (e) {}
  }

  function setReviewed(c, on, note) {
    if (on) reviews[c.address] = { key: reviewKey(c), note: note != null ? note : ((reviews[c.address] || {}).note || ""), at: new Date().toISOString() };
    else delete reviews[c.address];
    saveReviews();
    syncReview(c);
  }

  // After a change is marked or unmarked, the row, the progress line and the
  // detail panel catch up without a full render.
  function syncReview(c) {
    var row = rowEls && rowEls.get(c.address);
    if (row) {
      var old = row.querySelector(".tag.done");
      if (old) old.remove();
      if (isReviewed(c)) row.querySelector(".marks").prepend(reviewedTag());
    }
    fillReviewLine();
  }

  function reviewedTag() { return h("span", { class: "tag done", title: "You marked this reviewed" }, "reviewed"); }

  function reviewedCount() { return toReview.filter(isReviewed).length; }

  function nextToReview() {
    var current = state.sel && state.sel.kind === "change" ? state.sel.id : null;
    var start = current ? toReview.findIndex(function (c) { return c.address === current; }) : -1;
    for (var i = 1; i <= toReview.length; i++) {
      var c = toReview[(start + i + toReview.length) % toReview.length];
      if (!isReviewed(c)) return c;
    }
    return null;
  }

  function goNextReview() {
    var c = nextToReview();
    if (!c) return;
    if (state.tab !== "changes" && state.tab !== "map") setTab("changes", false);
    select({ kind: "change", id: c.address });
  }

  function reviewLine() {
    if (!toReview.length) return null;
    reviewLineEl = h("div", { class: "review-line" });
    fillReviewLine();
    return reviewLineEl;
  }

  function fillReviewLine() {
    if (!reviewLineEl) return;
    var done = reviewedCount(), total = toReview.length;
    reviewLineEl.textContent = "";
    var text = done === 0 ? plural(total, "change needs", "changes need") + " a look before this plan is applied."
      : done === total ? "You have reviewed every change that needs a look."
      : "You have reviewed " + done + " of the " + plural(total, "change", "changes") + " that need a look.";
    reviewLineEl.appendChild(h("p", null, text));
    var actions = h("div", { class: "review-actions" });
    if (done < total) actions.appendChild(h("button", { class: "text-link", type: "button", title: "The next change to review (n)", onclick: goNextReview }, done ? "Next to review" : "Start reviewing", h("kbd", null, "n")));
    if (done) {
      var copy = h("button", { class: "copy", type: "button", title: "Copy the review as a Markdown checklist" }, icon("copy"), h("span", { class: "copy-label", "data-label": "Copy review notes" }, "Copy review notes"));
      copy.addEventListener("click", function (e) { e.stopPropagation(); copyText(reviewMarkdown(), copy); });
      actions.appendChild(copy);
    }
    reviewLineEl.appendChild(actions);
  }

  // The control at the top of a change that needs a look.
  function reviewControl(c) {
    var wrap = h("div", { class: "review" + (isReviewed(c) ? " is-done" : "") });
    var input = h("input", { type: "checkbox" });
    input.checked = isReviewed(c);
    var note = h("textarea", { class: "review-note", rows: "2", placeholder: "What you checked, for the review notes (optional)", "aria-label": "Review note" });
    note.value = (reviews[c.address] && reviews[c.address].note) || "";
    note.hidden = !input.checked;
    input.addEventListener("change", function () {
      setReviewed(c, input.checked, note.value);
      wrap.classList.toggle("is-done", input.checked);
      note.hidden = !input.checked;
    });
    note.addEventListener("input", function () { if (isReviewed(c)) { reviews[c.address].note = note.value; saveReviews(); } });
    wrap.appendChild(h("label", { class: "check" }, input, "Reviewed", h("span", { class: "review-key" }, h("kbd", null, "r"))));
    wrap.appendChild(note);
    return wrap;
  }

  function reviewMarkdown() {
    var when = plan.timestamp ? new Date(plan.timestamp) : null;
    var head = "### Plan review" + (plan.dir ? ": " + plan.dir : "");
    var meta = [TOOL + (plan.version ? " " + plan.version : "")];
    if (when && !isNaN(when)) meta.push("planned " + when.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }));
    var lines = [head, "", "_" + meta.join(", ") + ". Reviewed " + reviewedCount() + " of " + plural(toReview.length, "change", "changes") + " that need a look._", ""];
    toReview.forEach(function (c) {
      var r = reviews[c.address];
      var done = isReviewed(c);
      var why = "";
      var f = (c.findings || []).map(function (id) { return findingById.get(id); }).filter(Boolean)[0];
      if (f) why = f.title;
      else if (c.why) why = firstSentence(c.why).replace(/\.$/, "");
      var line = "- [" + (done ? "x" : " ") + "] " + cap(label(c)) + " `" + c.address + "`" + (sevRank[c.severity] >= 2 ? " (" + c.severity + ")" : "") + (why ? ": " + why.replace(/`/g, "") : "");
      if (done && r && r.note) line += "  \n  " + r.note.trim().replace(/\n+/g, " ");
      lines.push(line);
    });
    return lines.join("\n") + "\n";
  }

  // -------------------------------------------------------------- memory

  // Outside interragate watch, the page remembers a little about the reports
  // opened for a configuration (in this browser only) and leads with what
  // changed since the last one: a pull request's report after new commits,
  // say. Two slots keep it steady across reloads: opening a new report moves
  // the current one to "before".
  // Each workspace and branch has its own: production's report is no
  // "before" for staging's, nor one pull request's for another's.
  var MEMORY_KEY = "interragate-seen:" + [plan.dir || plan.source || "plan", plan.workspace || "default", git && git.branch || ""].join("|");
  // Every report opened from a file shares one store, so only the most
  // recent configurations are kept, oldest out first; review marks, notes
  // and the theme need the room too.
  var MEMORY_INDEX = "interragate-seen-index";
  var MEMORY_KEEP = 6;

  // A snapshot from before this shape compares with nothing, once.
  var SNAPSHOT_VERSION = 3;

  function storeMemory(key, value) {
    var idx = [];
    try {
      idx = JSON.parse(localStorage.getItem(MEMORY_INDEX) || "[]") || [];
      // Memories saved before the index existed are the oldest of all.
      for (var i = 0; i < localStorage.length; i++) {
        var k = localStorage.key(i);
        if (k && k.indexOf("interragate-seen:") === 0 && idx.indexOf(k) < 0) idx.push(k);
      }
    } catch (e) { return; }
    idx = idx.filter(function (k) { return k !== key; });
    idx.unshift(key);
    while (idx.length > MEMORY_KEEP) { try { localStorage.removeItem(idx.pop()); } catch (e) {} }
    for (;;) {
      try { localStorage.setItem(key, value); break; } catch (e) {
        if (idx.length <= 1) return; // too big to keep at all
        try { localStorage.removeItem(idx.pop()); } catch (e2) { return; }
      }
    }
    try { localStorage.setItem(MEMORY_INDEX, JSON.stringify(idx)); } catch (e) {}
  }

  // planned keeps what a diff plans, not what the object holds now: the
  // same plan from different state is the same plan.
  function planned(n) {
    if (!n) return null;
    return { k: n.key, a: n.after, u: n.unknown || false, c: (n.children || []).map(planned) };
  }

  function snapshot() {
    var actions = {}, follows = {}, values = {}, orders = {};
    changes.forEach(function (c) {
      if (isUnchanged(c)) return;
      actions[c.address] = c.action;
      // Values: what the plan sets, so the same action with other values
      // still counts as a change. Causes: what a change follows from, so a
      // sentence names a cause only for what really follows from it.
      values[c.address] = hash(JSON.stringify(planned(c.diff)));
      if (c.order) orders[c.address] = c.order;
      if (c.indirect) follows[c.address] = (c.causes || []).map(function (k) { return k.address || k.resource; });
    });
    var snap = {
      v: SNAPSHOT_VERSION, at: data.generated_at, planned: plan.timestamp || data.generated_at, risk: (data.risk || {}).level || "none",
      actions: actions, follows: follows, values: values, orders: orders,
      // A finding is what it says about what: the same rule on the same
      // resource can flag another port.
      findings: findings.map(function (f) {
        var text = firstSentence(f.message || f.title);
        return { key: f.rule + "|" + (f.addresses || []).slice().sort().join(",") + "|" + text, text: text, sev: f.severity, refs: f.addresses || [] };
      }),
      nodes: graphNodes.map(function (n) { return n.id; }),
    };
    // The same plan opened twice is one report, whenever it was rendered.
    snap.id = hash(JSON.stringify([actions, values, orders, snap.findings.map(function (f) { return f.key + f.sev; })]));
    return snap;
  }

  // rememberAndCompare stores this report and returns what changed since
  // the one before it, or null when there is nothing to compare with.
  function rememberAndCompare() {
    if (liveData) return null;
    var now = snapshot(), mem;
    try { mem = JSON.parse(localStorage.getItem(MEMORY_KEY) || "{}") || {}; } catch (e) { return null; }
    if (!mem.cur || mem.cur.id !== now.id) { mem.prev = mem.cur || null; mem.cur = now; }
    storeMemory(MEMORY_KEY, JSON.stringify(mem));
    var before = mem.prev;
    if (!before || before.v !== SNAPSHOT_VERSION) return null;
    // Two plans of the same configuration share most of their resources;
    // a directory name alone could belong to another project.
    var seen = new Set(before.nodes || []), shared = 0;
    now.nodes.forEach(function (id) { if (seen.has(id)) shared++; });
    if (shared < 0.5 * Math.max(now.nodes.length, seen.size)) return null;
    return compareSnapshots(before, now);
  }

  var VERB = { create: "creates", update: "updates", replace: "replaces", "delete": "destroys", read: "reads", forget: "forgets", "no-op": "moves or imports" };
  var PART = { create: "created", update: "updated", replace: "replaced", "delete": "destroyed", read: "read", forget: "forgotten", "no-op": "moved or imported" };

  // compareSnapshots says what changed, most important first, as interragate
  // watch does: risk, new findings, fixed ones, then resources whose planned
  // action differs, grouped, with causes named and followers counted.
  function compareSnapshots(a, b) {
    var out = [];
    // In the verdict's own words; low and info both read Low risk.
    if (riskLabel(a.risk) !== riskLabel(b.risk)) {
      var rose = sevRank[b.risk] > sevRank[a.risk];
      out.push({ severity: rose ? b.risk : "none", text: "The verdict " + (rose ? "rose" : "fell") + " from " + riskLabel(a.risk) + " to " + riskLabel(b.risk) + "." });
    }
    var oldF = new Map(a.findings.map(function (f) { return [f.key, f]; }));
    var newF = new Map(b.findings.map(function (f) { return [f.key, f]; }));
    b.findings.forEach(function (f) {
      var was = oldF.get(f.key);
      if (!was) out.push({ severity: f.sev, text: "New: " + f.text });
      else if (sevRank[f.sev] > sevRank[was.sev]) out.push({ severity: f.sev, text: "Now " + (SEV_LABEL[f.sev] || f.sev).toLowerCase() + ", not " + (SEV_LABEL[was.sev] || was.sev).toLowerCase() + ": " + f.text });
    });
    a.findings.forEach(function (f) { if (!newF.has(f.key)) out.push({ severity: "none", text: "Fixed: " + f.text }); });
    var groups = new Map();
    var revalued = [], reordered = [];
    Object.keys(Object.assign({}, a.actions, b.actions)).sort().forEach(function (addr) {
      var was = a.actions[addr] || "", now = b.actions[addr] || "";
      if (was === now) {
        // The same action can still set other values or swap the order.
        if ((a.orders || {})[addr] !== (b.orders || {})[addr] && b.orders && b.orders[addr]) reordered.push(addr);
        else if (a.values && b.values && a.values[addr] !== b.values[addr]) revalued.push(addr);
        return;
      }
      var key = was + ">" + now;
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(addr);
    });
    var follows = Object.assign({}, a.follows, b.follows);
    var harmful = function (x) { return x === "delete" || x === "replace"; };
    var ordered = Array.from(groups.entries()).sort(function (x, y) {
      var cx = classOf(x[0]), cy = classOf(y[0]);
      return cx - cy;
    });
    function classOf(key) {
      var p = key.split(">");
      if (harmful(p[1]) && rankAction(p[1]) > rankAction(p[0])) return 0;
      if (harmful(p[0]) && rankAction(p[1]) < rankAction(p[0])) return 1;
      return 2;
    }
    ordered.forEach(function (entry) {
      var p = entry[0].split(">"), addrs = entry[1];
      var causes = addrs.filter(function (x) { return !follows[x]; }), rest = addrs.filter(function (x) { return follows[x]; });
      var q = function (x) { return "`" + x + "`"; };
      // Name a cause only when what is counted really follows from it.
      var explained = rest.every(function (x) {
        var why = Array.isArray(follows[x]) ? follows[x] : [];
        return why.some(function (k) { return causes.some(function (c) { return c === k || c.indexOf(k + "[") === 0 || c.indexOf(k + ".") === 0; }); });
      });
      var subject;
      if (causes.length && causes.length <= 2 && rest.length > 1 && explained) subject = causes.map(q).join(", ") + " and " + rest.length + " more that follow from " + (causes.length === 1 ? "it" : "them");
      else if (addrs.length > 3) subject = addrs.slice(0, 2).map(q).join(", ") + " and " + (addrs.length - 2) + " more";
      else subject = addrs.length === 1 ? q(addrs[0]) : addrs.slice(0, -1).map(q).join(", ") + " and " + q(addrs[addrs.length - 1]);
      var text = !p[0] ? "Now " + VERB[p[1]] + " " + subject + "."
        : !p[1] ? "No longer " + VERB[p[0]] + " " + subject + "."
        : subject + (addrs.length > 1 ? " are" : " is") + " now " + PART[p[1]] + ", not " + PART[p[0]] + ".";
      var cls = classOf(entry[0]);
      out.push({ severity: cls === 0 ? "medium" : cls === 1 ? "none" : "info", text: text });
    });
    reordered.forEach(function (addr) {
      var first = b.orders[addr] === "create-first";
      out.push({ severity: first ? "none" : "medium", text: first ? "Now creates the replacement for `" + addr + "` before destroying the old one." : "Now destroys `" + addr + "` before creating its replacement." });
    });
    if (revalued.length) {
      var q2 = function (x) { return "`" + x + "`"; };
      out.push({ severity: "info", text: (revalued.length === 1 ? q2(revalued[0]) + " now plans different values." : (revalued.length <= 3 ? andList(revalued.map(q2), 3) : andList(revalued.slice(0, 2).map(q2), 2) + " and " + (revalued.length - 2) + " more") + " now plan different values.") });
    }
    if (out.length > 8) { var more = out.length - 7; out = out.slice(0, 7); out.push({ severity: "info", text: "And " + more + " more differences." }); }
    if (!out.length) out.push({ severity: "none", text: "The plan is the same as before." });
    return { since: a.planned, sentences: out };
  }

  // ------------------------------------------------------------------ live

  // Under interragate watch the page is one step in a session. It listens
  // for newer plans, says when one is running or has failed, and shows what
  // the last edit changed. A newer report reloads the page, which keeps its
  // scroll position, selection and filters.
  var LIVE_KEY = "interragate-live";

  function liveSave() {
    var core = document.querySelector(".detail > .core");
    var snap = {
      y: window.scrollY, core: core ? core.scrollTop : 0,
      q: state.q, actions: state.actions, cosmetic: state.cosmetic, unchanged: state.unchanged,
      collapsed: Array.from(state.collapsed), mapScope: state.mapScope, mapGroup: state.mapGroup,
      view: state.view, noiseDrift: state.noiseDrift, mapTime: state.mapTime, groupBy: state.groupBy,
    };
    try { sessionStorage.setItem(LIVE_KEY, JSON.stringify(snap)); } catch (e) {}
  }

  // liveTake returns what the reload before this one saved, once.
  function liveTake() {
    if (!liveData) return null;
    try {
      var raw = sessionStorage.getItem(LIVE_KEY);
      sessionStorage.removeItem(LIVE_KEY);
      return raw ? JSON.parse(raw) : null;
    } catch (e) { return null; }
  }

  function liveApply(snap) {
    ["q", "cosmetic", "unchanged", "mapScope", "mapGroup", "view", "noiseDrift", "mapTime", "groupBy"].forEach(function (k) { if (k in snap) state[k] = snap[k]; });
    if (snap.actions) state.actions = snap.actions;
    if (snap.collapsed) state.collapsed = new Set(snap.collapsed);
  }

  function clockTime(iso) {
    var t = new Date(iso);
    return isNaN(t) ? "" : t.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  function duration(sec) {
    if (sec < 10) return sec.toFixed(1) + "s";
    if (sec < 60) return Math.round(sec) + "s";
    return Math.floor(sec / 60) + "m " + Math.round(sec % 60) + "s";
  }

  function fileList(files) {
    if (files.length === 1) return files[0];
    if (files.length === 2) return files[0] + " and " + files[1];
    return files[0] + " and " + plural(files.length - 1, "more file");
  }

  function deltaEmpty(d) {
    return d.risk_before === d.risk_after && !(d.changes || []).length && !(d.found || []).length && !(d.fixed || []).length && !(d.outputs || []).length;
  }

  function sinceList(d) {
    var list = h("ol", { class: "matters since-list" });
    (d.sentences || []).forEach(function (it) {
      list.appendChild(h("li", { class: sevRank[it.severity] <= 1 ? "info" : null }, sevIcon(it.severity), h("span", null, rich(it.text))));
    });
    return list;
  }

  // The hero says what the last save did to the plan, first thing; outside
  // watch, what changed since the last report opened for this
  // configuration.
  var remembered = null;
  function sinceBlock() {
    var d = liveData && liveData.delta;
    if (!d) {
      remembered = remembered || rememberAndCompare();
      if (!remembered) return null;
      var when = new Date(remembered.since);
      var head = "Since the report you opened before" + (isNaN(when) ? "" : ", planned " + when.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }));
      return h("div", Object.assign({ class: "since" }, revealAttrs(2)), h("h2", null, head), sinceList(remembered));
    }
    var head2 = "Since the previous plan";
    if (liveData.changed && liveData.changed.length) head2 += ", after " + fileList(liveData.changed) + " changed";
    return h("div", Object.assign({ class: "since" }, revealAttrs(2)), h("h2", null, head2), sinceList(d));
  }

  var SINCE_WORD = { create: "created", update: "updated", replace: "replaced", "delete": "destroyed", read: "read", forget: "forgotten", "no-op": "moved" };

  function sinceMark(c) {
    var d = sinceByAddr.get(c.address);
    if (!d) return null;
    var word = d.before && d.before === d.after ? "changed" : d.before ? "was " + (SINCE_WORD[d.before] || d.before) : d.after === "create" ? "new" : "was unchanged";
    var title = d.before && d.before === d.after ? "Plans " + (d.order ? "a different order" : "different values") + " than before" + (d.attributes && d.attributes.length ? ": " + d.attributes.slice(0, 4).join(", ") : "") : "Different from the previous plan";
    return h("span", { class: "tag since", title: title }, word);
  }

  // The status sits in the bar, in view wherever the page is scrolled: a
  // dot and a few words, with the details a click away.
  function liveStatus() {
    var text = h("span", { class: "live-text" });
    var btn = h("button", { class: "live", type: "button", "aria-expanded": "false", "aria-controls": "live-pop" }, h("span", { class: "live-dot", "aria-hidden": "true" }), text);
    var pop = h("div", { class: "live-pop", id: "live-pop", hidden: "" });
    var wrap = h("div", { class: "live-wrap" }, btn, pop);
    var current = {};
    var panel = null, dismissed = "";
    var d = liveData.delta;
    var unseen = !!d && !deltaEmpty(d);

    function fill() {
      pop.textContent = "";
      if (liveData.historic) {
        pop.appendChild(h("p", { class: "live-head" }, "An earlier plan"));
        pop.appendChild(h("p", null, "This is the plan from " + clockTime(data.generated_at) + ", kept while you work. ", h("a", { class: "inline-btn", href: liveBase }, "Go to the latest plan")));
      }
      pop.appendChild(h("p", { class: "live-head" }, current.gone ? "No longer watching " + liveData.dir : "Watching " + liveData.dir));
      pop.appendChild(h("p", null, current.gone
        ? "interragate watch has stopped, so this is the last plan it made."
        : "interragate plans again whenever you save a .tf or .tfvars file, and this page follows."));
      var line = "This plan ran at " + clockTime(data.generated_at);
      if (liveData.seconds) line += " and took " + duration(liveData.seconds);
      if (liveData.changed && liveData.changed.length) line += ", after " + fileList(liveData.changed) + " changed";
      pop.appendChild(h("p", null, line + "."));
      if (d) {
        pop.appendChild(h("p", { class: "live-head" }, "Since the previous plan"));
        pop.appendChild(sinceList(d));
      }
      if (current.error) {
        pop.appendChild(h("p", { class: "live-head" }, "The latest plan failed"));
        pop.appendChild(h("pre", { class: "live-err" }, current.error));
      }
      // Every plan this session made, newest first, each a page of its own.
      var past = (current.history || []).slice().reverse();
      if (past.length > 1) {
        pop.appendChild(h("p", { class: "live-head" }, "This session"));
        var ol = h("ol", { class: "live-history" });
        past.forEach(function (e) {
          var here = e.version === liveData.version;
          var latest = e.version === current.version;
          var why = e.changed && e.changed.length ? fileList(e.changed) : e.version === 1 ? "first plan" : "";
          var lead = e.headline ? (/^.*?\.(?=\s|$)/.exec(e.headline) || [e.headline])[0] : "";
          var label = [h("span", { class: "lh-time mono" }, clockTime(e.at)),
            h("span", { class: "lh-risk" }, sevIcon(e.risk), riskLabel(e.risk), why ? h("span", { class: "lh-why" }, why) : null),
            lead ? h("span", { class: "lh-what" }, rich(lead)) : null];
          ol.appendChild(h("li", { class: here ? "here" : null },
            here ? h("span", { class: "lh-row", "aria-current": "page" }, label) : h("a", { class: "lh-row", href: liveBase + (latest ? "" : "v/" + e.version + "/") }, label)));
        });
        pop.appendChild(ol);
      }
    }
    function showError(msg) {
      if (panel) panel.remove();
      panel = h("div", { class: "live-error", role: "alert" },
        h("p", { class: "live-head" }, "The plan failed"),
        h("p", null, "This page keeps the last plan that worked. Fix the problem and save, and interragate plans again."),
        h("pre", { class: "live-err" }, msg),
        h("button", { class: "text-link", type: "button", onclick: function () { dismissed = msg; panel.remove(); panel = null; } }, "Hide"));
      document.body.appendChild(panel);
    }
    function set(st) {
      current = st;
      wrap.className = "live-wrap " + (st.gone ? "gone" : st.planning ? "planning" : st.error ? "failed" : "ok");
      setFavicon(st.gone ? null : st.planning ? "#eda100" : st.error ? "#d6457f" : null);
      text.textContent = st.gone ? "Not watching" : st.planning ? "Planning…" : st.error ? "Plan failed" : liveData.historic ? "An earlier plan" : unseen ? "See what changed" : "Up to date";
      btn.title = st.planning && st.changed && st.changed.length ? fileList(st.changed) + " changed" : "";
      if (st.error && !st.planning && st.error !== dismissed) showError(st.error);
      if (!st.error && panel) { panel.remove(); panel = null; }
      if (!pop.hidden) fill();
    }
    function toggle(open) {
      pop.hidden = !open;
      btn.setAttribute("aria-expanded", open ? "true" : "false");
      if (!open) return;
      fill();
      if (unseen) { unseen = false; set(current); }
    }
    btn.addEventListener("click", function () { toggle(pop.hidden); });
    document.addEventListener("click", function (e) { if (!pop.hidden && !wrap.contains(e.target)) toggle(false); });
    wrap.addEventListener("keydown", function (e) { if (e.key === "Escape" && !pop.hidden) { e.stopPropagation(); toggle(false); btn.focus(); } });
    set({});
    // An update that lands in a background tab marks the title until seen.
    if (unseen && document.hidden) {
      var title = document.title;
      document.title = "● " + title;
      document.addEventListener("visibilitychange", function restore() {
        if (document.hidden) return;
        document.title = title;
        document.removeEventListener("visibilitychange", restore);
      });
    }
    return { el: wrap, set: set };
  }

  // An earlier plan of the session is served under v/N/ below the session's
  // own address, and stays put when newer plans arrive.
  var liveBase = liveData ? location.pathname.replace(/v\/\d+\/$/, "") : "";

  function liveConnect(status) {
    if (!("EventSource" in window)) return;
    var es = new EventSource(liveBase + "events");
    es.onmessage = function (e) {
      var st;
      try { st = JSON.parse(e.data); } catch (err) { return; }
      if (st.version > liveData.version && !liveData.historic) {
        liveSave();
        es.close();
        location.reload();
        return;
      }
      status.set(st);
    };
    // The stream drops when interragate watch stops; the browser keeps
    // trying.
    es.onerror = function () { status.set({ gone: true }); };
  }

  // ------------------------------------------------------------------ boot

  // With nothing changing, the map is the infrastructure as it stands.
  if (!changedCount()) state.mapScope = "all";
  var restored = liveTake();
  if (restored) liveApply(restored);
  // The page restores its own scroll after a live reload, once the fonts
  // have set the layout; the browser's guess would land first and jump.
  if (liveData && "scrollRestoration" in history) history.scrollRestoration = "manual";
  var deepLinked = readHash();
  if (!state.sel && findings.length) {
    var first = findings[0].addresses && findings[0].addresses[0];
    var initial = first && resolve(first);
    // Chosen for the reader, not by them: the Apply view may show something
    // more useful until they pick for themselves.
    if (initial && initial.kind !== "output") { state.sel = initial; state.autoSel = true; }
  }

  setFavicon(null);
  var app = document.getElementById("app");
  var bar = topBar();
  if (liveData) {
    var liveEl = liveStatus();
    bar.querySelector(".bar-tools").prepend(liveEl.el);
    liveConnect(liveEl);
  }
  app.appendChild(bar);
  var page = h("main", { class: "page" });
  page.appendChild(heroView());
  page.appendChild(overviewView());
  wsHead = h("div");
  workspace = h("div");
  page.appendChild(wsHead);
  page.appendChild(workspace);
  var genTime = new Date(data.generated_at);
  page.appendChild(h("footer", { class: "foot" },
    h("span", null, (data.generator || "interragate") + (isNaN(genTime) ? "" : " · generated " + genTime.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })) + ". Sensitive values are masked; treat this file like the plan itself."),
    h("span", null, "Set in Geist, Geist Mono and Geist Pixel (SIL OFL) · Icons by Phosphor (MIT)")));
  app.appendChild(page);
  watchBar(bar);
  renderBody(false);
  if (!deepLinked && !restored) {
    wsHead.setAttribute("data-reveal", "soft");
    workspace.setAttribute("data-reveal", "soft");
  }
  // A live reload replaces a page the reader was already looking at, so it
  // appears at once, where they were.
  if (restored) page.querySelectorAll("[data-reveal]").forEach(function (el) { el.classList.add("is-in"); });
  else reveal(page);
  syncThemeColor();
  // Deep links scroll once the fonts are in, so the hero has its final
  // height. fonts.ready alone can resolve before a face is even requested.
  if (deepLinked || restored) {
    var faces = document.fonts && document.fonts.load ? Promise.all(["600 16px Geist", "500 16px 'Geist Mono'", "400 16px 'Geist Pixel'"].map(function (f) { return document.fonts.load(f); })) : Promise.resolve();
    faces.catch(function () {}).then(function () {
      requestAnimationFrame(function () {
        if (!restored) { wsHead.scrollIntoView({ block: "start" }); return; }
        window.scrollTo(0, restored.y || 0);
        var core = document.querySelector(".detail > .core");
        if (core) core.scrollTop = restored.core || 0;
      });
    });
  }
})();

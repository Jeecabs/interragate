// interragate's website: small enhancements. Every page reads in full without
// them, and with reduced motion every drawing shows its result at once.
(function () {
  "use strict";

  var COPY = '<svg viewBox="0 0 256 256" aria-hidden="true"><path d="M216,34H88a6,6,0,0,0-6,6V82H40a6,6,0,0,0-6,6V216a6,6,0,0,0,6,6H168a6,6,0,0,0,6-6V174h42a6,6,0,0,0,6-6V40A6,6,0,0,0,216,34ZM162,210H46V94H162Zm48-48H174V88a6,6,0,0,0-6-6H94V46H210Z"/></svg>';
  var CHECK = '<svg viewBox="0 0 256 256" aria-hidden="true"><path d="M228.24,76.24l-128,128a6,6,0,0,1-8.48,0l-56-56a6,6,0,0,1,8.48-8.48L96,191.51,219.76,67.76a6,6,0,0,1,8.48,8.48Z"/></svg>';
  var REPLAY = '<svg viewBox="0 0 256 256" aria-hidden="true"><path d="M222,128a94,94,0,0,1-92.73,94H128a93.43,93.43,0,0,1-64.52-25.65,6,6,0,1,1,8.24-8.72A82,82,0,1,0,70,70l-.19.19L45.08,94H72a6,6,0,0,1,0,12H32a6,6,0,0,1-6-6V60a6,6,0,0,1,12,0V86.4L61.5,62.84A94,94,0,0,1,222,128Z"/></svg>';

  var slice = function (list) { return Array.prototype.slice.call(list); };
  var reduce = window.matchMedia("(prefers-reduced-motion: reduce)");
  var watch = "IntersectionObserver" in window;

  // seen calls fn once, when enough of el is on screen.
  function seen(el, fn, amount) {
    if (!watch) return fn();
    var io = new IntersectionObserver(function (entries) {
      if (entries.some(function (e) { return e.isIntersecting; })) {
        io.disconnect();
        fn();
      }
    }, { threshold: amount || 0.3 });
    io.observe(el);
  }

  // count runs the numbers in root up from zero.
  function count(root) {
    slice(root.querySelectorAll("[data-count]")).forEach(function (b) {
      var to = +b.getAttribute("data-count") || +b.textContent;
      b.setAttribute("data-count", to);
      if (reduce.matches || !to) { b.textContent = to; return; }
      var start = performance.now();
      (function step(now) {
        var p = Math.min(1, (now - start) / 900);
        b.textContent = Math.round(to * (1 - Math.pow(1 - p, 3)));
        if (p < 1) requestAnimationFrame(step);
      })(start);
    });
  }

  function escape(s) {
    return String(s).replace(/[&<>"]/g, function (c) { return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]; });
  }

  // The verdict: its squares arrive row by row. Each phrase of the headline
  // lights the square it is about, and the panel beside says why that
  // resource changes; pointing at any square does the same for it.
  var verdict = document.querySelector("[data-verdict]");
  if (verdict) {
    var detail = verdict.querySelector("[data-detail]");
    var units = slice(verdict.querySelectorAll(".unit"));
    var phrases = slice(verdict.querySelectorAll(".phrase"));
    var icons = {};
    var tpl = verdict.querySelector("template[data-sev-icons]");
    if (tpl) slice(tpl.content.querySelectorAll("[data-sev]")).forEach(function (i) { icons[i.getAttribute("data-sev")] = i.innerHTML; });

    var lit = null;
    var light = function (address) {
      if (address === lit) return;
      lit = address;
      units.forEach(function (u) { u.classList.toggle("lit", u.getAttribute("data-address") === address); });
      phrases.forEach(function (p) { p.classList.toggle("lit", p.getAttribute("data-address") === address); });
      var u = units.filter(function (u) { return u.getAttribute("data-address") === address; })[0];
      if (!u || !detail) return;
      var why = escape(u.getAttribute("data-why") || "").replace(/`([^`]+)`/g, "<code>$1</code>");
      var finding = u.getAttribute("data-finding");
      detail.innerHTML =
        '<p class="d-addr"><b class="sym ' + u.classList[1] + '">' + escape(u.getAttribute("data-sym")) + "</b> <code>" + escape(address) + "</code></p>" +
        '<p class="d-noun">' + escape(u.getAttribute("data-noun")) + "</p>" +
        '<p class="d-why">' + why + "</p>" +
        (finding ? '<p class="d-finding">' + (icons[u.getAttribute("data-sev")] || "") + "<span>" + escape(finding) + "</span></p>" : "");
      detail.classList.remove("swap");
      void detail.offsetWidth;
      detail.classList.add("swap");
    };

    // Once through the headline's phrases, then it rests on the first.
    var turn = 0, timer = null, held = false;
    var cycle = function () {
      clearTimeout(timer);
      if (held || !phrases.length) return;
      light(phrases[turn % phrases.length].getAttribute("data-address"));
      turn++;
      timer = turn <= phrases.length ? setTimeout(cycle, 3400) : null;
    };
    var hold = function (address) { held = true; clearTimeout(timer); light(address); };
    verdict.addEventListener("mouseover", function (e) {
      var t = e.target.closest(".unit, .phrase");
      if (t) hold(t.getAttribute("data-address"));
    });
    verdict.addEventListener("focusin", function () { held = true; clearTimeout(timer); });

    if (reduce.matches) {
      verdict.classList.add("shown");
      if (phrases.length) light(phrases[0].getAttribute("data-address"));
    } else {
      slice(verdict.querySelectorAll("[data-count]")).forEach(function (b) { b.setAttribute("data-count", b.textContent); b.textContent = "0"; });
      seen(verdict, function () {
        requestAnimationFrame(function () {
          verdict.classList.add("shown");
          count(verdict);
          timer = setTimeout(cycle, 1500);
        });
      }, 0.2);
    }
  }

  // The story: each change's scene plays its steps when its chapter is
  // read. On a wide screen the scenes take turns beside the chapters.
  var story = document.querySelector("[data-story]");
  if (story) {
    var scenes = slice(story.querySelectorAll(".scene"));
    var chapters = slice(story.querySelectorAll(".chapter"));
    var wide = window.matchMedia("(min-width: 1000px)");
    var BEAT = 1250;

    var steps = function (scene) { return +scene.getAttribute("data-beats") || 4; };

    // show sets a scene at step k; jump does it without the transitions.
    var show = function (scene, k) {
      for (var j = 1; j <= 4; j++) scene.classList.toggle("b" + j, j <= k);
      scene.classList.toggle("ended", k >= steps(scene));
    };
    var jump = function (scene, k) {
      scene.classList.add("instant");
      show(scene, k);
      void scene.offsetWidth;
      scene.classList.remove("instant");
    };
    var stop = function (scene) {
      (scene._timers || []).forEach(clearTimeout);
      scene._timers = [];
    };
    var play = function (scene) {
      stop(scene);
      if (reduce.matches) return show(scene, steps(scene));
      jump(scene, 0);
      for (var k = 1; k <= steps(scene); k++) {
        scene._timers.push(setTimeout(show, 350 + (k - 1) * BEAT, scene, k));
      }
    };

    scenes.forEach(function (scene) {
      scene.style.setProperty("--beat", BEAT + "ms");
      slice(scene.querySelectorAll(".beats li")).forEach(function (li, k) {
        var b = document.createElement("button");
        b.type = "button";
        b.textContent = li.textContent;
        b.addEventListener("click", function () { stop(scene); show(scene, k + 1); });
        li.textContent = "";
        li.appendChild(b);
      });
      var again = document.createElement("button");
      again.type = "button";
      again.className = "replay";
      again.setAttribute("aria-label", "Play again");
      again.innerHTML = REPLAY;
      again.addEventListener("click", function () { play(scene); });
      scene.appendChild(again);
      if (!reduce.matches) jump(scene, 0);
    });

    // On a wide screen, the chapter across the middle of the window is the
    // one being read.
    var current = -1;
    var read = function (i) {
      if (i === current) return;
      current = i;
      chapters.forEach(function (c, j) { c.classList.toggle("on", j === i); });
      scenes.forEach(function (s, j) {
        s.classList.toggle("on", j === i);
        if (j === i) play(s);
        else stop(s);
      });
    };
    var pick = function () {
      if (!wide.matches) return;
      var h = window.innerHeight, at = -1, best = h * 0.45;
      chapters.forEach(function (c, j) {
        var r = c.getBoundingClientRect(), off = Math.abs(r.top + r.height / 2 - h / 2);
        if (off < best) { best = off; at = j; }
      });
      if (at >= 0) read(at);
      else if (current < 0) scenes[0].classList.add("on");
    };
    var queued = false;
    window.addEventListener("scroll", function () {
      if (queued) return;
      queued = true;
      requestAnimationFrame(function () { queued = false; pick(); });
    }, { passive: true });

    // On a narrow screen, each scene plays as it comes into view.
    var inline = watch && new IntersectionObserver(function (entries) {
      if (wide.matches) return;
      entries.forEach(function (e) {
        if (e.isIntersecting && !e.target._shown) {
          e.target._shown = true;
          play(e.target);
        } else if (!e.isIntersecting) {
          e.target._shown = false;
        }
      });
    }, { threshold: 0.45 });
    scenes.forEach(function (s) { if (inline) inline.observe(s); });

    var mode = function () {
      current = -1;
      chapters.concat(scenes).forEach(function (el) { el.classList.remove("on"); });
      scenes.forEach(function (s) { s._shown = false; });
      pick();
    };
    if (wide.addEventListener) wide.addEventListener("change", mode);
    pick();
  }

  // Where the changes come from: every square in one row, then sorted into
  // the branch's edits, what follows from them and the rest.
  var origins = document.querySelector("[data-origins]");
  if (origins && !reduce.matches && watch && origins.animate) {
    origins.classList.add("mixed");
    seen(origins, function () {
      setTimeout(function () {
        var units = slice(origins.querySelectorAll(".unit"));
        var first = units.map(function (u) { return u.getBoundingClientRect(); });
        origins.classList.remove("mixed");
        origins.classList.add("sorted");
        units.forEach(function (u, i) {
          var last = u.getBoundingClientRect();
          var dx = first[i].left - last.left, dy = first[i].top - last.top;
          if (!dx && !dy) return;
          u.animate([{ transform: "translate(" + dx + "px, " + dy + "px)" }, { transform: "none" }],
            { duration: 900, delay: +u.style.getPropertyValue("--o") * 14, easing: "cubic-bezier(0.32, 0.72, 0, 1)", fill: "backwards" });
        });
        count(origins);
      }, 450);
    }, 0.5);
  }

  // The pull request comment arrives, and the terminal prints the sample.
  var pr = document.querySelector(".pr");
  if (pr && !reduce.matches && watch) {
    pr.classList.add("armed");
    seen(pr, function () { pr.classList.add("shown"); }, 0.3);
  }
  var term = document.querySelector("[data-terminal]");
  if (term && !reduce.matches && watch) {
    term.classList.add("armed");
    seen(term, function () {
      term.classList.add("typing");
      setTimeout(function () { term.classList.add("printed"); }, 1100);
    }, 0.35);
  }

  // The report's views, one at a time, switched like the report's tabs.
  slice(document.querySelectorAll("[data-tabs]")).forEach(function (root) {
    var tabs = slice(root.querySelectorAll('[role="tab"]'));
    function show(tab, focus) {
      tabs.forEach(function (t) {
        var on = t === tab;
        var panel = document.getElementById(t.getAttribute("aria-controls"));
        t.setAttribute("aria-selected", on ? "true" : "false");
        t.tabIndex = on ? 0 : -1;
        if (!panel) return;
        panel.classList.toggle("on", on);
        if (on) panel.querySelectorAll('img[loading="lazy"]').forEach(function (img) { img.loading = "eager"; });
      });
      if (focus) tab.focus();
    }
    tabs.forEach(function (t, i) {
      t.addEventListener("click", function () { show(t, false); });
      t.addEventListener("keydown", function (e) {
        var j = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
        if (j === undefined) return;
        e.preventDefault();
        show(tabs[(j + tabs.length) % tabs.length], true);
      });
    });
    if (tabs.length) show(tabs[0], false);
  });

  // Copy buttons on code that is worth pasting.
  if (navigator.clipboard) {
    slice(document.querySelectorAll("[data-copy]")).forEach(function (pre) {
      var button = document.createElement("button");
      button.type = "button";
      button.className = "copy";
      button.setAttribute("aria-label", "Copy");
      button.innerHTML = COPY;
      var timer;
      button.addEventListener("click", function () {
        var text = pre.getAttribute("data-copy") || pre.textContent;
        status.textContent = "";
        navigator.clipboard.writeText(text.trim() + "\n").then(function () {
          button.classList.add("done");
          button.innerHTML = CHECK;
          button.setAttribute("aria-label", "Copied");
          clearTimeout(timer);
          timer = setTimeout(function () {
            button.classList.remove("done");
            button.innerHTML = COPY;
            button.setAttribute("aria-label", "Copy");
          }, 1600);
        }).catch(function () {
          status.textContent = "Copy failed. Select the command to copy it.";
        });
      });
      // The button sits outside the code, which scrolls sideways on a
      // narrow screen.
      var box = document.createElement("div");
      box.className = "codebox";
      pre.parentNode.insertBefore(box, pre);
      box.appendChild(pre);
      box.appendChild(button);
      var status = document.createElement("span");
      status.className = "copy-status";
      status.setAttribute("role", "status");
      box.appendChild(status);
    });
  }

  // The rules page's filter: by name, title or description.
  var filter = document.querySelector("[data-filter]");
  if (filter) {
    var rules = slice(document.querySelectorAll(".rule"));
    var groups = slice(document.querySelectorAll("[data-group]"));
    var none = document.querySelector(".no-match");
    var text = rules.map(function (r) { return r.textContent.toLowerCase().replace(/\s+/g, " "); });
    filter.hidden = false;
    filter.addEventListener("input", function () {
      var words = filter.value.toLowerCase().split(/\s+/).filter(Boolean);
      var shown = 0;
      rules.forEach(function (r, i) {
        var hit = words.every(function (w) { return text[i].indexOf(w) >= 0; });
        r.hidden = !hit;
        if (hit) shown++;
      });
      groups.forEach(function (g) {
        var n = g.querySelectorAll(".rule:not([hidden])").length;
        g.hidden = n === 0;
        var count = document.querySelector('[data-count="' + g.getAttribute("data-group") + '"]');
        if (count) count.textContent = n;
      });
      if (none) none.hidden = shown > 0;
    });
  }
})();

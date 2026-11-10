// Small progressive enhancements. Everything works without JavaScript;
// this just makes forms nicer. Uses event delegation, so it keeps working
// after htmx swaps content in.
(function () {
  "use strict";

  // Confirm before submitting forms marked with data-confirm.
  document.addEventListener("submit", function (e) {
    var form = e.target;
    var msg = form.getAttribute && form.getAttribute("data-confirm");
    if (msg && !window.confirm(msg)) {
      e.preventDefault();
      e.stopImmediatePropagation();
    }
  }, true);

  // Copy buttons: <button data-copy="#input-id">
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-copy]");
    if (!btn) return;
    var input = document.querySelector(btn.getAttribute("data-copy"));
    if (!input) return;
    var done = function () { toast(msg("copied", "Link copied")); };
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(input.value).then(done, function () { input.select(); });
    } else {
      input.select();
      try { document.execCommand("copy"); done(); } catch (err) { /* user can copy manually */ }
    }
  });

  // Open Library result -> fill the book form.
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-fill-book]");
    if (!btn) return;
    var form = document.getElementById("book-form");
    if (!form) return;
    var data;
    try { data = JSON.parse(btn.getAttribute("data-fill-book")); } catch (err) { return; }
    var set = function (name, value) {
      var el = form.elements[name];
      if (el) el.value = value == null || value === 0 ? "" : value;
    };
    set("title", data.title);
    set("authors", data.authors);
    set("year", data.year);
    set("cover_url", data.cover_url);
    set("goodreads_url", data.goodreads_url);
    set("openlibrary_key", data.openlibrary_key);
    updateCoverPreview(data.cover_url, data.title);
    document.querySelectorAll(".lookup-item.selected").forEach(function (el) { el.classList.remove("selected"); });
    btn.classList.add("selected");
    form.elements["title"].focus();
  });

  document.addEventListener("change", function (e) {
    if (e.target.matches("[data-cover-input]")) {
      var form = e.target.form;
      updateCoverPreview(e.target.value, form && form.elements["title"] ? form.elements["title"].value : "");
    }
  });

  function updateCoverPreview(url, title) {
    var box = document.getElementById("cover-preview");
    if (!box) return;
    box.textContent = "";
    if (url) {
      var img = document.createElement("img");
      img.className = "cover cover-m";
      img.alt = "";
      img.referrerPolicy = "no-referrer";
      img.src = url;
      box.appendChild(img);
    } else {
      var ph = document.createElement("div");
      ph.className = "cover cover-m cover-placeholder";
      ph.textContent = (title || "").split(/\s+/).filter(Boolean).slice(0, 2).map(function (w) { return w[0].toUpperCase(); }).join("");
      box.appendChild(ph);
    }
  }

  // Vote form: show only the settings for the selected method.
  function syncMethodPanels(root) {
    var picked = root.querySelector("[data-method-radio]:checked");
    root.querySelectorAll("[data-method-panel]").forEach(function (panel) {
      panel.hidden = !picked || panel.getAttribute("data-method-panel") !== picked.value;
    });
  }
  document.addEventListener("change", function (e) {
    if (e.target.matches("[data-method-radio]")) syncMethodPanels(e.target.form);
  });

  // Ballots: live helpers for "N votes", points and ranks.
  function syncBallot(form) {
    var kind = form.getAttribute("data-ballot");
    if (kind === "multi") {
      var max = parseInt(form.getAttribute("data-max-choices"), 10) || 0;
      if (!max) return;
      var boxes = Array.prototype.slice.call(form.querySelectorAll('input[name="choice"]'));
      var checked = boxes.filter(function (b) { return b.checked; }).length;
      boxes.forEach(function (b) {
        if (b.dataset.ownDisabled === undefined) b.dataset.ownDisabled = b.disabled ? "1" : "0";
        if (b.dataset.ownDisabled === "1") return;
        b.disabled = !b.checked && checked >= max;
      });
    } else if (kind === "points") {
      var total = parseInt(form.getAttribute("data-points-total"), 10) || 0;
      var used = 0;
      form.querySelectorAll("[data-points]").forEach(function (inp) {
        var v = parseInt(inp.value, 10);
        if (v > 0) used += v;
      });
      var out = form.querySelector("[data-points-left]");
      if (out) {
        var left = total - used;
        out.textContent = " " + (left >= 0 ? msg("pointsLeft", "%d left.").replace("%d", left) : msg("pointsOver", "%d too many!").replace("%d", -left));
        out.classList.toggle("over", left < 0);
      }
    }
  }
  document.addEventListener("input", function (e) {
    var form = e.target.closest("form[data-ballot]");
    if (form) syncBallot(form);
  });
  document.addEventListener("change", function (e) {
    var form = e.target.closest("form[data-ballot]");
    if (!form) return;
    // Ranked: picking a rank another option already has swaps them.
    if (e.target.matches("[data-rank]") && e.target.value) {
      var prev = e.target.dataset.prev || "";
      form.querySelectorAll("[data-rank]").forEach(function (sel) {
        if (sel !== e.target && sel.value === e.target.value) {
          sel.value = prev;
          sel.dataset.prev = prev;
        }
      });
    }
    if (e.target.matches("[data-rank]")) e.target.dataset.prev = e.target.value;
    syncBallot(form);
  });

  // Theme menu: switch instantly and remember the choice in a cookie (the
  // links themselves work without JavaScript via /theme/{name}).
  document.addEventListener("click", function (e) {
    var choice = e.target.closest("[data-theme-choice]");
    if (choice) {
      e.preventDefault();
      var name = choice.getAttribute("data-theme-choice");
      var root = document.documentElement;
      if (name === "auto") {
        root.removeAttribute("data-theme");
        document.cookie = "bookclub_theme=; path=/; max-age=0; samesite=lax";
      } else {
        root.setAttribute("data-theme", name);
        document.cookie = "bookclub_theme=" + name + "; path=/; max-age=" + 400 * 86400 + "; samesite=lax" +
          (location.protocol === "https:" ? "; secure" : "");
      }
      document.querySelectorAll("[data-theme-choice]").forEach(function (el) {
        el.setAttribute("aria-current", el.getAttribute("data-theme-choice") === name ? "true" : "false");
      });
      var menu = choice.closest("details");
      if (menu) menu.open = false;
      return;
    }
    // Close an open theme menu when clicking elsewhere.
    document.querySelectorAll("details.theme-menu[open]").forEach(function (menu) {
      if (!menu.contains(e.target)) menu.open = false;
    });
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    document.querySelectorAll("details.theme-menu[open]").forEach(function (menu) {
      menu.open = false;
      menu.querySelector("summary").focus();
    });
  });

  function init(root) {
    root.querySelectorAll("form.poll-form").forEach(syncMethodPanels);
    root.querySelectorAll("form[data-ballot]").forEach(function (form) {
      form.querySelectorAll("[data-rank]").forEach(function (sel) { sel.dataset.prev = sel.value; });
      syncBallot(form);
    });
  }
  document.addEventListener("DOMContentLoaded", function () { init(document); });
  document.addEventListener("htmx:load", function (e) { init(e.target); });

  // Show htmx request errors instead of failing silently.
  document.addEventListener("htmx:responseError", function (e) {
    var xhr = e.detail.xhr;
    toast(xhr && xhr.responseText && xhr.responseText.length < 300 ? xhr.responseText : msg("errorGeneric", "Something went wrong. Reload and try again."), true);
  });
  document.addEventListener("htmx:sendError", function () {
    toast(msg("errorOffline", "Couldn't reach the server. Check your connection."), true);
  });

  // Translated phrases come from data- attributes on <body>.
  function msg(key, fallback) {
    return (document.body && document.body.dataset[key]) || fallback;
  }

  var toastTimer;
  function toast(msg, isError) {
    var el = document.getElementById("toast");
    if (!el) return;
    el.textContent = msg;
    el.classList.toggle("toast-error", !!isError);
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { el.hidden = true; }, 3500);
  }
})();

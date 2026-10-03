// Admin shell for page: list, detail, and upload views over the page APIs,
// rendered by the vendored Alpine CSP build. All logic lives here — the CSP
// build evaluates the directive expressions in index.html against this
// registered component (harden-admin-ui D1), and untrusted values reach the
// DOM through x-text/:attr bindings, which escape by default.
const PAGE_SIZE = 50;
const TRANSIENT = new Set(["parking", "unparking", "deleting"]);
const RESERVED = new Set(["api", "a", "p", "ui", "www", "assets", "cdn", "static", "healthz"]);

function fmtBytes(n) {
  if (n < 1024) return n + " B";
  if (n < 1048576) return (n / 1024).toFixed(1) + " KB";
  return (n / 1048576).toFixed(1) + " MB";
}
function fmtTime(iso) { return new Date(iso).toLocaleString(); }

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
function fmtRel(iso) {
  const secs = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 45) return "just now";
  const steps = [[31536000, "year"], [2592000, "month"], [604800, "week"],
                 [86400, "day"], [3600, "hour"], [60, "minute"]];
  for (const [limit, unit] of steps) {
    if (secs >= limit) return rtf.format(-Math.round(secs / limit), unit);
  }
  return rtf.format(-1, "minute");
}

// The identifier preview mirrors slug.Sanitize and the reserved set so it
// shows the real final URL shape; it is never authoritative — the server
// re-validates (one-surface-upload-ui D4).
function sanitizeIdent(s) {
  s = s.trim().toLowerCase().replace(/[^a-z0-9-]+/g, "-")
       .replace(/-{2,}/g, "-").replace(/^-+|-+$/g, "");
  return s.length > 64 ? s.slice(0, 64).replace(/-+$/, "") : s;
}

document.addEventListener("alpine:init", () => {
  Alpine.data("page", () => ({
    // --- auth (token entry collapsed to a header chip; the input row only
    // opens on demand or when auth actually fails, auth-modes D8 /
    // polish-admin-ui D5) ---
    authMode: document.body.dataset.authMode || "token",
    authOpen: false,
    authMsg: "",
    authRequired: false,
    tokenInput: "",
    tokenSet: false,

    // --- theme: what the toggle last chose; the server-injected data-theme
    // attribute is the pre-paint truth (harden-admin-ui D4) ---
    theme: "system",

    // --- routing ---
    route: "list", // list | detail | upload
    slug: "",
    flashMsg: "",

    // --- list state ---
    listLoaded: false,
    listLoading: false,
    listError: "",
    listPages: [],
    listTotal: 0,
    listOffset: 0,
    listStatus: "",

    // --- detail state ---
    detailLoading: false,
    detailError: "",
    detail: null,

    // --- upload state ---
    source: null, // null | {type:"file", f} | {type:"url", v}
    urlInput: "",
    ident: "",
    identPreviewURL: "",
    identReservedMsg: "",
    identError: false,
    dragOver: false,
    publishing: false,
    publishLabel: "Publish",
    result: null,

    init() {
      const t = document.documentElement.dataset.theme;
      this.theme = (t === "light" || t === "dark") ? t : "system";
      if (this.authMode === "token") {
        this.tokenInput = localStorage.getItem("sp-token") || "";
        this.tokenSet = !!this.tokenInput.trim();
        this.authOpen = !this.tokenSet;
      }
      window.addEventListener("hashchange", () => this.applyHash());
      this.applyHash();
    },

    // --- theme: light → dark → system, persisted in the sp-theme cookie the
    // server reads to inject data-theme before first paint (harden-admin-ui
    // D4); "system" clears the attribute so prefers-color-scheme applies ---
    get themeLabel() {
      return "Theme: " + this.theme;
    },
    cycleTheme() {
      const next = this.theme === "light" ? "dark" : this.theme === "dark" ? "system" : "light";
      this.theme = next;
      document.cookie = "sp-theme=" + next + "; path=/; max-age=31536000; samesite=lax";
      if (next === "system") document.documentElement.removeAttribute("data-theme");
      else document.documentElement.dataset.theme = next;
    },

    // --- auth ---
    get showChip() { return this.authMode === "token"; },
    get showAuth() { return this.authMode === "token" && this.authOpen; },
    get chipLabel() { return this.tokenSet ? "Token set" : "No token"; },
    toggleAuth() { this.authOpen = !this.authOpen; },
    onTokenInput(e) {
      this.tokenInput = e.target.value;
      const v = this.tokenInput.trim();
      localStorage.setItem("sp-token", v);
      this.tokenSet = !!v;
      this.authMsg = "";
      // After a 401 sent us here, entering a token re-loads the current view
      // so the user isn't left staring at the empty page that prompted them.
      // authRequired stays set: the first successful call clears it and
      // collapses the entry; another 401 keeps it open with the message.
      if (this.authRequired && v) this.applyHash();
    },

    // api issues the request with the stored token (token mode) or the
    // session cookie (oidc mode), always carrying the CSRF header the oidc
    // mode's state-changing rules require. A 401 opens the token entry in
    // token mode and navigates to /login in oidc mode.
    async api(path, opts = {}) {
      const headers = Object.assign({ "X-Requested-With": "page-ui" }, opts.headers || {});
      const tok = localStorage.getItem("sp-token");
      if (tok) headers["Authorization"] = "Bearer " + tok;
      const res = await fetch(path, Object.assign({}, opts, { headers }));
      if (res.status === 401) {
        if (this.authMode === "oidc") {
          location.href = "/login";
          throw new Error("unauthorized");
        }
        this.authRequired = true;
        this.authOpen = true;
        this.authMsg = "A valid API token is required.";
        throw new Error("unauthorized");
      }
      if (this.authRequired) {
        this.authRequired = false;
        this.authOpen = false;
        this.authMsg = "";
      }
      return res;
    },

    async errText(res) {
      const body = await res.text();
      try { return JSON.parse(body).error || body || res.statusText; }
      catch { return body || res.statusText; }
    },

    // --- routing ---
    applyHash() {
      this.flashMsg = "";
      const h = location.hash || "#/";
      const m = h.match(/^#\/p\/([^/]+)$/);
      if (m) {
        this.slug = decodeURIComponent(m[1]);
        this.route = "detail";
        this.detail = null;
        this.detailError = "";
        this.loadDetail();
      } else if (h === "#/upload") {
        this.route = "upload";
        this.resetUpload();
      } else {
        this.route = "list";
        this.loadList();
      }
    },

    // --- list ---
    async loadList() {
      this.listLoading = true;
      this.listError = "";
      const q = new URLSearchParams({ limit: PAGE_SIZE });
      if (this.listOffset) q.set("offset", this.listOffset);
      if (this.listStatus) q.set("status", this.listStatus);
      try {
        const res = await this.api("/api/pages?" + q.toString());
        if (!res.ok) throw new Error(await this.errText(res));
        const data = await res.json();
        this.authMsg = "";
        this.listPages = data.pages.map((p) => this.mapListPage(p));
        this.listTotal = data.total;
        this.listLoaded = true;
        // The options are static in the markup; the select's value follows
        // the state, not the other way round.
        const sel = document.getElementById("statusfilter");
        if (sel) sel.value = this.listStatus;
      } catch (e) {
        if (e.message === "unauthorized") { this.listPages = []; return; }
        this.listError = "Could not load pages: " + e.message;
      } finally {
        this.listLoading = false;
      }
    },
    mapListPage(p) {
      const transient = TRANSIENT.has(p.status);
      return {
        slug: p.slug,
        identifier: p.identifier,
        identShown: p.identifier !== p.slug,
        listHref: "#/p/" + encodeURIComponent(p.slug),
        badgeClass: "badge " + (transient ? "transient" : p.status),
        badgeLabel: p.status + (transient ? "…" : ""),
        size: fmtBytes(p.total_bytes),
        assetCount: p.asset_count,
        rel: fmtRel(p.created_at),
        abs: fmtTime(p.created_at),
        url: p.url,
        canOpen: p.status === "live",
        canPark: p.status === "live",
        canUnpark: p.status === "parked",
        canDelete: !transient,
        busy: transient,
      };
    },
    get listFrom() { return this.listTotal === 0 ? 0 : this.listOffset + 1; },
    get listTo() { return Math.min(this.listOffset + PAGE_SIZE, this.listTotal); },
    get listRange() { return "Showing " + this.listFrom + "–" + this.listTo + " of " + this.listTotal; },
    get prevDisabled() { return this.listOffset === 0; },
    get nextDisabled() { return this.listTo >= this.listTotal; },
    get listEmptyFiltered() { return this.listStatus !== "" && this.listPages.length === 0; },
    get listEmptyAll() { return this.listStatus === "" && this.listPages.length === 0; },
    onStatusChange(e) {
      this.listStatus = e.target.value;
      this.listOffset = 0;
      this.loadList();
    },
    prevPage() { this.listOffset = Math.max(0, this.listOffset - PAGE_SIZE); this.loadList(); },
    nextPage() { this.listOffset += PAGE_SIZE; this.loadList(); },

    // --- lifecycle actions: park/unpark refresh the view from the API
    // response; delete confirms first, then lands back on the list — the
    // deleted page has no detail to stay on. Conflicts (409) surface their
    // message instead of failing mute. A page mid-transition shows no
    // actions (nothing can run while it moves). ---
    async pageAction(p, act) {
      this.flashMsg = "";
      p.busy = true;
      try {
        if (act === "delete") {
          if (!confirm(`Delete ${p.slug} permanently? Its pages, assets and metadata are removed.`)) {
            p.busy = false;
            return;
          }
          const res = await this.api("/api/pages/" + encodeURIComponent(p.slug), { method: "DELETE" });
          if (!res.ok) throw new Error(await this.errText(res));
          // Same-hash assignment doesn't fire hashchange: only navigate
          // when it actually changes, else re-render in place.
          if (location.hash === "#/" || location.hash === "") this.applyHash();
          else location.hash = "#/";
          return;
        }
        const res = await this.api(`/api/pages/${encodeURIComponent(p.slug)}/${act}`, { method: "POST" });
        if (!res.ok) throw new Error(await this.errText(res));
        if (this.route === "list") await this.loadList();
        else await this.loadDetail();
      } catch (e) {
        this.flashMsg = "Error: " + e.message;
        p.busy = false;
      }
    },

    // --- detail ---
    async loadDetail() {
      this.detailLoading = true;
      try {
        const res = await this.api("/api/pages/" + encodeURIComponent(this.slug));
        if (!res.ok) throw new Error(await this.errText(res));
        const data = await res.json();
        this.authMsg = "";
        this.detail = this.mapDetail(data);
      } catch (e) {
        if (e.message === "unauthorized") return;
        this.detailError = `Could not load ${this.slug}: ` + e.message;
      } finally {
        this.detailLoading = false;
      }
    },
    // kept-external is the accepted-compromise status: it must read as a
    // risk, not as a settled state (one-surface-upload-ui D3 lineage).
    // Unstored assets report 0 bytes — an em dash instead of a misleading
    // "0 B".
    mapDetail(d) {
      const transient = TRANSIENT.has(d.status);
      const externals = (d.assets || []).filter((a) => a.status === "kept-external").length;
      return {
        slug: d.slug,
        identifier: d.identifier,
        status: d.status,
        badgeClass: "badge " + (transient ? "transient" : d.status),
        badgeLabel: d.status + (transient ? "…" : ""),
        url: d.url,
        openLabel: "open " + d.url,
        parked: d.status === "parked",
        createdRel: fmtRel(d.created_at),
        createdAbs: fmtTime(d.created_at),
        sizeLine: fmtBytes(d.total_bytes) + " across " + d.asset_count + " assets",
        canOpen: d.status === "live",
        canPark: d.status === "live",
        canUnpark: d.status === "parked",
        canDelete: !transient,
        // busy must exist: Alpine renders a missing member in :disabled as
        // an empty string, which sets the attribute (empty string is truthy
        // for boolean attrs) — undefined here would disable the buttons.
        busy: transient,
        isTransient: transient,
        assets: (d.assets || []).map((a) => ({
          path: a.path,
          badgeClass: "badge " + (TRANSIENT.has(a.status) ? "transient" : a.status),
          badgeLabel: a.status + (TRANSIENT.has(a.status) ? "…" : ""),
          contentType: a.content_type,
          size: a.bytes > 0 ? fmtBytes(a.bytes) : "—",
          sourceURL: a.source_url,
        })),
        note: externals
          ? `${externals} asset${externals > 1 ? "s" : ""} still load${externals > 1 ? "" : "s"} from the original site and may break or leak requests.`
          : "",
      };
    },

    // --- upload: one source card (one-surface-upload-ui D1), drop layer
    // spanning the card and a URL footer, last-action-wins selection (D2) —
    // the card always shows the single source Publish sends. ---
    resetUpload() {
      this.source = null;
      this.urlInput = "";
      this.ident = "";
      this.identPreviewURL = "";
      this.identReservedMsg = "";
      this.identError = false;
      this.dragOver = false;
      this.publishing = false;
      this.publishLabel = "Publish";
      this.result = null;
    },
    get hasSource() { return !!this.source; },
    get srcName() {
      if (!this.source) return "";
      return this.source.type === "file" ? this.source.f.name : this.source.v;
    },
    get srcSize() {
      return this.source && this.source.type === "file"
        ? fmtBytes(this.source.f.size) : "";
    },
    get dropClass() { return { over: this.dragOver }; },
    get footerClass() { return { dimmed: !this.source }; },
    // Client-side extension check is a hint, not the validator — the server
    // decides. A rejected file never reaches a request (D7).
    validName(f) { return /\.(html|htm|zip)$/i.test(f.name); },
    setFile(f) {
      if (!f) return;
      if (!this.validName(f)) {
        this.result = { ok: false, message: `Unsupported file type: ${f.name}. Use .html, .htm or .zip.` };
        return;
      }
      this.result = null;
      this.source = { type: "file", f };
      this.urlInput = ""; // last action wins (one-surface-upload-ui D2)
    },
    onFilePicked(e) {
      this.setFile(e.target.files[0]);
      e.target.value = "";
    },
    onDrop(e) {
      this.dragOver = false;
      const f = e.dataTransfer.files[0];
      if (f) this.setFile(f);
    },
    onURLInput(e) {
      const v = e.target.value.trim();
      if (v) this.source = { type: "url", v };
      else if (this.source && this.source.type === "url") this.source = null;
      else return;
      this.result = null;
    },
    clearSource() {
      this.source = null;
      this.urlInput = "";
    },
    onIdentInput(e) {
      this.ident = e.target.value;
      this.identError = false;
      const s = sanitizeIdent(this.ident);
      this.identPreviewURL = "";
      this.identReservedMsg = "";
      if (!s) return;
      if (RESERVED.has(s)) {
        this.identReservedMsg = `"${s}" is reserved — pick another`;
        return;
      }
      // A first publish for an identifier always gets code 1; later codes
      // are server-side state the UI cannot know, and the result corrects
      // it (one-surface-upload-ui D5).
      this.identPreviewURL = "/p/" + s + "-1";
    },
    async publish() {
      // Read the field, not the mirrored state: state mirrors the input for
      // the live preview, but the gate must see exactly what is in the field
      // at submit time (autofill and programmatic clears can skip input
      // events).
      const s = sanitizeIdent(document.getElementById("identifier").value);
      if (!s) {
        this.identError = true;
        document.getElementById("identifier").focus();
        return;
      }
      if (RESERVED.has(s)) {
        // the preview line already carries the reserved warning; block + focus
        document.getElementById("identifier").focus();
        return;
      }
      if (!this.source) {
        this.result = { ok: false, message: "Choose a file or enter a URL first." };
        return;
      }
      const fd = new FormData();
      if (this.source.type === "url") fd.append("url", this.source.v);
      else fd.append("file", this.source.f);
      fd.append("identifier", s);
      this.result = null;
      this.publishing = true;
      this.publishLabel = this.source.type === "url" ? "Importing…" : "Uploading…";
      try {
        const res = await this.api("/api/pages", { method: "POST", body: fd });
        // Errors may be JSON (validation, strict gate) or plain text (fetch
        // failures via http.Error); parse what we can either way.
        const raw = await res.text();
        let body = {};
        try { body = JSON.parse(raw); } catch { body = { message: raw.trim() }; }
        if (res.status === 201) {
          // The 201 response carries the ingest counts (upload.go); surface
          // them here so kept-external doesn't hide until someone opens the
          // detail view (one-surface-upload-ui D8).
          const a = body.assets || {};
          const parts = [];
          if (a.baked) parts.push(a.baked + " baked");
          if (a.local) parts.push(a.local + " local");
          if (a.kept_cdn) parts.push(a.kept_cdn + " kept on CDN");
          if (a.kept_external) parts.push(a.kept_external + " kept external");
          this.result = {
            ok: true,
            slug: body.slug,
            url: body.url,
            detailHref: "#/p/" + encodeURIComponent(body.slug),
            summary: parts.join(", "),
            summaryWarn: !!a.kept_external,
          };
        } else if (body.error === "import_incomplete") {
          // The strict gate: show which assets failed and the way out
          // (import-by-url D5).
          this.result = {
            ok: false,
            incomplete: true,
            message: body.message || "Import incomplete.",
            unresolved: (body.unresolved || []).map((u) => ({
              url: u.url,
              reason: u.reason || "unfetchable",
            })),
          };
        } else {
          this.result = {
            ok: false,
            message: "Error " + res.status + ": " +
              (body.message || body.error || res.statusText || "upload failed"),
          };
        }
      } catch (e) {
        if (e.message === "unauthorized") {
          this.result = {
            ok: false,
            message: this.authMode === "oidc"
              ? "Session expired — redirecting to login."
              : "Enter a valid API token first.",
          };
        } else {
          this.result = { ok: false, message: "Upload failed: " + e };
        }
      } finally {
        this.publishing = false;
        this.publishLabel = "Publish";
      }
    },
  }));
});

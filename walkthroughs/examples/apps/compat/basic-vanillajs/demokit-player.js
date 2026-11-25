// demokit-player — vanilla-JS web component for playing back demokit
// traces in any HTML host. Registers <demokit-demo> as a Custom
// Element. No framework dependencies.
//
// Data sources, in priority order:
//   1. Programmatic: el.trace = traceObject (assigning the property
//      triggers a re-render).
//   2. data-src URL: fetch JSON; render as static playback.
//   3. data-src URL with data-mode="live": connect via WebSocket and
//      render incrementally as the server emits structured events
//      (header / section / step-start / chunk / step-end /
//      input-needed / done). Driven by `demokit --serve`.
//   4. Inline blob: <demokit-demo>{...JSON...}</demokit-demo>.
//
// Public API on the element:
//   .trace = obj            // programmatic data injection
//   .play(), .pause(), .reset(), .step(), .goTo(n)
//   .currentStep (read), .totalEntries (read), .isPlaying (read)
//
// Events dispatched on the element (notifications, past tense):
//   demokit:loaded   — once trace data is available
//   demokit:stepped  — the visible step just advanced
//   demokit:done     — last entry shown
//   demokit:error    — fatal load/render error
//
// Events listened for (commands; host fires these to drive the
// widget without knowing the player's class name):
//   demokit:play, demokit:pause, demokit:reset, demokit:step
//
// The command/notification names are deliberately distinct
// (demokit:step vs demokit:stepped) — using one name for both
// would create a recursion loop the moment a step() advance
// dispatches the same event the host listener handles.
//
// Keyboard (when the element is focused):
//   Space / ArrowRight  — next step
//   ArrowLeft           — previous step (rewinds the visible feed)
//   P                   — play/pause toggle
//   R                   — reset to first entry

// This file is an ES module. Bundle templates load it via
// <script type="module" src="...">.
//
// ansi_up is pulled in via a dynamic import wrapped in try/catch so
// the player still loads when the CDN is unreachable — opening the
// bundle on file:// without internet, behind a strict CSP that
// blocks third-party scripts, or in air-gapped contexts. The
// fallback strips ANSI escapes to plain text rather than failing
// the whole module.

// ansi_up is loaded from a commit-pinned jsdelivr URL — content-
// addressable since the commit SHA is immutable, so no separate
// integrity check is needed. We don't vendor a copy under our repo
// because the file is already at the CDN; duplicating the bytes
// adds nothing.
//
// file:// pages can't fetch cross-origin scripts (Chrome blocks
// null-origin → https). For those cases the answer is the demokit
// serve command (HTTP origin → CDN imports work) rather than a
// chained fallback chasing every constraint.
//
// The stripper fallback below catches the residual case where the
// import genuinely fails (no network, hard CSP, or a future demokit
// air-gapped mode). Output stays readable (just without colour)
// rather than garbled or absent.

let AnsiUp;
try {
  const mod = await import(
    'https://cdn.jsdelivr.net/gh/drudru/ansi_up@07a4824757d4dfbb41236a4245a6ce37f21aeb91/ansi_up.js'
  );
  AnsiUp = mod.AnsiUp;
} catch (err) {
  console.warn(
    'demokit-player: ansi_up failed to load — captured ANSI escapes will render as plain text:',
    err && err.message ? err.message : err,
  );
  AnsiUp = class {
    constructor() { this.use_classes = false; }
    ansi_to_html(text) {
      // Strip ANSI escapes AND HTML-escape `<`, `>`, `&` so DOMParser
      // doesn't misinterpret literals (e.g. the dragon ASCII body)
      // as malformed tags. ansi_up's real ansi_to_html escapes
      // internally; the stripper has to do the same.
      return String(text)
        .replace(/\x1b\[[0-9;]*m/g, '')
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;');
    }
  };
}

const PLAY_INTERVAL_MS = 1500; // tune via Demo author later if needed

class DemokitDemoElement extends HTMLElement {
    constructor() {
      super();
      this._trace = null;
      this._programmaticTrace = null;
      this._currentStep = 0; // index of next entry to reveal
      this._isPlaying = false;
      this._timer = null;
      this._kbHandler = null;
      this._evtHandlers = {};
    }

    connectedCallback() {
      this.classList.add('demokit-player');
      if (!this.hasAttribute('tabindex')) {
        this.setAttribute('tabindex', '0'); // make focusable for keyboard
      }
      // Snapshot inline JSON before _renderShell() replaces the
      // element's children — otherwise _loadTrace would read the
      // rendered button labels as if they were the trace blob.
      this._inlineText = (this.textContent || '').trim();
      this._renderShell();
      this._bindKeyboard();
      this._bindHostEvents();
      this._loadTrace();
    }

    disconnectedCallback() {
      this._stopPlayback();
      this._unbindKeyboard();
      this._unbindHostEvents();
    }

    // --- Programmatic data source ---

    set trace(obj) {
      this._programmaticTrace = obj;
      this._loadTrace();
    }
    get trace() {
      return this._trace;
    }

    // --- Public controls ---

    play() {
      if (this._isPlaying || !this._trace) return;
      this._isPlaying = true;
      this._updateControls();
      this._timer = setInterval(() => {
        this.step();
        if (this._currentStep >= this._entries().length) {
          this._stopPlayback();
        }
      }, PLAY_INTERVAL_MS);
    }

    pause() {
      this._stopPlayback();
    }

    reset() {
      this._stopPlayback();
      this._currentStep = 0;
      this._feedEl.replaceChildren();
      this._updateControls();
    }

    step() {
      const entries = this._entries();
      if (this._currentStep >= entries.length) return;
      const e = entries[this._currentStep];
      this._renderEntry(e, this._currentStep + 1);
      this._currentStep++;
      this._updateControls();
      this.dispatchEvent(new CustomEvent('demokit:stepped', {
        detail: { index: this._currentStep, entry: e },
      }));
      if (this._currentStep >= entries.length) {
        this.dispatchEvent(new CustomEvent('demokit:done'));
      }
    }

    goTo(n) {
      const entries = this._entries();
      this._stopPlayback();
      this._feedEl.replaceChildren();
      this._currentStep = 0;
      const target = Math.max(0, Math.min(n, entries.length));
      for (let i = 0; i < target; i++) {
        this.step();
      }
    }

    get currentStep() { return this._currentStep; }
    get totalEntries() { return this._entries().length; }
    get isPlaying() { return this._isPlaying; }

    // --- Internals ---

    _entries() {
      if (!this._trace) return [];
      // The trace JSON shipped by --doc json wraps trace entries
      // under a "trace" key alongside the demo definition. Inline
      // blobs may carry just the entries array directly.
      if (Array.isArray(this._trace)) return this._trace;
      return this._trace.trace || [];
    }

    _demo() {
      if (!this._trace || Array.isArray(this._trace)) return null;
      return this._trace.demo || null;
    }

    _renderShell() {
      this.replaceChildren();
      const header = document.createElement('div');
      header.className = 'demokit-player__header';
      this._titleEl = document.createElement('h2');
      this._titleEl.className = 'demokit-player__title';
      this._descEl = document.createElement('p');
      this._descEl.className = 'demokit-player__description';
      header.appendChild(this._titleEl);
      header.appendChild(this._descEl);

      this._feedEl = document.createElement('div');
      this._feedEl.className = 'demokit-player__feed';

      const controls = document.createElement('div');
      controls.className = 'demokit-player__controls';
      this._prevBtn = this._mkBtn('◀', 'Previous (←)', () => this.goTo(this._currentStep - 1));
      this._stepBtn = this._mkBtn('▶ Next', 'Next step (Space)', () => this.step());
      this._playBtn = this._mkBtn('▶▶ Play', 'Play (P)', () => this.play());
      this._pauseBtn = this._mkBtn('❚❚ Pause', 'Pause (P)', () => this.pause());
      this._resetBtn = this._mkBtn('⟲ Reset', 'Reset (R)', () => this.reset());
      this._counterEl = document.createElement('span');
      this._counterEl.className = 'demokit-player__counter';
      controls.append(this._prevBtn, this._stepBtn, this._playBtn, this._pauseBtn, this._resetBtn, this._counterEl);

      // Controls position is configurable via data-controls
      // ("top" default, "bottom" for legacy/footer-style layouts).
      // Top is the default because the feed grows downward; with
      // controls at the bottom, the user has to scroll past their
      // own steps to reach Next/Play.
      this.appendChild(header);
      const controlsAtBottom = (this.dataset.controls || 'top') === 'bottom';
      if (controlsAtBottom) {
        this.appendChild(this._feedEl);
        this.appendChild(controls);
        controls.classList.add('demokit-player__controls--bottom');
      } else {
        this.appendChild(controls);
        this.appendChild(this._feedEl);
        controls.classList.add('demokit-player__controls--top');
      }
    }

    _mkBtn(label, title, onClick) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'demokit-player__btn';
      b.textContent = label;
      b.title = title;
      b.addEventListener('click', (e) => { e.stopPropagation(); onClick(); });
      return b;
    }

    _updateControls() {
      const total = this.totalEntries;
      this._counterEl.textContent = `${this._currentStep} / ${total}`;
      this._prevBtn.disabled = this._currentStep <= 0;
      this._stepBtn.disabled = this._currentStep >= total;
      this._playBtn.style.display = this._isPlaying ? 'none' : '';
      this._pauseBtn.style.display = this._isPlaying ? '' : 'none';
    }

    async _loadTrace() {
      const mode = this.dataset.mode || 'static';
      const src = this.dataset.src;

      try {
        if (this._programmaticTrace) {
          this._trace = this._programmaticTrace;
        } else if (src && mode === 'live') {
          // Live mode: open a WebSocket and stream structured events.
          // Returns immediately; events drive the render incrementally.
          this._connectWS(src);
          return;
        } else if (src) {
          const res = await fetch(src);
          if (!res.ok) {
            throw new Error(`demokit-player: fetch ${src} → HTTP ${res.status}`);
          }
          this._trace = await res.json();
        } else {
          // _inlineText was captured in connectedCallback before the
          // shell render replaced our children. Falls back to current
          // textContent if connectedCallback hasn't run yet (rare).
          const text = (this._inlineText !== undefined
            ? this._inlineText
            : (this.textContent || '').trim());
          if (!text) {
            this._renderError(
              'demokit-player: no trace data. Provide data-src, inline JSON, or set the .trace property.',
            );
            return;
          }
          this._trace = JSON.parse(text);
        }

        this._renderHeader();
        this._currentStep = 0;
        this._feedEl.replaceChildren();
        this._updateControls();
        this.dispatchEvent(new CustomEvent('demokit:loaded', {
          detail: { totalEntries: this.totalEntries },
        }));
      } catch (err) {
        this._renderError(err && err.message ? err.message : String(err));
        this.dispatchEvent(new CustomEvent('demokit:error', {
          detail: { message: err && err.message ? err.message : String(err) },
        }));
      }
    }

    _renderHeader() {
      const demo = this._demo();
      this._titleEl.textContent = demo && demo.title ? demo.title : '';
      this._descEl.textContent = demo && demo.description ? demo.description : '';
      this._descEl.style.display = this._descEl.textContent ? '' : 'none';
    }

    _renderError(message) {
      this._feedEl.replaceChildren();
      const box = document.createElement('div');
      box.className = 'demokit-player__error';
      box.textContent = message;
      this._feedEl.appendChild(box);
    }

    _renderEntry(entry, indexOneBased) {
      if (entry.kind === 'section') {
        this._renderSection(entry);
      } else {
        this._renderStep(entry, indexOneBased);
      }
      this._feedEl.lastElementChild.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }

    _renderSection(entry) {
      const sec = document.createElement('section');
      sec.className = 'demokit-section';
      const h = document.createElement('h3');
      h.className = 'demokit-section__title';
      h.textContent = entry.title || '';
      sec.appendChild(h);
      if (entry.body) {
        const p = document.createElement('p');
        p.className = 'demokit-section__body';
        p.textContent = entry.body;
        sec.appendChild(p);
      }
      this._feedEl.appendChild(sec);
    }

    _renderStep(entry, indexOneBased) {
      const art = document.createElement('article');
      const status = entry.status || 0; // 0 = success
      art.className = `demokit-step demokit-step--status-${status}`;
      const h = document.createElement('h3');
      h.className = 'demokit-step__title';
      h.textContent = `${indexOneBased}. ${entry.title || entry.step_id || ''}`;
      if (entry.visit && entry.visit > 1) {
        const v = document.createElement('span');
        v.className = 'demokit-step__visit';
        v.textContent = ` (visit ${entry.visit})`;
        h.appendChild(v);
      }
      art.appendChild(h);

      // Note from the demo definition (if available)
      const stepDef = this._lookupStepDef(entry.step_id);
      if (stepDef && stepDef.note) {
        const blk = document.createElement('blockquote');
        blk.className = 'demokit-step__note';
        blk.textContent = stepDef.note;
        art.appendChild(blk);
      }
      if (stepDef && stepDef.refs && stepDef.refs.length > 0) {
        const refs = document.createElement('p');
        refs.className = 'demokit-step__refs';
        refs.appendChild(document.createTextNode('References: '));
        stepDef.refs.forEach((ref, i) => {
          if (i > 0) refs.appendChild(document.createTextNode(', '));
          const a = document.createElement('a');
          a.href = ref.url;
          a.textContent = ref.name;
          a.target = '_blank';
          a.rel = 'noopener noreferrer';
          refs.appendChild(a);
        });
        art.appendChild(refs);
      }

      if (stepDef && Array.isArray(stepDef.verbatim) && stepDef.verbatim.length) {
        this._appendVerbatim(art, stepDef.verbatim);
      }

      // Inputs collected for this entry (read-only)
      if (entry.inputs && Object.keys(entry.inputs).length > 0) {
        const wrap = document.createElement('dl');
        wrap.className = 'demokit-step__inputs';
        Object.keys(entry.inputs).sort().forEach((k) => {
          const dt = document.createElement('dt');
          dt.textContent = k;
          const dd = document.createElement('dd');
          dd.textContent = String(entry.inputs[k]);
          wrap.appendChild(dt);
          wrap.appendChild(dd);
        });
        art.appendChild(wrap);
      }

      // Captured output — interpret ANSI SGR escapes so streamed
      // colored output (e.g. the dungeon's dragon scene) renders
      // with styling instead of leaking the literal "[38;5;245m"
      // sequences as text.
      if (entry.output) {
        const pre = document.createElement('pre');
        pre.className = 'demokit-step__output';
        appendANSI(pre, entry.output.replace(/\n+$/, ''));
        art.appendChild(pre);
      }

      // Status footer (error/warning/info messages)
      if (status !== 0 && (entry.message || entry.label)) {
        const foot = document.createElement('p');
        foot.className = 'demokit-step__status';
        const label = entry.label || statusLabel(status);
        foot.textContent = `${label}: ${entry.message || ''}`;
        art.appendChild(foot);
      }

      // Jump arrow
      if (entry.next) {
        const j = document.createElement('p');
        j.className = 'demokit-step__jump';
        j.textContent = `→ jumped to ${entry.next}`;
        art.appendChild(j);
      }

      this._feedEl.appendChild(art);
    }

    _lookupStepDef(stepId) {
      const demo = this._demo();
      if (!demo || !demo.items || !stepId) return null;
      for (const it of demo.items) {
        if (it.kind === 'step' && it.id === stepId) return it;
      }
      return null;
    }

    // Append each verbatim block as an optional label + a <pre><code>
    // with white-space: pre and overflow-x: auto. The browser does not
    // soft-wrap, so triple-clicking a long line yields the original
    // bytes — same copy-paste invariant the TUI guarantees.
    _appendVerbatim(parent, blocks) {
      blocks.forEach((b) => {
        if (b.label) {
          const h = document.createElement('p');
          h.className = 'demokit-step__verbatim-label';
          h.textContent = b.label;
          parent.appendChild(h);
        }
        const pre = document.createElement('pre');
        pre.className = 'demokit-step__verbatim';
        const code = document.createElement('code');
        if (b.lang) code.className = 'language-' + b.lang;
        code.textContent = b.content || '';
        pre.appendChild(code);
        parent.appendChild(pre);
      });
    }

    _stopPlayback() {
      if (this._timer) {
        clearInterval(this._timer);
        this._timer = null;
      }
      this._isPlaying = false;
      if (this._counterEl) this._updateControls();
    }

    _bindKeyboard() {
      this._kbHandler = (e) => {
        if (document.activeElement !== this) return;
        switch (e.key) {
          case ' ':
          case 'ArrowRight':
            e.preventDefault();
            this.step();
            break;
          case 'ArrowLeft':
            e.preventDefault();
            this.goTo(this._currentStep - 1);
            break;
          case 'p':
          case 'P':
            e.preventDefault();
            if (this._isPlaying) this.pause(); else this.play();
            break;
          case 'r':
          case 'R':
            e.preventDefault();
            this.reset();
            break;
        }
      };
      this.addEventListener('keydown', this._kbHandler);
    }

    _unbindKeyboard() {
      if (this._kbHandler) {
        this.removeEventListener('keydown', this._kbHandler);
        this._kbHandler = null;
      }
    }

    _bindHostEvents() {
      const evts = ['demokit:play', 'demokit:pause', 'demokit:reset', 'demokit:step'];
      evts.forEach((name) => {
        const handler = () => {
          switch (name) {
            case 'demokit:play':  this.play();  break;
            case 'demokit:pause': this.pause(); break;
            case 'demokit:reset': this.reset(); break;
            case 'demokit:step':  this.step();  break;
          }
        };
        this._evtHandlers[name] = handler;
        this.addEventListener(name, handler);
      });
    }

    _unbindHostEvents() {
      Object.keys(this._evtHandlers).forEach((name) => {
        this.removeEventListener(name, this._evtHandlers[name]);
      });
      this._evtHandlers = {};
    }

    // --- Live-mode WS ---
    //
    // When data-mode="live", the player connects to data-src as a
    // WebSocket and renders incoming structured events incrementally
    // (instead of fetching a static trace once). Outgoing messages
    // are user actions: input submissions, reset.

    _connectWS(rawURL) {
      const wsURL = this._toWSURL(rawURL);
      let ws;
      try {
        ws = new WebSocket(wsURL);
      } catch (err) {
        this._renderError('demokit-player: WS connect failed: ' + (err && err.message ? err.message : err));
        return;
      }
      this._ws = ws;
      this._currentStepEl = null;
      this._currentStepID = null;

      ws.onmessage = (e) => {
        let evt;
        try { evt = JSON.parse(e.data); } catch (_) { return; }
        this._handleServerEvent(evt);
      };
      ws.onerror = () => {
        this._renderError('demokit-player: WS error connecting to ' + wsURL);
      };
      ws.onclose = () => {
        const note = document.createElement('p');
        note.className = 'demokit-player__disconnected';
        note.textContent = '(disconnected)';
        this._feedEl.appendChild(note);
      };
    }

    // _toWSURL converts an http(s):// or relative URL into ws(s)://
    // so the host can write data-src as either a path or full URL.
    _toWSURL(url) {
      if (url.startsWith('ws://') || url.startsWith('wss://')) return url;
      if (url.startsWith('http://')) return 'ws://' + url.slice(7);
      if (url.startsWith('https://')) return 'wss://' + url.slice(8);
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const path = url.startsWith('/') ? url : '/' + url;
      return proto + '//' + location.host + path;
    }

    _handleServerEvent(evt) {
      switch (evt.kind) {
        case 'header':
          if (evt.demo) {
            this._titleEl.textContent = evt.demo.title || '';
            this._descEl.textContent = evt.demo.description || '';
            this._descEl.style.display = this._descEl.textContent ? '' : 'none';
          }
          break;
        case 'section':
          this._renderLiveSection(evt.extra || {});
          break;
        case 'step-start':
          this._openLiveStep(evt.extra || {});
          break;
        case 'chunk':
          this._appendChunkToLiveStep(evt.chunk || '');
          break;
        case 'step-end':
          this._closeLiveStep(evt.status || 0, evt.extra || {});
          break;
        case 'input-needed':
          this._renderLiveInputForm(evt.step_id, evt.inputs || []);
          break;
        case 'input-timeout':
          this._dismissLiveInputForm(evt.extra && evt.extra.timeout_ms);
          break;
        case 'done':
          this._renderLiveDone();
          break;
        case 'reset':
          this._feedEl.replaceChildren();
          this._currentStepEl = null;
          this._currentStepID = null;
          break;
      }
    }

    _renderLiveSection(extra) {
      const sec = document.createElement('section');
      sec.className = 'demokit-section';
      const h = document.createElement('h3');
      h.className = 'demokit-section__title';
      h.textContent = extra.title || '';
      sec.appendChild(h);
      if (extra.body) {
        const p = document.createElement('p');
        p.className = 'demokit-section__body';
        p.textContent = extra.body;
        sec.appendChild(p);
      }
      this._feedEl.appendChild(sec);
      sec.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }

    _openLiveStep(extra) {
      const art = document.createElement('article');
      art.className = 'demokit-step demokit-step--status-0';
      const h = document.createElement('h3');
      h.className = 'demokit-step__title';
      const num = extra.step_num;
      const title = extra.title || extra.id || '';
      h.textContent = num ? num + '. ' + title : title;
      art.appendChild(h);

      if (extra.note) {
        const blk = document.createElement('blockquote');
        blk.className = 'demokit-step__note';
        blk.textContent = extra.note;
        art.appendChild(blk);
      }
      if (Array.isArray(extra.refs) && extra.refs.length) {
        const refs = document.createElement('p');
        refs.className = 'demokit-step__refs';
        refs.appendChild(document.createTextNode('References: '));
        extra.refs.forEach((ref, i) => {
          if (i > 0) refs.appendChild(document.createTextNode(', '));
          const a = document.createElement('a');
          a.href = ref.url || ref.URL || '#';
          a.textContent = ref.name || ref.Name || ref.url || '';
          a.target = '_blank';
          a.rel = 'noopener noreferrer';
          refs.appendChild(a);
        });
        art.appendChild(refs);
      }

      if (Array.isArray(extra.verbatim) && extra.verbatim.length) {
        this._appendVerbatim(art, extra.verbatim);
      }

      const pre = document.createElement('pre');
      pre.className = 'demokit-step__output';
      pre.style.display = 'none'; // shown when first chunk arrives
      art.appendChild(pre);

      this._feedEl.appendChild(art);
      this._currentStepEl = art;
      this._currentStepPreEl = pre;
      this._currentStepID = extra.id || '';
      art.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }

    _appendChunkToLiveStep(text) {
      if (!this._currentStepPreEl) return;
      this._currentStepPreEl.style.display = '';
      appendANSI(this._currentStepPreEl, text);
    }

    _closeLiveStep(status, extra) {
      const el = this._currentStepEl;
      if (!el) return;
      el.classList.remove('demokit-step--status-0');
      el.classList.add('demokit-step--status-' + status);

      if (status !== 0 && extra && extra.message) {
        const foot = document.createElement('p');
        foot.className = 'demokit-step__status';
        foot.textContent = (extra.label || statusLabel(status)) + ': ' + extra.message;
        el.appendChild(foot);
      }
      if (extra && extra.next) {
        const j = document.createElement('p');
        j.className = 'demokit-step__jump';
        j.textContent = '→ jumped to ' + extra.next;
        el.appendChild(j);
      }
      this._currentStepEl = null;
      this._currentStepPreEl = null;
      this._currentStepID = null;
    }

    _renderLiveInputForm(stepID, inputs) {
      const form = document.createElement('form');
      form.className = 'demokit-input-form';
      const fields = [];
      inputs.forEach((inp) => {
        const wrap = document.createElement('label');
        wrap.className = 'demokit-input-form__field';
        const lbl = document.createElement('span');
        lbl.textContent = (inp.Prompt || inp.Name || inp.name || 'input') + ': ';
        wrap.appendChild(lbl);

        let ctrl;
        const kind = inp.Kind || inp.kind;
        if (kind === 'choice') {
          ctrl = document.createElement('select');
          (inp.Options || inp.options || []).forEach((opt) => {
            const o = document.createElement('option');
            o.value = opt;
            o.textContent = opt;
            ctrl.appendChild(o);
          });
        } else if (kind === 'int') {
          ctrl = document.createElement('input');
          ctrl.type = 'number';
        } else {
          ctrl = document.createElement('input');
          ctrl.type = 'text';
        }
        ctrl.name = inp.Name || inp.name || '';
        if (inp.Default !== undefined && inp.Default !== null) {
          ctrl.value = String(inp.Default);
        } else if (inp.default !== undefined && inp.default !== null) {
          ctrl.value = String(inp.default);
        }
        wrap.appendChild(ctrl);
        form.appendChild(wrap);
        fields.push({ name: ctrl.name, el: ctrl, kind });
      });

      const submit = document.createElement('button');
      submit.type = 'submit';
      submit.className = 'demokit-player__btn';
      submit.textContent = 'Submit';
      form.appendChild(submit);

      form.addEventListener('submit', (e) => {
        e.preventDefault();
        const values = {};
        fields.forEach((f) => {
          if (f.kind === 'int') {
            values[f.name] = parseInt(f.el.value, 10);
          } else {
            values[f.name] = f.el.value;
          }
        });
        if (this._ws && this._ws.readyState === WebSocket.OPEN) {
          this._ws.send(JSON.stringify({ kind: 'input', values }));
        }
        form.remove();
      });

      this._feedEl.appendChild(form);
      form.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }

    _dismissLiveInputForm(timeoutMs) {
      // Server timed out waiting for input and is continuing with
      // declared defaults. Find the most recent input form (the one
      // we just rendered for the current step) and replace it with
      // a small notice.
      const forms = this._feedEl.querySelectorAll('form.demokit-input-form');
      const form = forms[forms.length - 1];
      if (!form) return;
      const note = document.createElement('p');
      note.className = 'demokit-input-form__timeout';
      const secs = timeoutMs ? (timeoutMs / 1000).toFixed(1) : '';
      note.textContent = secs
        ? `(no input after ${secs}s — continuing with defaults)`
        : '(input timed out — continuing with defaults)';
      form.replaceWith(note);
      note.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }

    _renderLiveDone() {
      const note = document.createElement('p');
      note.className = 'demokit-player__done';
      note.textContent = '(demo ended)';
      this._feedEl.appendChild(note);
    }
  }

  // --- ANSI SGR → DOM ---
  //
  // Delegates to ansi_up (https://github.com/drudru/ansi_up, MIT;
  // vendored under web/player/vendor/ — see VENDOR.md for the pin).
  // The bundle and dev harness load ansi_up.umd.js *before* this
  // file, so window.AnsiUp is available when appendANSI runs.
  // Covers the full SGR table: 8-color, 256-color, true-color RGB,
  // bold/dim/italic/underline/strikethrough.
  //
  // ansi_to_html escapes input itself and emits only text plus
  // <span style=...> wrappers; running its output through DOMParser
  // and reparenting the resulting nodes is safe and avoids touching
  // innerHTML on the live document.
  //
  // Fallback: if AnsiUp isn't loaded (player JS used standalone
  // without the vendor bundle), output renders as plain text — the
  // escape sequences appear as literals but the step still loads.

  function appendANSI(parent, text) {
    if (text == null) return;
    const ansiUp = new AnsiUp();
    ansiUp.use_classes = false;
    const safeHTML = ansiUp.ansi_to_html(String(text));
    const parsed = new DOMParser().parseFromString(safeHTML, 'text/html');
    while (parsed.body.firstChild) {
      parent.appendChild(parsed.body.firstChild);
    }
  }

  function statusLabel(status) {
    switch (status) {
      case 1: return 'Error';
      case 2: return 'Warning';
      case 3: return 'Info';
      default: return 'Result';
    }
  }

if (!customElements.get('demokit-demo')) {
  customElements.define('demokit-demo', DemokitDemoElement);
}

<!--
  /help — plain-language help.

  Goal: a 15-year-old should be able to read this and understand what
  every concept means. No jargon without a definition on the same line.
  Sections are friendly, with one analogy per concept.

  Layout: sticky table of contents on the inline-start side, scrolling
  content on the trailing side. On narrow screens the ToC collapses
  above the content. All copy lives in locale.ts so EN + AR ship in
  the same shape.
-->
<script lang="ts">
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import {
    Sparkles,
    Rocket,
    Plug,
    Workflow,
    Layers,
    AlertOctagon,
    KeyRound,
    Webhook as WebhookIcon,
    Keyboard,
    BookOpen,
    ExternalLink
  } from 'lucide-svelte';

  // ─── Section metadata ────────────────────────────────────────────
  // Each entry pairs an icon + the locale slug for title/body. Keeping
  // the slugs in one place means the ToC and the body always agree on
  // ordering and labels — adding a new section is a single row here.
  const sections: { id: string; icon: typeof Plug; key: string }[] = [
    { id: 'what', icon: Sparkles, key: 'help.what' },
    { id: 'quickstart', icon: Rocket, key: 'help.quickstart' },
    { id: 'connections', icon: Plug, key: 'help.connections' },
    { id: 'pipelines', icon: Workflow, key: 'help.pipelines' },
    { id: 'stages', icon: Layers, key: 'help.stages' },
    { id: 'dlq', icon: AlertOctagon, key: 'help.dlq' },
    { id: 'tokens', icon: KeyRound, key: 'help.tokens' },
    { id: 'webhooks', icon: WebhookIcon, key: 'help.webhooks' },
    { id: 'shortcuts', icon: Keyboard, key: 'help.shortcuts' },
    { id: 'glossary', icon: BookOpen, key: 'help.glossary' }
  ];

  // Smooth-scroll a section into view + update the URL hash. The CSS
  // also has scroll-margin-block-start so the section heading isn't
  // hidden under the sticky topbar.
  function jumpTo(id: string) {
    const el = document.getElementById(id);
    if (!el) return;
    el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    history.replaceState(null, '', '#' + id);
  }
</script>

<div class="help-shell max-w-6xl">
  <PageHeader title={t($locale, 'help.title')} subtitle={t($locale, 'help.subtitle')} />

  <div class="help-grid">
    <!-- ─── Table of contents (sticky on wide screens) ─────────── -->
    <aside class="help-toc" aria-label={t($locale, 'help.toc')}>
      <p class="help-toc-eyebrow">{t($locale, 'help.toc')}</p>
      <ul class="help-toc-list">
        {#each sections as s (s.id)}
          <li>
            <a
              class="help-toc-link"
              href="#{s.id}"
              on:click|preventDefault={() => jumpTo(s.id)}
            >
              <span class="help-toc-icon" aria-hidden="true">
                <svelte:component this={s.icon} size={14} strokeWidth={1.75} />
              </span>
              <span>{t($locale, s.key + '.title')}</span>
            </a>
          </li>
        {/each}
      </ul>
    </aside>

    <!-- ─── Scrollable content column ───────────────────────────── -->
    <div class="help-content">
      <!-- 1. What does the app do? -->
      <Card>
        <section id="what" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <Sparkles size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.what.title')}</h2>
          </header>
          <p class="help-section-body">{t($locale, 'help.what.body')}</p>
          <p class="help-section-example">{t($locale, 'help.what.example')}</p>
        </section>
      </Card>

      <!-- 2. Quick start -->
      <Card>
        <section id="quickstart" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <Rocket size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.quickstart.title')}</h2>
          </header>
          <p class="help-section-body">{t($locale, 'help.quickstart.body')}</p>
          <ol class="help-steps">
            <li><span class="step-num">1</span>{t($locale, 'help.quickstart.s1')}</li>
            <li><span class="step-num">2</span>{t($locale, 'help.quickstart.s2')}</li>
            <li><span class="step-num">3</span>{t($locale, 'help.quickstart.s3')}</li>
            <li><span class="step-num">4</span>{t($locale, 'help.quickstart.s4')}</li>
            <li><span class="step-num">5</span>{t($locale, 'help.quickstart.s5')}</li>
          </ol>
        </section>
      </Card>

      <!-- 3. Connections -->
      <Card>
        <section id="connections" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <Plug size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.connections.title')}</h2>
          </header>
          <p class="help-section-body">{t($locale, 'help.connections.body')}</p>
          <p class="help-tip"><strong>Tip.</strong> {t($locale, 'help.connections.tip')}</p>
        </section>
      </Card>

      <!-- 4. Pipelines -->
      <Card>
        <section id="pipelines" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <Workflow size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.pipelines.title')}</h2>
          </header>
          <p class="help-section-body">{t($locale, 'help.pipelines.body')}</p>
          <p class="help-tip"><strong>Tip.</strong> {t($locale, 'help.pipelines.tip')}</p>
        </section>
      </Card>

      <!-- 5. Stages -->
      <Card>
        <section id="stages" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <Layers size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.stages.title')}</h2>
          </header>
          <p class="help-section-body">{t($locale, 'help.stages.body')}</p>
          <dl class="help-defs">
            <dt>Filter</dt>
            <dd>{t($locale, 'help.stage.filter')}</dd>
            <dt>Transform</dt>
            <dd>{t($locale, 'help.stage.transform')}</dd>
            <dt>Translate</dt>
            <dd>{t($locale, 'help.stage.translate')}</dd>
            <dt>Route</dt>
            <dd>{t($locale, 'help.stage.route')}</dd>
            <dt>Script</dt>
            <dd>{t($locale, 'help.stage.script')}</dd>
            <dt>Validate</dt>
            <dd>{t($locale, 'help.stage.validate')}</dd>
          </dl>
        </section>
      </Card>

      <!-- 6. DLQ -->
      <Card>
        <section id="dlq" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <AlertOctagon size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.dlq.title')}</h2>
          </header>
          <p class="help-section-body">{t($locale, 'help.dlq.body')}</p>
          <p class="help-tip"><strong>Tip.</strong> {t($locale, 'help.dlq.tip')}</p>
        </section>
      </Card>

      <!-- 7. Tokens -->
      <Card>
        <section id="tokens" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <KeyRound size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.tokens.title')}</h2>
          </header>
          <p class="help-section-body">{t($locale, 'help.tokens.body')}</p>
          <p class="help-tip"><strong>Tip.</strong> {t($locale, 'help.tokens.tip')}</p>
        </section>
      </Card>

      <!-- 8. Webhooks -->
      <Card>
        <section id="webhooks" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <WebhookIcon size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.webhooks.title')}</h2>
          </header>
          <p class="help-section-body">{t($locale, 'help.webhooks.body')}</p>
          <p class="help-tip"><strong>Tip.</strong> {t($locale, 'help.webhooks.tip')}</p>
        </section>
      </Card>

      <!-- 9. Keyboard shortcuts -->
      <Card>
        <section id="shortcuts" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <Keyboard size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.shortcuts.title')}</h2>
          </header>
          <p class="help-section-body">{t($locale, 'help.shortcuts.body')}</p>
        </section>
      </Card>

      <!-- 10. Glossary -->
      <Card>
        <section id="glossary" class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <BookOpen size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.glossary.title')}</h2>
          </header>
          <dl class="help-defs">
            <dt>Broker</dt>
            <dd>{t($locale, 'help.glossary.broker')}</dd>
            <dt>Queue / Topic</dt>
            <dd>{t($locale, 'help.glossary.queue')}</dd>
            <dt>Tenant</dt>
            <dd>{t($locale, 'help.glossary.tenant')}</dd>
            <dt>Role</dt>
            <dd>{t($locale, 'help.glossary.role')}</dd>
            <dt>Audit log</dt>
            <dd>{t($locale, 'help.glossary.audit')}</dd>
          </dl>
        </section>
      </Card>

      <!-- 11. More resources (links to repo docs) -->
      <Card>
        <section class="help-section">
          <header class="help-section-head">
            <span class="help-section-icon" aria-hidden="true">
              <ExternalLink size={18} strokeWidth={1.75} />
            </span>
            <h2 class="help-section-title">{t($locale, 'help.more.title')}</h2>
          </header>
          <ul class="help-links">
            <li>
              <a href="/api/openapi.yaml" target="_blank" rel="noopener">OpenAPI spec (raw YAML)</a>
            </li>
            <li>
              <a
                href="https://github.com/anthropics/mqconnector/tree/main/docs/runbooks"
                target="_blank"
                rel="noopener noreferrer"
              >{t($locale, 'help.more.runbooks')}</a>
            </li>
            <li>
              <a
                href="https://github.com/anthropics/mqconnector/blob/main/SECURITY.md"
                target="_blank"
                rel="noopener noreferrer"
              >{t($locale, 'help.more.security')}</a>
            </li>
            <li>
              <a
                href="https://github.com/anthropics/mqconnector/blob/main/COMPLIANCE.md"
                target="_blank"
                rel="noopener noreferrer"
              >{t($locale, 'help.more.compliance')}</a>
            </li>
          </ul>
        </section>
      </Card>
    </div>
  </div>
</div>

<style>
  .help-shell {
    display: flex;
    flex-direction: column;
    gap: 24px;
  }

  /* Two-column layout: sticky ToC on the inline-start side, scrollable
     content on the trailing side. On narrow screens the ToC collapses
     above the content as a horizontal chip strip. */
  .help-grid {
    display: grid;
    grid-template-columns: 220px minmax(0, 1fr);
    gap: 24px;
    align-items: start;
  }
  @media (max-width: 880px) {
    .help-grid {
      grid-template-columns: 1fr;
    }
  }

  /* ─── Table of contents ───────────────────────────────────────── */
  .help-toc {
    position: sticky;
    inset-block-start: 88px; /* below the sticky topbar */
    align-self: start;
    padding: 16px;
    background: var(--surface);
    border: 1px solid var(--card-border);
    border-radius: 16px;
  }
  .help-toc-eyebrow {
    color: var(--text-tertiary);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-block-end: 10px;
  }
  .help-toc-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .help-toc-link {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px;
    border-radius: 12px; /* §7 rule 10 — interactive */
    color: var(--text-muted);
    font-size: 13px;
    line-height: 1.3;
    text-decoration: none;
  }
  .help-toc-link:hover {
    background: var(--surface-2);
    color: var(--text);
  }
  .help-toc-icon {
    color: var(--text-tertiary);
    display: inline-flex;
    flex: 0 0 auto;
  }
  .help-toc-link:hover .help-toc-icon {
    color: var(--secondary);
  }
  :global([data-theme='light']) .help-toc-link:hover .help-toc-icon {
    color: var(--primary);
  }

  /* ─── Content column ──────────────────────────────────────────── */
  .help-content {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-inline-size: 0;
  }

  .help-section {
    /* Anchor jumps land below the sticky topbar — without this the
       heading would slip under it. */
    scroll-margin-block-start: 88px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .help-section-head {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .help-section-icon {
    inline-size: 32px;
    block-size: 32px;
    border-radius: 12px; /* §7 rule 10 — labeled-chip class */
    background: color-mix(in srgb, var(--secondary) 16%, transparent);
    color: var(--secondary);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
  }
  :global([data-theme='light']) .help-section-icon {
    background: color-mix(in srgb, var(--primary) 12%, transparent);
    color: var(--primary);
  }
  .help-section-title {
    margin: 0;
    color: var(--text);
    font-size: 16px;
    font-weight: 600;
    letter-spacing: -0.005em;
  }
  .help-section-body {
    color: var(--text-muted);
    font-size: 14px;
    line-height: 1.65;
  }
  /*
   * "Example" / "Tip" callouts share the same neutral surface — soft
   * surface-2 background, 12dp radius (alert per §5.10 styling), a
   * single bolded lead-word to break up the paragraph rhythm.
   */
  .help-section-example,
  .help-tip {
    color: var(--text);
    background: var(--surface-2);
    border: 1px solid var(--divider);
    border-radius: 12px; /* §5.10 alert */
    padding: 12px 14px;
    font-size: 13px;
    line-height: 1.6;
  }
  .help-section-example::before {
    content: 'Example. ';
    color: var(--text-tertiary);
    font-weight: 600;
  }
  :global([dir='rtl']) .help-section-example::before {
    content: 'مثال. ';
  }

  /* ─── Steps list (quick-start) ─────────────────────────────────── */
  .help-steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
    counter-reset: none;
  }
  .help-steps li {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    color: var(--text);
    font-size: 14px;
    line-height: 1.55;
  }
  .step-num {
    flex: 0 0 auto;
    inline-size: 22px;
    block-size: 22px;
    border-radius: 12px;
    background: color-mix(in srgb, var(--secondary) 18%, transparent);
    color: var(--secondary);
    font-size: 12px;
    font-weight: 600;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  :global([data-theme='light']) .step-num {
    background: color-mix(in srgb, var(--primary) 14%, transparent);
    color: var(--primary);
  }

  /* ─── Definition lists (Stages + Glossary) ─────────────────────── */
  .help-defs {
    display: grid;
    grid-template-columns: max-content 1fr;
    column-gap: 18px;
    row-gap: 10px;
    margin: 0;
  }
  .help-defs dt {
    color: var(--text);
    font-size: 13px;
    font-weight: 600;
    padding-block-start: 2px;
  }
  .help-defs dd {
    color: var(--text-muted);
    font-size: 13px;
    line-height: 1.55;
    margin: 0;
  }
  @media (max-width: 600px) {
    .help-defs {
      grid-template-columns: 1fr;
      row-gap: 4px;
    }
    .help-defs dd {
      margin-block-end: 8px;
    }
  }

  /* ─── External-resource links ──────────────────────────────────── */
  .help-links {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .help-links a {
    color: var(--secondary);
    font-size: 14px;
    text-decoration: none;
    border-block-end: 1px dashed transparent;
  }
  :global([data-theme='light']) .help-links a {
    color: var(--primary);
  }
  .help-links a:hover {
    border-block-end-color: currentColor;
  }
</style>

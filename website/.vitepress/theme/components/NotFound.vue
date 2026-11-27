<script setup lang="ts">
import { withBase } from 'vitepress'

const links = [
  { text: 'What is Mk?', link: '/guide/', note: 'Start of the guide' },
  { text: 'Installation', link: '/guide/installation', note: 'Install mk or build it from source' },
  { text: 'Language reference', link: '/reference/', note: 'Every rule, section by section' },
  { text: 'Standard library', link: '/reference/std/', note: 'What each std module offers' },
]
</script>

<template>
  <main class="mk-404" aria-labelledby="mk-404-title">
    <p class="code">404</p>
    <h1 id="mk-404-title">This page does not exist</h1>
    <p class="lede">
      The link may be out of date, or the page may have moved. Search with
      <kbd>/</kbd> or <kbd>Ctrl</kbd> <kbd>K</kbd>, or pick up from one of these:
    </p>
    <ul>
      <li v-for="item in links" :key="item.link">
        <a :href="withBase(item.link)">
          <strong>{{ item.text }}</strong>
          <span>{{ item.note }}</span>
        </a>
      </li>
    </ul>
  </main>
</template>

<style scoped>
.mk-404 {
  max-width: 640px;
  margin: 0 auto;
  padding: 96px 24px 128px;
}

.code {
  margin: 0;
  font-family: var(--vp-font-family-mono);
  font-size: 14px;
  font-weight: 600;
  color: var(--vp-c-brand-1);
}

h1 {
  margin: 8px 0 16px;
  font-size: 32px;
  font-weight: 700;
  line-height: 1.25;
  letter-spacing: -0.02em;
}

.lede {
  margin: 0 0 32px;
  line-height: 1.7;
  color: var(--vp-c-text-2);
}

kbd {
  padding: 1px 6px;
  border: 1px solid var(--vp-c-divider);
  border-radius: 4px;
  font-family: var(--vp-font-family-mono);
  font-size: 12px;
}

ul {
  display: grid;
  gap: 12px;
  margin: 0;
  padding: 0;
  list-style: none;
}

a {
  display: block;
  padding: 14px 18px;
  border: 1px solid var(--vp-c-divider);
  border-radius: 8px;
  color: inherit;
  text-decoration: none;
  transition: border-color 0.15s ease;
}

a:hover {
  border-color: var(--vp-c-brand-1);
}

strong {
  display: block;
  font-size: 15px;
}

span {
  display: block;
  margin-top: 2px;
  font-size: 14px;
  color: var(--vp-c-text-2);
}
</style>

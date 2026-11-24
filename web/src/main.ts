import { mount } from 'svelte';
// KaTeX first: its stylesheet draws the glyphs, ours decides how big they are next to
// prose and what colour the syntax is, so the order is the cascade.
import 'katex/dist/katex.min.css';
// The app's own stylesheet goes here, once it is written: `import './app.css';`
// Its content is specified in docs/ui-spec.md §2 — tokens first, then the shared controls of §3.
import App from './App.svelte';

export default mount(App, { target: document.getElementById('app')! });

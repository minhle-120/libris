import { mount } from 'svelte';
// KaTeX first: its stylesheet draws the glyphs, ours decides how big they are next to
// prose and what colour the syntax is, so the order is the cascade.
import 'katex/dist/katex.min.css';
// The design system: tokens, then the shared controls. Component styles come after it in the
// bundle, so a component overrides a global rule by being later, not by being louder.
import './app.css';
import App from './App.svelte';

export default mount(App, { target: document.getElementById('app')! });

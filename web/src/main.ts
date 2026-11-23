import { mount } from 'svelte';
// KaTeX first: its stylesheet draws the glyphs, ours decides how big they are next to
// prose and what colour the syntax is, so the order is the cascade.
import 'katex/dist/katex.min.css';
import './app.css';
import App from './App.svelte';

export default mount(App, { target: document.getElementById('app')! });

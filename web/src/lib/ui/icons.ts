/**
 * The icon set, in one file so a glyph cannot be defined twice and a name cannot be invented
 * quietly. Line icons, no fill, current colour, uniform stroke — and a new one needs a reason.
 */
export type IconName =
  | 'plus'
  | 'sun'
  | 'moon'
  | 'book'
  | 'upload'
  | 'inbox'
  | 'chat'
  | 'pulse'
  | 'settings'
  | 'file'
  | 'image'
  | 'trash'
  | 'x'
  | 'alert'
  | 'clock'
  | 'open'
  | 'layers'
  | 'retry'
  | 'stop'
  | 'download'
  | 'send'
  | 'pencil'
  | 'collapse'
  | 'expand'
  | 'back'
  | 'check'
  | 'search';

export const ICON_PATHS: Record<IconName, string> = {
  plus: 'M8 3.5v9M3.5 8h9',
  sun: 'M8 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM8 1.5v1.5M8 13v1.5M1.5 8H3M13 8h1.5M3.4 3.4l1 1M11.6 11.6l1 1M12.6 3.4l-1 1M4.4 11.6l-1 1',
  moon: 'M13.2 9.8A5.6 5.6 0 0 1 4.2 3.6 5.6 5.6 0 1 0 13.2 9.8Z',
  book: 'M3 2.5h6.5a2.5 2.5 0 0 1 2.5 2.5v10a2 2 0 0 0-2-2H3ZM3 2.5v10.5M12 5v7.5',
  upload: 'M8 11V3M5 6l3-3 3 3M3 13h10',
  inbox: 'M2.5 8.5h4l1 2h1l1-2h4M2.5 8.5 4 3h8l1.5 5.5V13H2.5Z',
  chat: 'M2.5 3.5h11v7h-7l-3 2.5v-2.5h-1Z',
  pulse: 'M2 8h2.5l1.5-4 3 8 1.5-4H14',
  settings:
    'M8 10.2a2.2 2.2 0 1 0 0-4.4 2.2 2.2 0 0 0 0 4.4ZM8 1.8l1.1 1.6h2l.4 1.9 1.6.9-.8 1.9.8 1.9-1.6.9-.4 1.9h-2L8 14.2l-1.1-1.5h-2l-.4-1.9-1.6-.9.8-1.9-.8-1.9 1.6-.9.4-1.9h2Z',
  file: 'M3.5 2h6l3 3v9h-9ZM9.5 2v3h3',
  image: 'M2.5 3.5h11v9h-11ZM2.5 10l3-3 2.5 2.5L11 6l2.5 3M5.8 6.2a.8.8 0 1 0 0-1.6.8.8 0 0 0 0 1.6Z',
  trash: 'M3.5 4.5h9M6.5 4.5V3h3v1.5M5 4.5l.5 9h5l.5-9M7 7v4M9 7v4',
  x: 'M4 4l8 8M12 4l-8 8',
  alert: 'M8 2 14 13H2ZM8 6v3.5M8 11.4v.1',
  clock: 'M8 14.5a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13ZM8 4.5V8l2.5 1.6',
  open: 'M6 4H3.5v9h9V10M9 2h5v5M14 2 7.5 8.5',
  layers: 'M8 2 2 5.5 8 9l6-3.5ZM2 9.5 8 13l6-3.5M2 12.2 8 15.7l6-3.5',
  retry: 'M13.5 8a5.5 5.5 0 1 1-1.9-4.2M13.5 1.5V4h-2.5',
  stop: 'M5 5h6v6H5Z',
  download: 'M8 2.5v8M4.5 7 8 10.5 11.5 7M3.5 13.5h9',
  send: 'M3 8h9M9 4.5 12.5 8 9 11.5',
  pencil: 'M3 13.2l.6-2.5 7-7 1.9 1.9-7 7ZM11.6 2.7l1.9 1.9',
  collapse: 'M10 4 6 8l4 4',
  expand: 'M6 4l4 4-4 4',
  // The panel's own back-turning chevron: the mirror of the toggle, so the two are
  // not mistaken for each other at a glance.
  back: 'M10 4 6 8l4 4',
  check: 'M3.5 8.5 6.5 11.5 12.5 4.5',
  search: 'M7 11.5a4.5 4.5 0 1 0 0-9 4.5 4.5 0 0 0 0 9ZM10.5 10.5 13.5 13.5'
};

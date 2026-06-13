# Kyvik UI/UX Design Guide — "Nordic Burrow"

Reference document for all agents working on the Kyvik web dashboard. Follow these patterns to maintain visual and behavioral consistency.

## Aesthetic Direction

**"Nordic Burrow"** — warm, earthy, dark-first. A command center feel for infrastructure/security tools, inspired by the badger mascot and Scandinavian roots.

Key visual motifs:
- **Grain texture overlay** on the page background (subtle SVG noise)
- **Glow pulse** on running status badges (teal shimmer)
- **Diagonal stripe motif** on card headers (badger's face stripe)
- **Amber stripe divider** below the sidebar brand
- **Glass-morphism badges** with `backdrop-filter: blur()`
- **Staggered card reveal** animation on page load
- **Hover depth shift** on cards (`translateY(-1px)`)

## Tech Stack

- **Rendering:** Server-side Go templates (`web/templates/`) with HTMX for interactivity
- **Styling:** Single vanilla CSS file (`web/static/css/style.css`) using CSS custom properties
- **Fonts:** [Instrument Serif](https://fonts.google.com/specimen/Instrument+Serif) (display headings), [Outfit](https://fonts.google.com/specimen/Outfit) (body), [IBM Plex Mono](https://fonts.google.com/specimen/IBM+Plex+Mono) (code), loaded via Google Fonts
- **No JS framework.** Inline `<script>` blocks only for theme toggle, sidebar toggle, and page-specific logic (e.g., SSE chat). Keep JS minimal.

## Color System

All colors are defined as CSS custom properties in `:root` (light) and overridden in `[data-theme="dark"]`. **Never use raw hex values in components** — always reference variables.

### Core Variables

| Variable | Light | Dark | Usage |
|---|---|---|---|
| `--content-bg` | `#F5F0E8` | `#131316` | Page background (warm parchment / deep charcoal) |
| `--card-bg` | `#FFFFFF` | `#1A1A1F` | Cards, inputs, panels |
| `--text-primary` | `#1C1917` | `#E8E4DC` | Headings, body text |
| `--text-secondary` | `#57534E` | `#A8A29E` | Descriptions, supporting text |
| `--text-muted` | `#A8A29E` | `#78716C` | Hints, timestamps, labels |
| `--border-color` | `#E7E5E4` | `#2A2A2F` | Borders, dividers |
| `--input-border` | `#D6D3D1` | `#3A3A40` | Form input borders |
| `--input-bg` | `#FFFFFF` | `#1A1A1F` | Form input backgrounds |
| `--input-focus` | `#D4A853` | (same) | Focus ring color |
| `--primary` | `#D4A853` | (same) | Amber/honey — primary accent, links, buttons |
| `--primary-hover` | `#C49A45` | (same) | Primary button hover |
| `--success` | `#2D8C6F` | (same) | Muted teal — running status, success actions |
| `--success-hover` | `#246E58` | (same) | Success button hover |
| `--danger` | `#C45B4A` | (same) | Warm red — error status, destructive actions |
| `--danger-hover` | `#A94D3F` | (same) | Danger button hover |
| `--warning` | `#D4A853` | (same) | Same as primary (amber) |

### Sidebar Variables

The sidebar is always dark (the "burrow"). These are consistent across themes:

| Variable | Value |
|---|---|
| `--sidebar-bg` | `#0C0C0E` |
| `--sidebar-hover-bg` | `#1A1A1F` |
| `--sidebar-text` | `#9B9B9F` |
| `--sidebar-active` | `#E8E4DC` |
| `--sidebar-width` | `220px` |

### Effect Variables

| Variable | Light | Dark | Usage |
|---|---|---|---|
| `--grain-opacity` | `0.03` | `0.04` | Grain texture overlay opacity |
| `--glow-color` | `rgba(45, 140, 111, 0.4)` | `rgba(45, 140, 111, 0.5)` | Running badge glow |
| `--stripe-color` | `rgba(212, 168, 83, 0.06)` | `rgba(212, 168, 83, 0.04)` | Card header stripe |
| `--glass-bg` | `rgba(255, 255, 255, 0.6)` | `rgba(26, 26, 31, 0.7)` | Glass-morphism bg |
| `--glass-blur` | `8px` | `10px` | Badge backdrop blur |

### Font Variables

| Variable | Value | Usage |
|---|---|---|
| `--font-display` | `"Instrument Serif", Georgia, serif` | h1, h2 headings |
| `--font-stack` | `"Outfit", -apple-system, sans-serif` | Body text, labels, buttons |
| `--font-mono` | `"IBM Plex Mono", "SF Mono", monospace` | Code, monospace |

### Dark Mode Badge/Alert Pattern

Badges and alerts use hardcoded colors (not variables) because they need distinct light/dark treatments. In dark mode, use **semi-transparent backgrounds with lighter text**:

```css
/* Light: translucent bg + dark text */
.badge-running { background: rgba(45, 140, 111, 0.12); color: #1A6B52; }

/* Dark: translucent bg + bright text */
[data-theme="dark"] .badge-running { background: rgba(45, 140, 111, 0.15); color: #5EC4A0; }
```

Follow this pattern for any new badge or alert variant.

## Dark Mode

### How It Works

1. An inline `<script>` in `<head>` (before CSS loads) sets `data-theme` on `<html>` from `localStorage`, falling back to `prefers-color-scheme: dark`. This prevents flash-of-wrong-theme.
2. CSS overrides all variables via `[data-theme="dark"] { ... }`.
3. Components with hardcoded colors (badges, alerts, hover states) get explicit `[data-theme="dark"] .class` overrides.
4. The toggle button calls `toggleTheme()` which flips the attribute and saves to `localStorage`.

### Rules for New Components

- Use CSS variables for all colors — they switch automatically.
- If you must use a hardcoded color (e.g., a colored background that doesn't map to an existing variable), add a `[data-theme="dark"]` override.
- For hover/focus states with hardcoded light backgrounds, add a dark override using `rgba()` with low opacity.
- Test both themes. Common things that break: hardcoded white text on light backgrounds, unreadable contrast, invisible borders.

### Standalone Pages

Pages outside `layout.html` (like `login.html`) need their own:
- Google Fonts `<link>` tags (Instrument Serif + Outfit + IBM Plex Mono)
- Theme init `<script>` in `<head>`
- `toggleTheme()` function
- A `.theme-toggle-float` button (fixed top-right)

## Typography

| Role | Font | Weight | Size |
|---|---|---|---|
| Display headings (h1, h2) | Instrument Serif | 400 | 26-28px |
| Body text | Outfit | 400 | 14px |
| Labels / small text | Outfit | 600 | 13px |
| Card headings (h3) | Outfit | 600 | 16px |
| Monospace / code | IBM Plex Mono | 400 | 13px |
| Muted / hints | Outfit | 400 | 12px |
| Sidebar brand | Instrument Serif | 400 | 20px |
| Mobile brand | Instrument Serif | 400 | 18px |

Use `.text-mono` for inline monospace. Base `line-height: 1.5`.

## Visual Effects

### Grain Texture

Applied via `body::before` with a fixed-position pseudo-element using an inline SVG `feTurbulence` filter. No external files needed. Controlled by `--grain-opacity`.

### Glow Pulse (Running Badge)

`.badge-running::before` uses a `glow-pulse` keyframe animation (2s ease-in-out infinite) that alternates `box-shadow` between subtle and visible glow using `--glow-color`.

### Diagonal Stripe Motif

`.card-header::before` renders a `repeating-linear-gradient(135deg, ...)` stripe at the top of card headers, inspired by the badger's face stripe. Uses `--stripe-color` for theme-aware opacity.

### Glass-Morphism Badges

`.badge` applies `backdrop-filter: blur(var(--glass-blur))` for a frosted glass effect. This is progressive enhancement — badges remain readable without blur support.

### Staggered Card Reveal

`.card-grid > .card` gets a `card-reveal` animation with 50ms staggered delays (up to 10 cards). **Important:** This is disabled inside `#agent-cards` to prevent re-trigger on HTMX polling updates.

### Card Hover Depth

Cards shift up 1px on hover (`transform: translateY(-1px)`) with an enhanced shadow for a subtle depth effect.

### Amber Stripe Divider

`.sidebar-brand::after` renders a gradient line from amber to transparent below the brand name.

### Custom Scrollbars

Styled to match the palette using `::-webkit-scrollbar` and `scrollbar-color` (Firefox).

## Responsive Layout

### Strategy: Mobile-First

Base CSS = mobile. Features are added at wider breakpoints via `@media (min-width: ...)`.

### Breakpoints

| Name | Query | Purpose |
|---|---|---|
| Mobile | Base (no query) | Default single-column layout |
| Tablet | `@media (min-width: 768px)` | Sidebar visible, multi-column grids |
| Desktop | `@media (min-width: 1024px)` | Wizard step labels shown |
| Large | `@media (min-width: 1280px)` | Wider padding, 5-column stat grid |

### Sidebar Behavior

- **Mobile:** Hidden off-screen (`transform: translateX(-100%)`). A `.mobile-topbar` with hamburger button is shown. Tapping the hamburger adds `.sidebar-open` to `.layout`, which slides the sidebar in and shows a `.sidebar-overlay`. Tapping the overlay closes it.
- **Tablet+:** Sidebar is always visible (220px). Mobile topbar and overlay are `display: none`.

### Layout Behavior by Breakpoint

| Component | Mobile | Tablet (768px+) | Large (1280px+) |
|---|---|---|---|
| Layout | Block, no grid | `grid: 220px 1fr` | Same |
| Sidebar | Off-canvas drawer | Fixed 220px | Same |
| Main padding | `16px` (top: `64px` for topbar) | `24px` | `32px` |
| Page header | Column (title stacked above button) | Row | Same |
| Stat grid | `2 columns` | `auto-fill, 200px` | `5 columns` |
| Card grid | `1 column` | `auto-fill, 320px` | Same |
| Wizard | Full width, step numbers only | `720px max`, labels shown at 1024px | Same |
| Chat messages | `90% max-width` | `70% max-width` | Same |
| Tables | Horizontal scroll via `.table-wrap` | Full width | Same |

## Component Reference

### Page Structure

Every page inside the dashboard uses `layout.html` which provides the sidebar + main content shell. Page content is injected via `{{.Content}}`.

```
layout.html
  ├── .sidebar-overlay
  ├── aside.sidebar
  │     ├── .sidebar-brand (with ::after amber stripe)
  │     ├── nav.sidebar-nav (SVG icons + .nav-text spans)
  │     └── .sidebar-footer (theme toggle + logout)
  ├── .mobile-topbar (hamburger + brand)
  └── main.main-content
        └── {{.Content}}
```

### Navigation Icons

Sidebar nav uses inline SVGs inside `.nav-icon` spans, with text wrapped in `.nav-text` spans:
- **Dashboard:** Grid icon (4 squares)
- **Agents:** Person icon
- **Secrets:** Lock icon

### Cards

The primary content container. Cards have a diagonal stripe motif via `.card-header::before` and hover depth shift.

```html
<div class="card">
  <div class="card-header">
    <h3>Title</h3>
    <span class="badge badge-running">running</span>
  </div>
  <div class="card-body">
    <p>Description text</p>
  </div>
  <div class="card-actions">
    <button class="btn btn-success btn-sm">Action</button>
  </div>
</div>
```

### Badges

Pill-shaped status indicators with glass-morphism effect.

**Status badges:** `badge-running` (teal + glow), `badge-stopped` (warm gray), `badge-starting` (amber), `badge-error` (warm red), `badge-paused` (indigo), `badge-quarantined` (orange-red)

**Template badges:** `badge-reader` (blue), `badge-worker` (amber), `badge-admin` (warm red)

**Utility badges:** `badge-default` (neutral), `badge-info` (blue)

### Buttons

```html
<button class="btn btn-primary">Primary (amber bg, dark text)</button>
<button class="btn btn-success btn-sm">Small Success (teal)</button>
<button class="btn btn-danger">Danger (warm red)</button>
<button class="btn btn-secondary">Secondary (border-color bg)</button>
```

Primary buttons use dark text (`#1C1917`) on amber background for ~6.5:1 contrast ratio.

### Forms

```html
<div class="form-group">
  <label for="field">Label</label>
  <input type="text" id="field" name="field" class="form-input" required>
  <span class="hint">Help text</span>
</div>
```

**Input classes:** `form-input`, `form-select`, `form-textarea`

Focus ring uses amber glow (`rgba(212, 168, 83, 0.25)`). Checkbox/radio elements use `accent-color: var(--primary)`.

### Dialogs / Modals

Dialogs use the native `<dialog>` element. The CSS reset `* { margin: 0; }` is counteracted by `dialog { margin: auto; }` to restore native centering.

For memory-style dialogs, use the `.memory-dialog` class:

```html
<dialog id="my-dialog" class="memory-dialog">
  <div class="card" style="min-width: 320px; max-width: 520px;">
    <!-- dialog content -->
  </div>
</dialog>
```

For inline dialogs (like secrets), style the `<dialog>` element directly with card-like styling.

### Progress Bars

Use an amber-to-gold gradient:

```html
<div class="progress-bar">
  <div class="progress-fill" style="width: 65%;"></div>
</div>
```

### Alerts, Tables, Wizard, Chat, Empty State

Same markup patterns as before — see the templates for examples. All colors now use the Nordic Burrow palette.

## HTMX Patterns

- **Inline updates:** `hx-target="#element-id"` + `hx-swap="outerHTML"` for updating a single row/card without page reload
- **Full page refresh:** `hx-target="body"` when the fragment shape doesn't match
- **Status polling:** `hx-get="/agents/{id}/status"` + `hx-trigger="every 5s"` on badge elements
- **Wizard navigation:** Each step is a `<form>` with hidden fields. HTMX posts and swaps `#wizard-content`
- **SSE (chat):** `EventSource` for real-time agent responses (vanilla JS, not HTMX)
- **Loading indicator:** `.htmx-indicator` class (hidden by default, shown during requests)
- **Card animations:** Staggered card reveal is disabled inside `#agent-cards` to prevent re-trigger on HTMX polling

## Accessibility

- All interactive elements must be keyboard-accessible
- Buttons use `focus-visible` outlines (2px solid `--primary` / amber)
- Hamburger button has `aria-label="Toggle menu"`
- Theme toggle has `title="Toggle dark mode"`
- Use `.sr-only` for screen-reader-only text when needed
- Form inputs always have associated `<label>` elements with `for` attribute
- Badge glass-morphism degrades gracefully — badges remain readable without `backdrop-filter` support
- Primary button text uses `#1C1917` on amber for ~6.5:1 contrast ratio

## Checklist for New Pages/Components

1. Use CSS variables for all colors
2. Write mobile-first CSS (base = mobile, add `@media` for wider)
3. Test in both light and dark themes
4. Test at mobile (< 768px), tablet, and large (1280px+) widths
5. Follow existing component patterns (cards, buttons, forms, badges)
6. Ensure HTMX interactions still work after changes
7. Keep JS minimal — prefer CSS solutions and HTMX
8. Use Instrument Serif for display headings, Outfit for body, IBM Plex Mono for code
9. Add `[data-theme="dark"]` overrides for any hardcoded colors
10. Verify dialog centering works (use `.memory-dialog` class or ensure `margin: auto`)
11. Run `make build` to verify Go template compilation
12. Run `make test` to catch handler/template regressions

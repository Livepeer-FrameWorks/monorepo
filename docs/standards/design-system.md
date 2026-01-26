# FrameWorks Design System

> Design principles and patterns for the FrameWorks UI.
> This document captures the visual language and component philosophy for AI agents and developers.

## Core Philosophy

### 1. Slabs, Not Cards

**The Problem with Cards:**

- Cards float in space with padding and rounded corners
- They create visual noise with shadows and borders everywhere
- Gaps between cards waste space and break visual flow
- Nested rounded corners inside cards look cluttered

**The Slab Approach:**

- Slabs are solid, structural blocks that fill their container
- They touch edges (flush to seams, no outer padding)
- Content is organized into header/body/actions zones
- Seams (thin lines) separate slabs instead of gaps

```
┌─────────────────────┬─────────────────────┐
│  SLAB HEADER        │  SLAB HEADER        │  ← Uppercase, padded
│  ─────────────────  │  ─────────────────  │  ← Seam (1px line)
│  Content with       │  Content with       │
│  padding            │  padding            │  ← Body (padded for text)
│  ─────────────────  │  ─────────────────  │  ← Seam
│ [Action] │ [Action] │      [Action]       │  ← Actions (flush, square)
├─────────────────────┼─────────────────────┤  ← Seam between slabs
│  NEXT SLAB...       │  NEXT SLAB...       │
```

### 2. Seams, Not Gaps

**Gaps** = empty space between elements (creates floating effect)
**Seams** = thin lines that join elements (creates solid, unified surface)

- Use `border` (1px, gutter color at 30% opacity) instead of `gap`
- Seams make the interface feel like one cohesive surface
- Elements feel connected, not scattered

### 3. Flush Actions, Padded Content

**Text content needs breathing room:**

- Titles, paragraphs, form fields get padding
- Text should never touch a seam edge

**Actions fill their space:**

- Buttons in action zones have `border-radius: 0`
- They stretch to fill available width
- Seams separate multiple actions (not gaps)
- This creates a solid, tactile feel

### 4. Structured Zones

Every slab has up to 3 zones:

| Zone        | Purpose        | Styling                                               |
| ----------- | -------------- | ----------------------------------------------------- |
| **Header**  | Title, badges  | Padded, uppercase text, border-bottom seam            |
| **Body**    | Main content   | Padded for text, can be flush for special content     |
| **Actions** | Buttons, links | Flush (no padding), buttons fill space, seams between |

## Color Philosophy (Tokyo Night)

### Surfaces (Dark to Light)

```
--tn-bg-dark     → Darkest (slab backgrounds)
--tn-bg          → Main background
--tn-bg-highlight → Elevated surfaces (hover states, panels)
--tn-bg-visual   → Selection/active states
```

### Text Hierarchy

```
--tn-fg          → Primary text (high contrast)
--tn-fg-dark     → Secondary text (muted)
--tn-fg-gutter   → Borders, seams, dividers
```

### Accent Colors (Semantic)

```
--tn-blue   → Primary actions, links
--tn-green  → Success, positive states
--tn-red    → Destructive, errors, live indicators
--tn-yellow → Warnings, attention
--tn-purple → Secondary accent, special features
--tn-cyan   → Info, neutral highlights
```

## Responsive Patterns

### Grid Stacking

For 4-item grids (stats, metrics):

- Desktop (≥1024px): 4 columns
- Tablet (640-1023px): 2×2 grid
- Mobile (<640px): 1 column stack

**Key insight:** Don't jump from 4→1. The 2×2 intermediate state prevents the "squished" look on tablets.

### Full-Bleed vs Contained

| Element             | Behavior                                        |
| ------------------- | ----------------------------------------------- |
| Page headers        | Contained (max-width + padding for readability) |
| Stats grids         | Full-bleed (edge to edge)                       |
| Content grids       | Full-bleed with seams                           |
| Text-heavy sections | Contained                                       |

## Component Patterns

### Slab Structure

```html
<div class="slab">
  <div class="slab-header">
    <h3>Title Here</h3>
  </div>
  <div class="slab-body--padded">
    <!-- Text content, forms, data displays -->
  </div>
  <div class="slab-actions slab-actions--row">
    <button variant="ghost">Action 1</button>
    <button variant="ghost">Action 2</button>
  </div>
</div>
```

### GridSeam (Metric Cards)

```html
<GridSeam cols="{4}" stack="2x2" surface="panel" flush="{true}">
  <div><MetricCard ... /></div>
  <div><MetricCard ... /></div>
  <div><MetricCard ... /></div>
  <div><MetricCard ... /></div>
</GridSeam>
```

### Dashboard Grid

```html
<div class="dashboard-grid">
  <div class="slab">...</div>
  <div class="slab">...</div>
  <div class="slab">...</div>
  <div class="slab">...</div>
</div>
```

## Button Variants in Context

| Context            | Variant       | Border Radius        |
| ------------------ | ------------- | -------------------- |
| Standalone         | `default`     | Normal (rounded)     |
| In slab actions    | `ghost`       | Forced to 0 (square) |
| Outlined secondary | `outline`     | Normal               |
| Destructive        | `destructive` | Normal               |

## Anti-Patterns to Avoid

### DON'T: Nested Rounded Corners

```html
<!-- Bad: Card inside card, both rounded -->
<div class="rounded-lg p-4">
  <div class="rounded-lg p-4">
    <button class="rounded-lg">Click</button>
  </div>
</div>
```

### DON'T: Gaps Everywhere

```html
<!-- Bad: gap-6 creates floating cards -->
<div class="grid gap-6">
  <div class="card">...</div>
  <div class="card">...</div>
</div>
```

### DON'T: Padding on Action Containers

```html
<!-- Bad: Buttons don't fill space -->
<div class="p-4">
  <button class="w-full">Click</button>
</div>
```

### DO: Seams and Flush Actions

```html
<!-- Good: Solid, unified surface -->
<div class="dashboard-grid">
  <div class="slab">
    <div class="slab-header"><h3>Title</h3></div>
    <div class="slab-body--padded">Content here</div>
    <div class="slab-actions">
      <button variant="ghost">Action</button>
    </div>
  </div>
</div>
```

## CSS Custom Properties Reference

### Spacing

```css
--space-xs: 0.25rem /* 4px */ --space-sm: 0.5rem /* 8px */ --space-md: 1rem
  /* 16px */ --space-lg: 1.5rem /* 24px */ --space-xl: 2rem /* 32px */
  --space-2xl: 3rem /* 48px */;
```

### Seam Styling

```css
/* Standard seam */
border: 1px solid hsl(var(--tn-fg-gutter) / 0.3);

/* Stronger seam (between major sections) */
border: 1px solid hsl(var(--tn-fg-gutter) / 0.5);
```

### Slab Header Text

```css
font-size: 0.875rem;
font-weight: 600;
text-transform: uppercase;
letter-spacing: 0.05em;
color: hsl(var(--tn-fg-dark));
```

## Implementation references (tokens & primitives)

These point to the current canonical implementation. Other apps may extend or override.

| Purpose            | File                                                            |
| ------------------ | --------------------------------------------------------------- |
| Design tokens      | `website_application/src/styles/tokens.css`                     |
| Layout primitives  | `website_application/src/styles/layout.css`                     |
| Slab classes       | `website_application/src/styles/layout.css` (SLAB SYSTEM section) |
| GridSeam component | `website_application/src/lib/components/layout/GridSeam.svelte` |
| Marketing tokens   | `website_marketing/src/index.css`                               |
| Marketing layout   | `website_marketing/src/styles/marketing/layout.css`             |

## Summary: The FrameWorks Look

1. **Solid, not floaty** - Elements connect via seams, not float in gaps
2. **Structured zones** - Header/Body/Actions pattern for slabs
3. **Flush where functional** - Actions fill space, text gets padding
4. **Dark, unified surface** - One continuous interface, not scattered cards
5. **Responsive with intention** - 4→2×2→1 flow, not abrupt jumps
6. **Tokyo Night palette** - Dark backgrounds, bright accents, subtle seams

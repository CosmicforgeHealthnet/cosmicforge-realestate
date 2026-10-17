---
name: CosmicForge Individual Owner
colors:
  surface: '#f9f9ff'
  surface-dim: '#ccdbf8'
  surface-bright: '#f9f9ff'
  surface-container-lowest: '#ffffff'
  surface-container-low: '#f0f3ff'
  surface-container: '#e7eeff'
  surface-container-high: '#dee8ff'
  surface-container-highest: '#d5e3ff'
  on-surface: '#0c1c31'
  on-surface-variant: '#434654'
  inverse-surface: '#223147'
  inverse-on-surface: '#ebf1ff'
  outline: '#747686'
  outline-variant: '#c3c5d7'
  surface-tint: '#1852d9'
  primary: '#0041bc'
  on-primary: '#ffffff'
  primary-container: '#2459e0'
  on-primary-container: '#dce1ff'
  inverse-primary: '#b5c4ff'
  secondary: '#006b5d'
  on-secondary: '#ffffff'
  secondary-container: '#9cf3e1'
  on-secondary-container: '#037163'
  tertiary: '#1441b6'
  on-tertiary: '#ffffff'
  tertiary-container: '#375bcf'
  on-tertiary-container: '#dce1ff'
  error: '#ba1a1a'
  on-error: '#ffffff'
  error-container: '#ffdad6'
  on-error-container: '#93000a'
  primary-fixed: '#dce1ff'
  primary-fixed-dim: '#b5c4ff'
  on-primary-fixed: '#00164e'
  on-primary-fixed-variant: '#003baf'
  secondary-fixed: '#9cf3e1'
  secondary-fixed-dim: '#80d6c5'
  on-secondary-fixed: '#00201b'
  on-secondary-fixed-variant: '#005046'
  tertiary-fixed: '#dce1ff'
  tertiary-fixed-dim: '#b6c4ff'
  on-tertiary-fixed: '#001550'
  on-tertiary-fixed-variant: '#053ab0'
  background: '#f9f9ff'
  on-background: '#0c1c31'
  surface-variant: '#d5e3ff'
  canvas-light: '#F6F8FC'
  card-light: '#FFFFFF'
  surface-subtle-light: '#EDF2FA'
  ink-primary: '#17263C'
  ink-secondary: '#536479'
  border-default: '#DCE4F0'
  canvas-dark: '#0C1525'
  card-dark: '#17253A'
  surface-subtle-dark: '#1E3048'
  text-dark: '#ECF3FF'
  text-dark-secondary: '#ADC0D9'
  border-dark: '#344966'
  brand-sapphire: '#2459E0'
  brand-hover: '#1743B8'
  brand-dark-action: '#6C96FF'
  status-positive: '#087A66'
  status-positive-dark: '#58D4BC'
  status-warning: '#9A6507'
  status-warning-dark: '#F2BD69'
  status-danger: '#C93647'
  status-danger-dark: '#FF8694'
  finance-available: '#087A66'
  finance-pending: '#2459E0'
  finance-held: '#7048E8'
  finance-restricted: '#C93647'
typography:
  display-hero:
    fontFamily: Inter
    fontSize: 44px
    fontWeight: '700'
    lineHeight: 52px
    letterSpacing: -0.02em
  display-hero-mobile:
    fontFamily: Inter
    fontSize: 32px
    fontWeight: '700'
    lineHeight: 40px
    letterSpacing: -0.015em
  headline-lg:
    fontFamily: Inter
    fontSize: 32px
    fontWeight: '600'
    lineHeight: 40px
    letterSpacing: -0.015em
  headline-lg-mobile:
    fontFamily: Inter
    fontSize: 26px
    fontWeight: '600'
    lineHeight: 34px
    letterSpacing: -0.01em
  headline-md:
    fontFamily: Inter
    fontSize: 22px
    fontWeight: '600'
    lineHeight: 30px
    letterSpacing: -0.01em
  headline-sm:
    fontFamily: Inter
    fontSize: 18px
    fontWeight: '600'
    lineHeight: 26px
  body-lg:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: '400'
    lineHeight: 24px
  body-md:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: '400'
    lineHeight: 20px
  body-bold:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: '600'
    lineHeight: 20px
  label-md:
    fontFamily: Inter
    fontSize: 13px
    fontWeight: '500'
    lineHeight: 18px
    letterSpacing: 0.01em
  label-sm:
    fontFamily: Inter
    fontSize: 12px
    fontWeight: '500'
    lineHeight: 16px
    letterSpacing: 0.02em
  meta-numeric:
    fontFamily: Inter
    fontSize: 12px
    fontWeight: '600'
    lineHeight: 16px
    letterSpacing: 0.03em
rounded:
  sm: 0.25rem
  DEFAULT: 0.5rem
  md: 0.75rem
  lg: 1rem
  xl: 1.5rem
  full: 9999px
spacing:
  gutter: 1.5rem
  gutter-mobile: 1rem
  margin: 2rem
  margin-mobile: 1rem
  space-xs: 0.25rem
  space-sm: 0.5rem
  space-md: 1rem
  space-lg: 1.5rem
  space-xl: 2rem
---

## Brand & Style

The design system establishes a high-trust, architecturally disciplined workspace for individual property owners managing high-stakes assets. The brand personality is calm, rigorous, authoritative, and uncompromised in security. It delivers clarity under legal and financial pressure, reinforcing that the owner remains the sovereign principal over their portfolio, whether managing a single urban unit or an international diaspora portfolio.

The visual style blends **Corporate Modern** with **Architectural Geometry**. It features structural cleanliness, generous whitespace, strict rectangular bounding grids, crisp dividers, and zero gratuitous ornamentation. Gradients on functional elements, ambiguous state representations, and glassmorphic blur effects are strictly prohibited. Every visual choice emphasizes transactional truth, provenance, and operational confidence.

## Colors

The palette employs a crisp architectural light foundation (`#F6F8FC`) elevated by clean pure card whites (`#FFFFFF`) and structured ink neutrals (`#17263C`). Brand Sapphire (`#2459E0`) provides decisive focus for verified primary interactive touchpoints, backed by `#1743B8` for tactile pressed states. 

Color functions semantically to reflect backend canonical truths:
- **Finance Buckets:**
  - `finance-available`: `#087A66` — settled funds ready for transfer.
  - `finance-pending`: `#2459E0` — clearing in transit according to the Financial Core.
  - `finance-held`: `#7048E8` — escrowed or tenancy-protected deposits.
  - `finance-restricted`: `#C93647` — legal freeze, compliance lock, or dispute flag.
- **Dark Mode Strategy:** The dark canvas (`#0C1525`) and elevated card containers (`#17253A`) maintain identical functional contrast ratios using brightened operational accents (`#6C96FF`, `#58D4BC`, `#ECF3FF`).

Never use ambiguous colors to indicate financial states or pseudo-verified operational conditions.

## Typography

The design system exclusively implements **Inter** across all touchpoints, utilizing font weights 400 (Regular), 500 (Medium), 600 (Semi-Bold), and 700 (Bold). Inter provides maximum technical legibility, clean vertical stems, and distinct character apertures needed when rendering tabular ledger entries, legal caveats, and transaction timestamps.

- Financial amounts, identifier codes, and ledger rows enforce tabular numeric OpenType features (`font-feature-settings: 'tnum' on, 'cv05' on`) to ensure decimal and comma alignment across high-volume statements.
- Hero levels (`44px` desktop / `32px` mobile) are reserved exclusively for onboarding welcome views and high-level portfolio valuations.
- System UI fallbacks are configured to maintain proportions if web fonts are suspended under degraded network connectivity.

## Layout & Spacing

The layout model is anchored on an architectural 8-point rhythmic grid (subdivided into 4px micro-steps for compact control clusters). Content flows strictly within disciplined containers to prevent layout drift during complex multi-pane review workflows.

- **Desktop (≥ 1200px):** Fixed 248px left collateral navigation bar, 72px structured top bar, 12-column layout grid with 24px gutters, and a maximum content containment cap of 1440px. A contextual 360px sticky rail can be triggered for action details or audit logs.
- **Tablet (768px – 1199px):** Collapsible 76px icon rail; 8-column layout grid with 16px gutters; right rail folds into a full-height slide-over drawer.
- **Mobile (< 768px):** 4-column layout grid with 16px outer margin; persistent 64px bottom tab bar for navigation (Home, Properties, Activity, Wallet, More). Modals collapse into full-width bottom sheets. Interactive touch targets are forced to a minimum dimension of 44 × 44px.

## Elevation & Depth

Visual hierarchy relies on structural architectural borders (`#DCE4F0` light, `#344966` dark) rather than heavy drop shadows. Layering reinforces data trust without decorative noise.

- **Base Surfaces:** Light mode canvases sit on `#F6F8FC`. Elevated cards, modals, and sheets take `#FFFFFF` with a crisp 1px structural border (`border.default`).
- **Ambient Shadow (Light Mode):** Restricted strictly to floating cards, top-level popovers, and sticky dialogs:
  `0 8px 28px rgba(18, 38, 70, 0.07)`.
- **Dark Mode Elevation:** Dark mode relies on tonal surface progression (`#0C1525` background to `#17253A` card to `#1E3048` nested row) paired with 1px stroke boundaries (`#344966`). Heavy drop shadows in dark mode are explicitly omitted.
- **Step-Up Verification Modals:** Modals addressing sensitive asset transfers or MFA confirmations use a dimmed barrier (`rgba(12, 21, 37, 0.65)`) with an elevated container positioned centrally with zero motion skew.

## Shapes

The shape geometry expresses precision and architectural restraint:
- **Interactive Controls & Inputs:** 10px corner radius (`0.625rem`), balancing modern usability with geometric structure.
- **Cards, Panels & Data Containers:** 16px corner radius (`1rem`), framing content cleanly against the canvas.
- **Modals, Dialogs & Bottom Sheets:** 22px corner radius (`1.375rem`) on exposed corners.
- **Status Badges & Chips:** 999px fully rounded pill geometries, differentiating operational status tags from square-cornered actionable components.

## Components

### Buttons & Interactive Triggers
- **Primary Action:** Solid Sapphire `#2459E0`, white text, 10px radius, 44px height (mobile) / 40px (desktop). Hover: `#1743B8`. Focus: 2px offset outline.
- **Secondary / Outline:** Transparent fill, 1px border `#DCE4F0`, text `#17263C`. Hover: `#EDF2FA`.
- **Destructive Action:** Solid crimson `#C93647` or 1px border `#C93647` with text `#C93647`. Requires an explicit confirmation dialog and step-up authorization.

### Financial Balance Cards
Cards must display four distinct states: `Available`, `Pending`, `Held/Protected`, and `Restricted`.
- Each balance bucket must include:
  1. Semantic title and authoritative source ledger indicator.
  2. Tabular numeric value formatted to appropriate currency (e.g., `NGN`, `USD`).
  3. Context badge (e.g., "In Escrow", "Clearing 24h", "Dispute Hold").
  4. Timestamp of the most recent ledger synchronization.
- Never show an "Available to withdraw" button on balance cards unless verified directly by backend financial services.

### Status Chips & Badges
- 999px pill format, `label-sm` font, 4px vertical / 10px horizontal padding.
- `Draft` / `Paused`: Subtle neutral fill `#EDF2FA`, text `#536479`.
- `Verified` / `Active` / `Ready`: Teal positive fill `#E6F5F2`, text `#087A66`.
- `Under Review` / `Pending User Action`: Amber warning fill `#FEF7EB`, text `#9A6507`.
- `Blocked` / `Conflict` / `Restricted`: Red danger fill `#FDF2F3`, text `#C93647`.

### Input Fields & Selects
- Height 44px, 10px radius, background `#FFFFFF`, border 1px `#DCE4F0`, text `#17263C`.
- Focus state: border `#2459E0` with a 2px outer aura (`rgba(36, 89, 224, 0.2)`).
- Error state: border `#C93647`, helper text rendered in `status-danger` with a leading 16px warning icon.

### Cards & Data Tables
- Cards use 16px padding on mobile, 24px on desktop, `#FFFFFF` background, 1px border `#DCE4F0`.
- Data tables require sticky headers, fixed heights, alternating row states (`#FFFFFF` to `#F6F8FC`), and numeric alignment anchored to the right. No hover-only controls are permitted on table rows.

### Security & Step-Up Modals
- High-stakes workflows (e.g., tenancy agreements, payout account updates, manager delegation revocation) require dedicated step-up modals featuring explicit consequence statements, TOTP/MFA inputs, and an immutable reference trace key.
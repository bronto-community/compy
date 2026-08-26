# compy menu-bar icon — direction 1c "Track + signals"

A three-toed theropod track with three marks falling into it: the dinosaur reference
(Compsognathus) and the collector's three signals — logs, metrics, traces — in one
silhouette. Chosen over a literal Grallator print because at 16pt an anatomically
correct track loses the detail that makes it recognisable, while this reads at every
size.

## Files

| file | use |
|---|---|
| `compy-menubar-running.svg` | primary state, 32×32 source |
| `compy-menubar-running@16.svg` | same geometry at 16×16 for pixel-fitting |
| `compy-menubar-stopped.svg` | collector not running |
| `compy-menubar-attention.svg` | collector is reporting errors |
| `preview.html` | all states at 88/52/32/16 on both menu-bar backgrounds |

## Implementation (macOS)

These are **template images**. Ship them black-on-transparent and let AppKit tint them:

```swift
let image = NSImage(named: "compy-menubar-running")!
image.isTemplate = true            // required — macOS handles light/dark and menu tinting
statusItem.button?.image = image
```

In SwiftUI with an asset catalogue, set the image set's **Render As** to *Template Image*.
Provide 1× (16×16) and 2× (32×32) rasters, or a single-scale PDF/SVG. Do not add colour,
gradients, or shadows — the system discards everything but alpha.

## Geometry

- 32-unit grid, all coordinates on or near half-units so the 16pt render lands on the
  pixel grid.
- Stroke weight 3 units (running/attention), 2.2 units (stopped) — 1.5px and 1.1px at
  16pt respectively. Thinner than 2.2 disappears on non-retina displays.
- Round caps. Toes converge on a filled pad at (16, 25); the three signal dots sit
  outside the toe tips with real clearance so they never fuse at small sizes.

## State without colour

macOS strips colour from template images, so state must be shape:

| state | treatment |
|---|---|
| running | solid — 3-unit strokes, filled pad, three filled dots |
| stopped | hollow — 2.2-unit strokes, outlined pad, outlined dots |
| needs attention | the three dots collapse into one heavy mark above the toes |

Never a red dot: it would be tinted away.

## Not included

The 512pt app icon. That is where an actual Compsognathus belongs — a head or full
silhouette has room to work at that size and collapses into a smudge at 16pt. The two can
be related without being the same drawing; ask if you want it designed.

## Other directions considered

Discarded, in case the question comes back: a pure fan-in pipeline glyph (clear collector,
no compy), a tail-and-spine abstraction, tapered/clawed footprint variants, and three
anatomically-shaped *Grallator* prints. All are in the design file if you want to revisit.

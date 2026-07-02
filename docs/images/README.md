# README image assets

The main [README.md](../../README.md) references the image files below. They are
**placeholders**. Drop in real assets with these exact filenames and the README
will pick them up without any Markdown edits.

Keep files reasonably small so the README loads fast on GitHub.


| File            | Used for                       | Recommended size                      | Caption / content                                                                   |
| --------------- | ------------------------------ | ------------------------------------- | ----------------------------------------------------------------------------------- |
| `logo.png`      | Header logo                    | 120×120 (square, transparent PNG)     | Ligand-X mark, exported from [build/appicon.svg](../../build/appicon.svg)           |
| `hero.gif`      | Hero animation under the title | ~820px wide, up to about 8 MB, 5 to 8s loop | Download, install, open app flow (launcher window, then the browser opens localhost:3000) |
| `first-run.png` | Screenshot gallery             | 1280×720 (16:9)                       | First-run setup wizard                                                              |
| `modules.png`   | Screenshot gallery             | 1280×720 (16:9)                       | Module selection (open-core + Pro)                                                  |
| `services.png`  | Screenshot gallery             | 1280×720 (16:9)                       | Service monitoring / container status                                               |
| `logs.png`      | Screenshot gallery             | 1280×720 (16:9)                       | Diagnostics / real-time logs                                                        |


## Optional


| File               | Used for               | Notes                                                                                                                          |
| ------------------ | ---------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `architecture.svg` | "How it works" diagram | Launcher → Docker Engine → services. The README currently uses an ASCII diagram; swap in this SVG if you want a richer visual. |
| `banner.png`       | Social / repo banner   | 1200×600, for GitHub social preview or the top of the README                                                                   |


## Tips

- Export `logo.png` from the vector source:
  ```bash
  convert -background none ../../build/appicon.svg -resize 120x120 logo.png
  ```
- Record `hero.gif` at the launcher's native window size, then compress
(for example `gifsicle -O3 --lossy=80`, or an online optimizer) to stay under about 8 MB.
- Use a consistent theme/window chrome across the four screenshots so the gallery
looks cohesive.


# README image assets

The main [README.md](../../README.md) references the files below. Keep files
reasonably small so the README loads fast on GitHub.

## Currently used


| File                   | Used for                         | Notes                                                                 |
| ---------------------- | -------------------------------- | --------------------------------------------------------------------- |
| `logo.png`             | Header logo                      | Exported from [build/appicon.svg](../../build/appicon.svg) (128×128) |
| `ligand-x-app-ui.png`  | Hero product screenshot          | Placeholder UI capture; replace with a current release screenshot when ready |


## Planned / optional

Drop in real assets with these exact filenames when available; the README can
reference them without inventing new names.


| File               | Used for                       | Recommended size                              | Caption / content                                                                 |
| ------------------ | ------------------------------ | --------------------------------------------- | --------------------------------------------------------------------------------- |
| `hero.gif`         | Hero animation under the title | ~820px wide, up to about 8 MB, 5–8s loop      | Download, install, open app flow (launcher → browser at localhost:8080)           |
| `first-run.png`    | Screenshot gallery             | 1280×720 (16:9)                               | First-run setup wizard                                                            |
| `modules.png`      | Screenshot gallery             | 1280×720 (16:9)                               | Module selection (Free core + licensed Pro)                                       |
| `services.png`     | Screenshot gallery             | 1280×720 (16:9)                               | Service monitoring / container status                                             |
| `logs.png`         | Screenshot gallery             | 1280×720 (16:9)                               | Diagnostics / real-time logs                                                      |
| `architecture.svg` | "How it works" diagram         | —                                             | Launcher → Docker Engine → services                                               |
| `banner.png`       | Social / repo banner           | 1200×600                                      | GitHub social preview or README top banner                                        |


## Tips

- Re-export `logo.png` from the vector source:
  ```bash
  convert -background none ../../build/appicon.svg -resize 128x128 logo.png
  ```
- Record `hero.gif` at the launcher's native window size, then compress
  (for example `gifsicle -O3 --lossy=80`) to stay under about 8 MB.
- Prefer a consistent theme/window chrome across gallery screenshots.

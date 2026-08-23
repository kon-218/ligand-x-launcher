# README image assets

The main [README.md](../../README.md) references the files below. Keep files
reasonably small so the README loads fast on GitHub.

## Currently used


| File                  | Used for                | Notes                                                                              |
| --------------------- | ----------------------- | ---------------------------------------------------------------------------------- |
| `ligand-x-app-ui.png` | Hero product screenshot | Placeholder UI capture; replace with a current release screenshot when ready       |


## Planned / optional

Drop in real assets with these exact filenames when available.


| File               | Used for                       | Recommended size                         | Caption / content                                                       |
| ------------------ | ------------------------------ | ---------------------------------------- | ----------------------------------------------------------------------- |
| `logo.png`         | Header logo (optional)         | 120×120 square, transparent PNG          | Only add if it reads clearly on both light and dark GitHub themes       |
| `hero.gif`         | Hero animation                 | ~820px wide, up to ~8 MB, 5–8s loop      | Download → install → open app at localhost:8080                         |
| `first-run.png`    | Screenshot gallery             | 1280×720 (16:9)                          | First-run setup wizard                                                  |
| `modules.png`      | Screenshot gallery             | 1280×720 (16:9)                          | Module selection (Free core + licensed Pro)                             |
| `services.png`     | Screenshot gallery             | 1280×720 (16:9)                          | Service monitoring / container status                                   |
| `logs.png`         | Screenshot gallery             | 1280×720 (16:9)                          | Diagnostics / real-time logs                                            |
| `architecture.svg` | How-it-works diagram           | —                                        | Launcher → Docker Engine → services                                     |
| `banner.png`       | Social / repo banner           | 1200×600                                 | GitHub social preview                                                   |


## Tips

- A dark logo on a transparent background often looks blank on GitHub. Prefer a
  mark with enough contrast for light and dark themes, or skip the header logo.
- Record `hero.gif` at the launcher window size, then compress (for example
  `gifsicle -O3 --lossy=80`) to stay under about 8 MB.

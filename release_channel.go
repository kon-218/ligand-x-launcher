package main

// includePrereleases reports whether this install has opted into release
// candidates. Unreadable config means stable, the safe default.
func (a *App) includePrereleases() bool {
	config, err := a.GetLauncherConfig()
	if err != nil {
		return false
	}
	return config.ShowPrereleases
}

// GetShowPrereleases reports the current release channel for the UI toggle.
func (a *App) GetShowPrereleases() bool {
	return a.includePrereleases()
}

// SetShowPrereleases switches the release channel and persists it.
func (a *App) SetShowPrereleases(enabled bool) error {
	config, err := a.GetLauncherConfig()
	if err != nil {
		config = LauncherConfig{ConfigVersion: 1}
	}
	config.ShowPrereleases = enabled
	return a.SaveLauncherConfig(config)
}

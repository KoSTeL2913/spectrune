package main

func domainRouteSettingsPath() (string, error) { return "/var/lib/spectrune/domain-routes.json", nil }
func (s *Service) refreshDomainRoutesLocked() error {
	if s.bridge == nil {
		return nil
	}
	cfg, err := loadProfile(s.activeProfile)
	if err != nil {
		return err
	}
	s.bridge.UpdateIncludedDomainLists(cfg.IncludedDomainLists)
	return nil
}

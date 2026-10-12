package biz

func (p ProviderSettings) Supports(device, channel string) bool {
	if device == "" {
		device = "web"
	}
	devices := p.SupportedDevices
	if len(devices) == 0 {
		devices = []string{"web"}
	}
	found := false
	for _, v := range devices {
		if v == device {
			found = true
		}
	}
	if !found {
		return false
	}
	channels := p.DeviceChannels[device]
	if len(channels) == 0 {
		return device == "web"
	}
	for _, v := range channels {
		if v == channel {
			return true
		}
	}
	return false
}
func FilterProviders(providers []ProviderSettings, device string, items []TaskItem) []ProviderSettings {
	out := []ProviderSettings{}
	for _, p := range providers {
		valid := true
		for _, item := range items {
			if !p.Supports(device, item.PlatformCode) {
				valid = false
				break
			}
		}
		if valid {
			out = append(out, p)
		}
	}
	return out
}
